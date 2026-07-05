package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// TestTraceContextAttrs confirms the extractor emits trace/span ids for a valid
// span context and nothing for a bare context.
func TestTraceContextAttrs(t *testing.T) {
	if attrs := traceContextAttrs(context.Background(), nil); attrs != nil {
		t.Errorf("bare context yielded %v, want nil", attrs)
	}

	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	attrs := traceContextAttrs(ctx, nil)
	got := map[string]string{}
	for _, a := range attrs {
		got[a.Key] = a.Value.String()
	}
	if got["trace_id"] != tid.String() {
		t.Errorf("trace_id = %q, want %q", got["trace_id"], tid.String())
	}
	if got["span_id"] != sid.String() {
		t.Errorf("span_id = %q, want %q", got["span_id"], sid.String())
	}
}

// otelEnvKeys are every env var Enabled() consults; tests neutralize them for a
// clean baseline (t.Setenv to "" reads as unset via os.Getenv).
var otelEnvKeys = []string{
	"CORNUS_OTEL", "OTEL_SDK_DISABLED",
	"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER",
}

func clearOtelEnv(t *testing.T) {
	t.Helper()
	for _, k := range otelEnvKeys {
		t.Setenv(k, "")
	}
}

func TestEnabledGate(t *testing.T) {
	clearOtelEnv(t)
	if Enabled() {
		t.Fatal("Enabled() = true with no telemetry env")
	}

	t.Setenv("CORNUS_OTEL", "1")
	if !Enabled() {
		t.Fatal("Enabled() = false with CORNUS_OTEL=1")
	}

	// OTEL_SDK_DISABLED wins over everything.
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if Enabled() {
		t.Fatal("Enabled() = true with OTEL_SDK_DISABLED=true")
	}
}

func TestEnabledViaExporterEnv(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4317")
	if !Enabled() {
		t.Fatal("Enabled() = false with an OTLP endpoint set")
	}

	clearOtelEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	if !Enabled() {
		t.Fatal("Enabled() = false with OTEL_TRACES_EXPORTER set")
	}
}

func TestSetupDisabledIsNoop(t *testing.T) {
	clearOtelEnv(t)
	p, err := Setup(context.Background(), Options{ServiceName: "cornus-test"})
	if err != nil {
		t.Fatalf("Setup (disabled): %v", err)
	}
	if len(p.shutdowns) != 0 {
		t.Fatalf("disabled Setup installed %d providers, want 0", len(p.shutdowns))
	}
	// Instrumentation still works (as no-ops) and Shutdown is safe + idempotent.
	_, span := Tracer().Start(context.Background(), "noop")
	span.End()
	Logger().Info("noop")
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown (idempotent): %v", err)
	}
}

func TestPrometheusGateAndHandler(t *testing.T) {
	clearOtelEnv(t)

	// Flag alone, telemetry off: gate stays closed and no handler is installed.
	t.Setenv("CORNUS_METRICS_PROMETHEUS", "1")
	if PrometheusEnabled() {
		t.Fatal("PrometheusEnabled() = true with telemetry disabled")
	}
	if _, err := Setup(context.Background(), Options{ServiceName: "cornus-test"}); err != nil {
		t.Fatal(err)
	}
	if PrometheusHandler() != nil {
		t.Fatal("PrometheusHandler() non-nil with telemetry disabled")
	}

	// Telemetry on + flag on: gate opens and Setup installs the scrape handler.
	// Push signals to "none" keeps the enabled path hermetic.
	t.Setenv("CORNUS_OTEL", "1")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	if !PrometheusEnabled() {
		t.Fatal("PrometheusEnabled() = false with telemetry on and flag on")
	}
	p, err := Setup(context.Background(), Options{ServiceName: "cornus-test"})
	if err != nil {
		t.Fatal(err)
	}
	if PrometheusHandler() == nil {
		t.Fatal("PrometheusHandler() nil after enabled Setup with the flag on")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// A subsequent disabled Setup clears the process-global handler.
	clearOtelEnv(t)
	t.Setenv("CORNUS_METRICS_PROMETHEUS", "")
	if _, err := Setup(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if PrometheusHandler() != nil {
		t.Fatal("PrometheusHandler() not cleared by a later disabled Setup")
	}
}

func TestSetupEnabledInstallsProviders(t *testing.T) {
	clearOtelEnv(t)
	// "none" exporters keep the enabled path fully hermetic: real SDK providers
	// are installed, but nothing is exported over the network.
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	p, err := Setup(context.Background(), Options{ServiceName: "cornus-test", ServiceVersion: "v0"})
	if err != nil {
		t.Fatalf("Setup (enabled): %v", err)
	}
	if len(p.shutdowns) != 3 {
		t.Fatalf("enabled Setup installed %d providers, want 3 (traces/metrics/logs)", len(p.shutdowns))
	}

	// Exercise all three signals; none should panic.
	_, span := Tracer().Start(context.Background(), "unit")
	span.End()
	c, _ := Meter().Int64Counter("unit.count")
	c.Add(context.Background(), 1)
	Logger().Info("unit log")

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestInjectHTTPConformsToW3C confirms the propagator Setup installs — and that
// InjectHTTP (the shared client/caretaker injector) uses — is the standard
// composite of W3C Trace Context (traceparent) AND W3C Baggage (baggage), so an
// injected request carries both in their standards-defined header form. This is
// the conformance guarantee for the client <> server <> caretaker chain; every
// injection point routes through the global propagator this asserts.
func TestInjectHTTPConformsToW3C(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	p, err := Setup(context.Background(), Options{ServiceName: "cornus-test"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer p.Shutdown(context.Background())

	// A context carrying a sampled span (W3C Trace Context) and a baggage member
	// (W3C Baggage).
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	}))
	mem, err := baggage.NewMember("tenant", "acme")
	if err != nil {
		t.Fatalf("baggage.NewMember: %v", err)
	}
	bag, err := baggage.New(mem)
	if err != nil {
		t.Fatalf("baggage.New: %v", err)
	}
	ctx = baggage.ContextWithBaggage(ctx, bag)

	h := InjectHTTP(ctx)

	// W3C Trace Context: "version-traceid-spanid-flags" (flags 01 = sampled).
	const wantTP = "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"
	if got := h.Get("traceparent"); got != wantTP {
		t.Errorf("traceparent = %q, want %q", got, wantTP)
	}
	// W3C Baggage: "key=value".
	if got := h.Get("baggage"); got != "tenant=acme" {
		t.Errorf("baggage = %q, want %q", got, "tenant=acme")
	}
}
