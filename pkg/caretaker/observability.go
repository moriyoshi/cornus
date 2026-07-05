package caretaker

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"cornus/pkg/observability"
	"cornus/pkg/wire"
)

// ctMetrics are the caretaker's OTel instruments, one set per process. They come
// from the global meter, which is a no-op (so the instruments are cheap no-ops)
// unless telemetry was enabled at startup (see pkg/observability).
type ctMetrics struct {
	mountUp      metric.Int64UpDownCounter
	mountFail    metric.Int64Counter
	mountLatency metric.Float64Histogram
	mountBytes   metric.Int64Counter
	proxyConns   metric.Int64Counter
	proxyBytes   metric.Int64Counter
	egressConns  metric.Int64Counter
	egressBytes  metric.Int64Counter
	dnsQueries   metric.Int64Counter
}

var (
	metricsOnce sync.Once
	ctm         *ctMetrics
	ctTracer    trace.Tracer
)

// metrics lazily builds (once) and returns the caretaker instrument set and, as
// a side effect, caches the tracer. Instrument-creation errors are ignored: the
// SDK returns a working no-op instrument on error.
func metrics() *ctMetrics {
	metricsOnce.Do(func() {
		ctTracer = observability.Tracer()
		m := observability.Meter()
		mountUp, _ := m.Int64UpDownCounter("caretaker.mounts.active",
			metric.WithDescription("Live 9P mounts currently held by the caretaker."))
		mountFail, _ := m.Int64Counter("caretaker.mount.failures",
			metric.WithDescription("9P mount establishment failures."))
		mountLatency, _ := m.Float64Histogram("caretaker.mount.setup.duration",
			metric.WithUnit("s"),
			metric.WithDescription("Time to establish a 9P mount (relay dial to live)."))
		mountBytes, _ := m.Int64Counter("caretaker.mount.io.bytes",
			metric.WithUnit("By"),
			metric.WithDescription("Bytes transferred over 9P mounts, by mount name and direction (rx/tx)."))
		proxyConns, _ := m.Int64Counter("caretaker.proxy.connections",
			metric.WithDescription("Proxy connections handled, by outcome (allow/deny/error)."))
		proxyBytes, _ := m.Int64Counter("caretaker.proxy.bytes",
			metric.WithUnit("By"),
			metric.WithDescription("Bytes relayed by the proxy, by direction (inbound/outbound)."))
		egressConns, _ := m.Int64Counter("caretaker.egress.connections",
			metric.WithDescription("Client-side-egress connections, by route (client/gateway/cluster/deny/error) and protocol."))
		egressBytes, _ := m.Int64Counter("caretaker.egress.bytes",
			metric.WithUnit("By"),
			metric.WithDescription("Bytes relayed by client-side egress, by direction (inbound/outbound)."))
		dnsQueries, _ := m.Int64Counter("caretaker.dns.queries",
			metric.WithDescription("DNS queries handled, by disposition (local/nodata/forward)."))
		ctm = &ctMetrics{mountUp, mountFail, mountLatency, mountBytes, proxyConns, proxyBytes, egressConns, egressBytes, dnsQueries}
	})
	return ctm
}

// tracer returns the caretaker tracer (after ensuring metrics/tracer are built).
func tracer() trace.Tracer {
	metrics()
	return ctTracer
}

// propagationHeader returns HTTP headers carrying the current span's W3C trace
// context, so the relay server can continue the trace. Empty when telemetry is
// disabled (the global propagator is then a no-op).
func propagationHeader(ctx context.Context) http.Header {
	return observability.InjectHTTP(ctx)
}

// meterMountStream wraps a caretaker mount stream so bytes read from the server
// (delivered into the pod) count as rx and bytes written to the server as tx,
// under the named mount.
func meterMountStream(mt *ctMetrics, name string, stream net.Conn) net.Conn {
	rx := metric.WithAttributes(attribute.String("name", name), attribute.String("direction", "rx"))
	tx := metric.WithAttributes(attribute.String("name", name), attribute.String("direction", "tx"))
	return &wire.MeteredConn{
		Conn:    stream,
		OnRead:  func(n int) { mt.mountBytes.Add(context.Background(), int64(n), rx) },
		OnWrite: func(n int) { mt.mountBytes.Add(context.Background(), int64(n), tx) },
	}
}

// traceMountBytes wraps a caretaker mount stream so span — the mount's
// caretaker.mount span — carries the stream's total byte counts when it ends
// (rx = bytes read from the server, i.e. delivered into the pod; tx = bytes
// written to it, matching caretaker.mount.io.bytes). finish stamps the totals
// as span attributes and must run before the span ends. Zero-cost when
// telemetry is off: a non-recording span gets the stream back untouched and a
// no-op finish, so the data path is byte-identical.
func traceMountBytes(span trace.Span, stream net.Conn) (net.Conn, func()) {
	if !span.IsRecording() {
		return stream, func() {}
	}
	var rx, tx atomic.Int64
	wrapped := &wire.MeteredConn{
		Conn:    stream,
		OnRead:  func(n int) { rx.Add(int64(n)) },
		OnWrite: func(n int) { tx.Add(int64(n)) },
	}
	return wrapped, func() {
		span.SetAttributes(
			attribute.Int64("caretaker.mount.bytes.rx", rx.Load()),
			attribute.Int64("caretaker.mount.bytes.tx", tx.Load()),
		)
	}
}

// connAttr labels a proxy-connection measurement with its outcome.
func connAttr(outcome string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("outcome", outcome))
}

// dnsAttr labels a DNS-query measurement with its disposition.
func dnsAttr(disposition string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("disposition", disposition))
}

// recordProxyBytes records the two directional byte counts of a spliced
// connection (ab = source->upstream = outbound, ba = upstream->source = inbound).
func recordProxyBytes(mt *ctMetrics, ab, ba int64) {
	ctx := context.Background()
	if ab > 0 {
		mt.proxyBytes.Add(ctx, ab, metric.WithAttributes(attribute.String("direction", "outbound")))
	}
	if ba > 0 {
		mt.proxyBytes.Add(ctx, ba, metric.WithAttributes(attribute.String("direction", "inbound")))
	}
}

// egressAttr labels an egress-connection measurement with its route verdict and
// the inbound protocol (http/http-connect/socks5/transparent).
func egressAttr(route, proto string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("route", route), attribute.String("proto", proto))
}

// recordEgressBytes records the two directional byte counts of a spliced egress
// connection (ab = app->upstream = outbound, ba = upstream->app = inbound).
func recordEgressBytes(mt *ctMetrics, ab, ba int64) {
	ctx := context.Background()
	if ab > 0 {
		mt.egressBytes.Add(ctx, ab, metric.WithAttributes(attribute.String("direction", "outbound")))
	}
	if ba > 0 {
		mt.egressBytes.Add(ctx, ba, metric.WithAttributes(attribute.String("direction", "inbound")))
	}
}
