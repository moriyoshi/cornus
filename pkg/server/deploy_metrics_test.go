package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"cornus/pkg/api"
)

// TestDeployCounterIgnoresReads drives the REAL HTTP surface and asserts what
// cornus.deploys ends up holding: a deployment that was applied, and nothing for
// the reads.
//
// The reads are the whole point. `list` and `status` are what the web UI polls —
// every 3s and 4s — so counting them made an idle browser add ~1200 to "Deploys"
// an hour, and the dashboard's Deploys panel climbed while nothing was deployed.
// The counter was measuring its own observer.
//
// It goes through http.Post/Get rather than calling traceDeploy directly, because
// the claim is about which ROUTES count, not about what the helper does when
// handed a string. A test that called traceDeploy("list", ...) would keep passing
// if the list handler were rewired tomorrow to pass "apply".
func TestDeployCounterIgnoresReads(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	fb := &fakeBackend{}
	srv := newTestServer(t, fb) // New() -> newInstruments() binds to the manual reader
	defer srv.Close()

	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"}
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Poll the two read routes hard enough that a counter recording them could not
	// be mistaken for noise.
	for i := 0; i < 5; i++ {
		r1, err := http.Get(srv.URL + "/.cornus/v1/deploy")
		if err != nil {
			t.Fatal(err)
		}
		r1.Body.Close()
		r2, err := http.Get(srv.URL + "/.cornus/v1/deploy/web")
		if err != nil {
			t.Fatal(err)
		}
		r2.Body.Close()
	}

	counts := deployCounts(t, reader)
	if counts["apply"] != 1 {
		t.Errorf("cornus.deploys{action=apply} = %d, want 1 — the mutating action must still be counted", counts["apply"])
	}
	for _, read := range []string{"list", "status"} {
		if n, ok := counts[read]; ok {
			t.Errorf("cornus.deploys{action=%s} = %d, want no series at all: %s is a read the web UI polls, "+
				"and counting it makes the Deploys panel climb while nothing is deployed", read, n, read)
		}
	}
}

// deployCounts sums cornus.deploys by its `action` attribute. An action with no
// datapoint is absent from the map, which is what the read assertions check —
// "recorded zero" and "never recorded" are different claims, and only the second
// one is true of a read.
func deployCounts(t *testing.T, reader sdkmetric.Reader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cornus.deploys" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("cornus.deploys is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				a, _ := dp.Attributes.Value("action")
				out[a.AsString()] += dp.Value
			}
		}
	}
	return out
}
