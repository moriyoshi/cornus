package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/deploy"
)

// statsRecorderBackend captures the StatsOptions the handler built. deploy.Backend
// is embedded (and nil) so only Stats is reachable; anything else would panic
// loudly rather than quietly answering a zero value.
type statsRecorderBackend struct {
	deploy.Backend
	onStats func(api.StatsOptions)
}

func (b *statsRecorderBackend) Stats(_ context.Context, _ string, opts api.StatsOptions, w io.Writer) error {
	b.onStats(opts)
	_, err := w.Write([]byte("{}\n"))
	return err
}

// The per-replica half of Backend.Stats, guarded on both sides of the wire.
//
// This is the same defect that already shipped once for logs (see
// TestLogOptionsInstanceCrossesTheWire): the interface, every backend, and the
// caller can all be correct while the option never leaves the client, because
// options travel to the server as query parameters and nothing fails when one is
// missing. For metrics the symptom is worse than missing data — a collector
// enumerating N replicas records instance 0 N times, each stamped with a
// different ordinal, which renders as a perfectly balanced workload.

func TestStatsOptionsInstanceCrossesTheWire(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("instance")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	if err := c.Stats(context.Background(), "web", api.StatsOptions{Instance: 2}, io.Discard); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got != "2" {
		t.Errorf("instance query parameter = %q, want \"2\"", got)
	}

	// The default stays off the wire, so a request for the common case is
	// byte-identical to what it was before the field existed.
	if err := c.Stats(context.Background(), "web", api.StatsOptions{}, io.Discard); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got != "" {
		t.Errorf("instance = %q for the default; it must not be sent", got)
	}
}

// TestServerParsesStatsInstance is the receiving half: the handler must read the
// parameter the client now sends, and must not turn a malformed one into a
// replica index.
func TestServerParsesStatsInstance(t *testing.T) {
	var got api.StatsOptions
	backend := &statsRecorderBackend{onStats: func(o api.StatsOptions) { got = o }}
	s := &Server{}

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 0},
		{"?instance=0", 0},
		{"?instance=3", 3},
		{"?instance=-1", 0},
		{"?instance=nonsense", 0},
	} {
		r := httptest.NewRequest(http.MethodGet, "/.cornus/v1/deploy/web/stats"+tc.query, nil)
		s.handleDeployStats(httptest.NewRecorder(), r, backend, "web")
		if got.Instance != tc.want {
			t.Errorf("query %q: Instance = %d, want %d", tc.query, got.Instance, tc.want)
		}
	}
}
