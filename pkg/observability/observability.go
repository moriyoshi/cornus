// Package observability wires OpenTelemetry into cornus's two long-lived
// processes: the unified HTTP server (cornus serve) and the per-pod sidecar
// (the caretaker). It is the single setup seam both use.
//
// Design:
//
//   - Opt-in. Setup installs SDK providers only when telemetry is requested
//     (see Enabled): any of the standard OTEL_* exporter/endpoint env vars, or
//     CORNUS_OTEL. Otherwise the OpenTelemetry API stays at its no-op default,
//     so instrumentation call sites cost effectively nothing and no exporter
//     goroutines or network connections are started.
//   - Env-driven. Exporters, protocol (grpc vs http/protobuf), endpoint, headers,
//     sampler, and resource attributes are all selected by the standard OTEL_*
//     environment via go.opentelemetry.io/contrib/exporters/autoexport — cornus
//     adds no parallel configuration surface.
//   - Three signals. Traces, metrics (plus Go runtime metrics), and logs are all
//     configured and set as the global providers.
//
// This is cornus-owned telemetry at the server/sidecar layer; it is unrelated
// to the in-process BuildKit solver, which deliberately omits BuildKit's own
// OTEL (see ARCHITECTURE.md).
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	otellog "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"

	"cornus/pkg/logging"
)

// ScopeName is the instrumentation scope cornus's own spans, metrics, and log
// records are recorded under.
const ScopeName = "cornus"

// Options configures Setup.
type Options struct {
	// ServiceName is the OTel service.name (e.g. "cornus" or
	// "cornus-caretaker"). Overridden by OTEL_SERVICE_NAME if set.
	ServiceName string
	// ServiceVersion is the OTel service.version (the cornus build version).
	ServiceVersion string
}

// Providers holds the shutdown hooks for the installed SDK providers. A zero
// Providers (returned when telemetry is disabled) has a no-op Shutdown, so
// callers can unconditionally `defer p.Shutdown(ctx)`.
type Providers struct {
	shutdowns []func(context.Context) error
}

// promHandler is the /metrics scrape handler backed by the Prometheus metric
// reader installed by the most recent Setup. It is a process-global (mirroring
// the global trace/metric/log providers Setup installs) so the server can reach
// it without threading Providers through construction. nil unless the Prometheus
// pull exporter is active (see PrometheusEnabled).
var promHandler http.Handler

// PrometheusHandler returns the /metrics scrape handler backed by the Prometheus
// metric reader installed by Setup, or nil when the Prometheus pull exporter is
// not active (telemetry disabled, or CORNUS_METRICS_PROMETHEUS not truthy). The
// server registers /metrics only when this is non-nil.
func PrometheusHandler() http.Handler { return promHandler }

// PrometheusEnabled reports whether the optional Prometheus pull /metrics exporter
// should be installed as an ADDITIONAL metric reader (alongside the OTLP push
// path). It is off by default and layered on top of the telemetry enable gate:
// it takes effect only when telemetry is already Enabled AND
// CORNUS_METRICS_PROMETHEUS is truthy, so it stays off whenever telemetry is off.
func PrometheusEnabled() bool {
	return Enabled() && truthy(os.Getenv("CORNUS_METRICS_PROMETHEUS"))
}

// Enabled reports whether telemetry should be installed. It is opt-in: true when
// CORNUS_OTEL is truthy or any standard OTLP endpoint/exporter env var is set,
// and always false when OTEL_SDK_DISABLED is truthy (which wins).
func Enabled() bool {
	if truthy(os.Getenv("OTEL_SDK_DISABLED")) {
		return false
	}
	if truthy(os.Getenv("CORNUS_OTEL")) {
		return true
	}
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
		"OTEL_TRACES_EXPORTER",
		"OTEL_METRICS_EXPORTER",
		"OTEL_LOGS_EXPORTER",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// Setup installs global OpenTelemetry providers for traces, metrics, and logs,
// plus the W3C trace-context + baggage propagator, all driven by the standard
// OTEL_* environment. When telemetry is disabled (see Enabled) it is a no-op and
// returns a Providers whose Shutdown does nothing. On a partial failure it shuts
// down whatever it already installed before returning the error.
func Setup(ctx context.Context, opts Options) (*Providers, error) {
	p := &Providers{}
	// Clear any handler from a previous Setup so a disabled (or Prometheus-off)
	// run leaves no stale /metrics handler behind.
	promHandler = nil
	if !Enabled() {
		return p, nil
	}

	res, err := buildResource(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("observability: resource: %w", err)
	}

	// Cross-cutting propagation so the caretaker's spans link to the server's
	// (the mount-relay dial injects this on its way out).
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if err := p.setupTraces(ctx, res); err != nil {
		return p.fail(ctx, err)
	}
	if err := p.setupMetrics(ctx, res); err != nil {
		return p.fail(ctx, err)
	}
	if err := p.setupLogs(ctx, res); err != nil {
		return p.fail(ctx, err)
	}
	// Fan the process logger out to the OTel LoggerProvider installed above, so
	// slog records ride the same OTLP pipeline as traces and metrics.
	logging.InitWith(otelslog.NewHandler(ScopeName))
	// Fold the active span's trace/span ids onto every logger FromContext hands
	// out, so the human-readable stderr logs carry the same correlation the OTLP
	// bridge derives natively. Reads the span context that the *Context log
	// methods thread through.
	logging.SetContextAttrs(traceContextAttrs)
	return p, nil
}

// traceContextAttrs appends the active span's trace and span ids to dst, or
// leaves dst untouched when ctx carries no valid span. Visitor-style
// (logging.ContextAttrHook) so it appends in place without its own allocation.
func traceContextAttrs(ctx context.Context, dst []slog.Attr) []slog.Attr {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return dst
	}
	return append(dst,
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	)
}

func (p *Providers) setupTraces(ctx context.Context, res *resource.Resource) error {
	exp, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return fmt.Errorf("observability: span exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exp),
	)
	otel.SetTracerProvider(tp)
	p.shutdowns = append(p.shutdowns, tp.Shutdown)
	return nil
}

func (p *Providers) setupMetrics(ctx context.Context, res *resource.Resource) error {
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fmt.Errorf("observability: metric reader: %w", err)
	}
	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	}
	// Optional Prometheus pull exporter as an ADDITIONAL reader on the same
	// provider, off unless explicitly enabled. It is self-contained (its own
	// client_golang registry + handler) and never touches the OTLP autoexport
	// reader above.
	if PrometheusEnabled() {
		promReader, handler, err := newPrometheusReader()
		if err != nil {
			return fmt.Errorf("observability: prometheus exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(promReader))
		promHandler = handler
	}
	mp := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(mp)
	p.shutdowns = append(p.shutdowns, mp.Shutdown)
	// Go runtime metrics (GC, goroutines, memory) under the same provider.
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return fmt.Errorf("observability: runtime metrics: %w", err)
	}
	return nil
}

func (p *Providers) setupLogs(ctx context.Context, res *resource.Resource) error {
	exp, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return fmt.Errorf("observability: log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	otellog.SetLoggerProvider(lp)
	p.shutdowns = append(p.shutdowns, lp.Shutdown)
	return nil
}

// newPrometheusReader builds the Prometheus pull exporter (a sdkmetric.Reader)
// over a dedicated client_golang registry, plus the matching /metrics scrape
// handler. Keeping its own registry (rather than the global default) keeps the
// exposed series confined to cornus's meter provider and makes the exporter
// self-contained, so it composes cleanly as an extra reader without entangling
// the OTLP autoexport path.
func newPrometheusReader() (sdkmetric.Reader, http.Handler, error) {
	reg := promclient.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, nil, err
	}
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return exporter, handler, nil
}

// buildResource assembles the resource from env + process/host/container
// detectors, with cornus's service identity as the base. A non-fatal detector
// error (e.g. a schema-URL mismatch) that still yields a resource is tolerated.
func buildResource(ctx context.Context, opts Options) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithProcess(),
		resource.WithHost(),
		resource.WithContainer(),
		resource.WithAttributes(
			semconv.ServiceName(opts.ServiceName),
			semconv.ServiceVersion(opts.ServiceVersion),
		),
	)
	if err != nil && res == nil {
		return nil, err
	}
	return res, nil
}

// fail shuts down anything already installed and returns err, so a partial Setup
// never leaks provider goroutines.
func (p *Providers) fail(ctx context.Context, err error) (*Providers, error) {
	_ = p.Shutdown(ctx)
	return nil, err
}

// Shutdown flushes and stops every installed provider (in reverse order),
// joining any errors. Safe to call on a disabled/zero Providers.
func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(p.shutdowns) - 1; i >= 0; i-- {
		if err := p.shutdowns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	p.shutdowns = nil
	return errors.Join(errs...)
}

// InjectHTTP returns HTTP headers carrying ctx's active span's W3C trace context
// (traceparent/tracestate + baggage), so a downstream cornus process continues
// the same trace. It is the client/caretaker counterpart to the otelhttp server
// handler's automatic extraction. The returned header is empty when telemetry is
// disabled (the global propagator is then a no-op) or when ctx carries no valid
// span, so callers can inject unconditionally at zero cost.
func InjectHTTP(ctx context.Context) http.Header {
	h := http.Header{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
	return h
}

// Tracer returns the cornus tracer from the global provider (a no-op tracer
// when telemetry is disabled).
func Tracer() trace.Tracer { return otel.Tracer(ScopeName) }

// Meter returns the cornus meter from the global provider (a no-op meter when
// telemetry is disabled).
func Meter() metric.Meter { return otel.Meter(ScopeName) }

// Logger returns the process default logger (slog.Default). After a successful
// enabled Setup it fans out to both stderr and the OTel LoggerProvider; before
// Setup (or when disabled) it is whatever logging.Init installed.
func Logger() *slog.Logger { return slog.Default() }

// truthy parses a loose boolean env value.
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
