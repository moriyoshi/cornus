package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cornus/pkg/config"
	"cornus/pkg/obsstore"
	"cornus/pkg/storage"
)

func newObsServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg.DataDir = dir
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closeResources)
	return s
}

// TestObsDisabledByDefault pins the default posture: no config, no store, and
// therefore no cost. A server that quietly began recording every workload's
// output because someone linked a new package would be a surprise, not a
// feature.
func TestObsDisabledByDefault(t *testing.T) {
	s := newObsServer(t, config.Config{})
	if s.obsEnabled() {
		t.Fatal("observability store is live without ObsEnabled")
	}
}

// TestObsRouteAbsentWhenOff is the zero-cost-when-off contract at the routing
// layer, mirroring how /metrics is registered only when the Prometheus reader
// exists: a server without the store must answer 404, not 500 or 503, because
// the honest statement is "this server does not have that endpoint".
func TestObsRouteAbsentWhenOff(t *testing.T) {
	s := newObsServer(t, config.Config{})
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.cornus/v1/obs/status", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET obs/status with the store off = %d, want 404", rec.Code)
	}
}

// TestObsEnabledWithoutTagStaysOff is the important degradation case. Asking for
// the store in a binary that cannot provide it must leave the server fully
// functional with the feature simply absent — never a failed startup, because
// observability is what you reach for when something ELSE is broken.
//
// In a build WITH the tag this same config really does open a store, so the
// assertion is written against obsstore.Compiled() rather than against a fixed
// expectation.
func TestObsEnabledMatchesCompiledIn(t *testing.T) {
	s := newObsServer(t, config.Config{ObsEnabled: true})
	if got, want := s.obsEnabled(), obsstore.Compiled(); got != want {
		t.Fatalf("obsEnabled() = %v, want %v (obsstore.Compiled() = %v)", got, want, obsstore.Compiled())
	}
	// Either way the server is usable: an unrelated route still answers.
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200; enabling the store must not break the server", rec.Code)
	}
}

// TestObservabilityDirDefaultsUnderDataDir covers the placement rule. It matters
// for the in-container server topology: the store must live in server-owned
// state that is never handed to a container runtime, so it needs no host-path
// translation.
func TestObservabilityDirDefaultsUnderDataDir(t *testing.T) {
	cfg := config.Config{DataDir: "/var/lib/cornus"}
	if got, want := cfg.ObservabilityDir(), filepath.Join("/var/lib/cornus", "observability"); got != want {
		t.Errorf("ObservabilityDir() = %q, want %q", got, want)
	}
	cfg.ObsDir = "/mnt/obs"
	if got := cfg.ObservabilityDir(); got != "/mnt/obs" {
		t.Errorf("explicit ObsDir ignored: got %q", got)
	}
}

// TestCloseObsStoreIsIdempotent guards the shutdown path: closeResources runs
// once on a clean stop, but a test (or a future double-unwind) must not panic.
func TestCloseObsStoreIsIdempotent(t *testing.T) {
	s := newObsServer(t, config.Config{ObsEnabled: true})
	s.closeObsStore()
	s.closeObsStore()
	if s.obsEnabled() {
		t.Error("store still reports enabled after close")
	}
}
