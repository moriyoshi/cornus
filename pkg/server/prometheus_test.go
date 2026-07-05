package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/observability"
)

// TestPrometheusMetricsEndpoint covers the optional Prometheus pull endpoint end
// to end: OFF by default there is no /metrics route and no exporter handler; ON
// (telemetry enabled + CORNUS_METRICS_PROMETHEUS) /metrics answers 200 with
// Prometheus text output. Push exporters are pinned to "none" so the test stays
// hermetic (no OTLP connection attempts).
func TestPrometheusMetricsEndpoint(t *testing.T) {
	ctx := context.Background()

	// --- OFF: no telemetry env. Force a clean disabled Setup so no stale global
	// Prometheus handler leaks in from another test. ---
	for _, k := range []string{"CORNUS_OTEL", "CORNUS_METRICS_PROMETHEUS",
		"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER",
		"OTEL_EXPORTER_OTLP_ENDPOINT"} {
		t.Setenv(k, "")
	}
	if _, err := observability.Setup(ctx, observability.Options{}); err != nil {
		t.Fatal(err)
	}
	if observability.PrometheusHandler() != nil {
		t.Fatal("expected no Prometheus handler when telemetry is disabled")
	}

	off := newTestServer(t, &fakeBackend{})
	resp, err := http.Get(off.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("OFF: GET /metrics = %d, want 404 (route absent)", resp.StatusCode)
	}
	off.Close()

	// --- ON: telemetry + Prometheus, push signals to none for hermeticity. ---
	t.Setenv("CORNUS_OTEL", "1")
	t.Setenv("CORNUS_METRICS_PROMETHEUS", "1")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	providers, err := observability.Setup(ctx, observability.Options{ServiceName: "cornus-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = providers.Shutdown(sctx)
		// Clear the process-global Prometheus handler so later tests in this
		// package see no /metrics route again.
		os.Unsetenv("CORNUS_OTEL")
		os.Unsetenv("CORNUS_METRICS_PROMETHEUS")
		_, _ = observability.Setup(context.Background(), observability.Options{})
	})
	if observability.PrometheusHandler() == nil {
		t.Fatal("expected a Prometheus handler when enabled")
	}

	on := newTestServer(t, &fakeBackend{})
	defer on.Close()

	// Drive one deploy so cornus's own instruments emit at least one series
	// (target_info is emitted regardless, but this exercises app metrics too).
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"}
	body, _ := json.Marshal(spec)
	if r, err := http.Post(on.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body)); err == nil {
		r.Body.Close()
	}

	resp, err = http.Get(on.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ON: GET /metrics = %d, want 200", resp.StatusCode)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "# HELP") && !strings.Contains(text, "# TYPE") {
		t.Fatalf("ON: /metrics body has no Prometheus HELP/TYPE lines:\n%s", text)
	}
}
