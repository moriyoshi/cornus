package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"

	"cornus/pkg/blockcache"
	"cornus/pkg/observability"
	"cornus/pkg/wire"
)

// instruments holds the server's own OTel metric instruments for build and
// deploy operations. They are created from the global meter, which is a no-op
// (so the instruments are cheap no-ops) unless telemetry was enabled at startup.
type instruments struct {
	builds     metric.Int64Counter
	buildDur   metric.Float64Histogram
	deploys    metric.Int64Counter
	mountBytes metric.Int64Counter
}

// newInstruments creates the build/deploy instruments. Instrument-creation
// errors are ignored: on error the SDK returns a working no-op instrument, so a
// failure only means that particular series is not recorded.
func newInstruments() instruments {
	m := observability.Meter()
	builds, _ := m.Int64Counter("cornus.builds",
		metric.WithDescription("Image builds served, by outcome."))
	buildDur, _ := m.Float64Histogram("cornus.build.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of image builds."))
	deploys, _ := m.Int64Counter("cornus.deploys",
		metric.WithDescription("Deploy operations served, by action and outcome."))
	mountBytes, _ := m.Int64Counter("cornus.mount.io.bytes",
		metric.WithUnit("By"),
		metric.WithDescription("Bytes relayed to/from client-local 9P mounts, by mount name and direction (rx/tx)."))
	return instruments{builds: builds, buildDur: buildDur, deploys: deploys, mountBytes: mountBytes}
}

// registerFileCacheMetrics registers an observable gauge reporting the current
// on-disk size (bytes) of the per-file block cache, so operators can watch it
// grow and size the size cap / prune cadence. It is a no-op when the cache is
// disabled; when telemetry is off the callback binds to a no-op meter and never
// fires. The callback walks the cache directory on collection (scrape), which is
// infrequent.
func (s *Server) registerFileCacheMetrics() {
	if s.fileCache == nil {
		return
	}
	dir := s.cfg.FileCacheDir
	_, _ = observability.Meter().Int64ObservableGauge(
		"cornus.filecache.size.bytes",
		metric.WithUnit("By"),
		metric.WithDescription("Current on-disk size of the per-file block cache backing files."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if b, _, err := blockcache.DiskUsage(dir); err == nil {
				o.Observe(b)
			}
			return nil
		}),
	)
}

// mountMeter returns per-mount rx/tx byte callbacks that record
// cornus.mount.io.bytes for the named mount (rx = bytes into the container,
// tx = bytes out). Used by the dockerhost MountManager (SetMeter) and, via
// meterMountConn, the k8s caretaker relay.
func (s *Server) mountMeter(name string) (onRx, onTx func(int)) {
	rx := metric.WithAttributes(attribute.String("name", name), attribute.String("direction", "rx"))
	tx := metric.WithAttributes(attribute.String("name", name), attribute.String("direction", "tx"))
	return func(n int) { s.metrics.mountBytes.Add(context.Background(), int64(n), rx) },
		func(n int) { s.metrics.mountBytes.Add(context.Background(), int64(n), tx) }
}

// meterMountConn wraps the pod-side conn of a mount relay so bytes written toward
// the pod count as rx and bytes read from it as tx, under the named mount.
func (s *Server) meterMountConn(name string, c net.Conn) net.Conn {
	onRx, onTx := s.mountMeter(name)
	return &wire.MeteredConn{Conn: c, OnRead: onTx, OnWrite: onRx}
}

// Relay outcomes recorded as mount-relay span statuses. They exist for
// telemetry only: the relay deliberately tells the caretaker nothing beyond
// closing the stream (see relayMountMuxed / relayMountRemote).
var (
	errMountNotAllowed = errors.New("mount name not allowed for session")
	errMountNoOwner    = errors.New("no live owner for mount session")
)

// traceMountRelay starts the per-connection span for ONE relayed mount stream,
// named cornus.mount.relay. It is called at the caretaker-facing edge only
// (mirroring meterMountConn's placement), so each stream is traced exactly once
// cluster-wide. transport is "local" (session held by this process) or
// "forwarded" (inter-replica hop via /.cornus/v1/mount/forward). The session id is a
// capability, so only its digest (the same one mountServiceName derives) is
// recorded. The span parents to whatever ctx carries — the otelhttp span of the
// pod's /.cornus/v1/caretaker/attach handshake — so every mount conn links back to its
// caretaker connection's trace; with no parent it is a root span.
//
// It returns the conn to relay and a finish func that stamps rx/tx byte totals
// (rx = bytes toward the pod, tx = bytes from it, matching cornus.mount.io.bytes),
// records err as the span status, and ends the span (duration = span lifetime).
// Zero-cost when telemetry is off: a non-recording span is ended immediately and
// c is returned untouched — no byte-counting wrapper on the data path.
func (s *Server) traceMountRelay(ctx context.Context, session, name, transport string, c net.Conn) (net.Conn, func(error)) {
	_, span := s.tracer.Start(ctx, "cornus.mount.relay")
	if !span.IsRecording() {
		span.End()
		return c, func(error) {}
	}
	span.SetAttributes(
		attribute.String("cornus.mount.session", strings.TrimPrefix(mountServiceName(session), mountServicePrefix)),
		attribute.String("cornus.mount.name", name),
		attribute.String("cornus.mount.transport", transport),
	)
	var rx, tx atomic.Int64
	wrapped := &wire.MeteredConn{
		Conn:    c,
		OnRead:  func(n int) { tx.Add(int64(n)) }, // read from the pod-side conn = tx
		OnWrite: func(n int) { rx.Add(int64(n)) }, // written toward the pod = rx
	}
	return wrapped, func(err error) {
		span.SetAttributes(
			attribute.Int64("cornus.mount.bytes.rx", rx.Load()),
			attribute.Int64("cornus.mount.bytes.tx", tx.Load()),
		)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// traceDeploy runs a deploy backend operation under a span named
// "cornus.deploy.<action>" and records the cornus.deploys counter keyed by
// action and outcome. name, when non-empty, is attached as a span attribute (it
// is not a metric dimension, to keep cardinality bounded). It returns fn's error.
func (s *Server) traceDeploy(ctx context.Context, action, name string, fn func(context.Context) error) error {
	ctx, span := s.tracer.Start(ctx, "cornus.deploy."+action)
	defer span.End()
	span.SetAttributes(attribute.String("cornus.deploy.action", action))
	if name != "" {
		span.SetAttributes(attribute.String("cornus.deploy.name", name))
	}
	err := fn(ctx)
	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	s.metrics.deploys.Add(ctx, 1, metric.WithAttributes(
		attribute.String("action", action),
		attribute.String("outcome", outcome),
	))
	return err
}

// otelHandler wraps h with OpenTelemetry HTTP server instrumentation. Span names
// are the request method plus a low-cardinality route template (see
// routePattern), so high-cardinality path components (digests, deployment names,
// upload UUIDs) never blow up span or metric series.
func otelHandler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "cornus.http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + routePattern(r.URL.Path)
		}),
	)
}

// routePattern maps a request path to a low-cardinality route template.
func routePattern(path string) string {
	switch path {
	case "/healthz", "/readyz", "/metrics",
		"/.cornus/v1/build", "/.cornus/v1/build/attach",
		"/.cornus/v1/deploy", "/.cornus/v1/deploy/attach", "/.cornus/v1/caretaker/attach", "/.cornus/v1/hub/catalog",
		"/v2", "/v2/", "/v2/_catalog":
		return path
	}
	switch {
	case strings.HasPrefix(path, "/.cornus/v1/deploy/exec/"):
		return "/.cornus/v1/deploy/exec/{id}"
	case strings.HasPrefix(path, "/.cornus/v1/deploy/"):
		rest := strings.TrimPrefix(path, "/.cornus/v1/deploy/")
		// action is a bounded set (start/stop/restart/logs/stats/archive/...),
		// so it is safe — and useful — to keep in the template.
		if _, action, ok := strings.Cut(rest, "/"); ok {
			return "/.cornus/v1/deploy/{name}/" + action
		}
		return "/.cornus/v1/deploy/{name}"
	case strings.HasPrefix(path, "/v2/"):
		return registryRoutePattern(path)
	}
	return path
}

// registryRoutePattern collapses an OCI /v2/* path (whose repository name may
// itself contain slashes) to its route template.
func registryRoutePattern(path string) string {
	switch {
	case strings.Contains(path, "/blobs/uploads"):
		// ".../blobs/uploads" or ".../blobs/uploads/" is the initiate endpoint;
		// a session id after it is a chunk PATCH/PUT.
		i := strings.Index(path, "/blobs/uploads")
		if strings.Trim(path[i+len("/blobs/uploads"):], "/") != "" {
			return "/v2/{name}/blobs/uploads/{uuid}"
		}
		return "/v2/{name}/blobs/uploads"
	case strings.Contains(path, "/blobs/"):
		return "/v2/{name}/blobs/{digest}"
	case strings.Contains(path, "/manifests/"):
		return "/v2/{name}/manifests/{reference}"
	case strings.HasSuffix(path, "/tags/list"):
		return "/v2/{name}/tags/list"
	}
	return "/v2/{name}"
}
