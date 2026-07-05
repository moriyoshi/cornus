package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
)

// TestInfoAdvertisesVersion proves GET /.cornus/v1/info reports the server's build
// version, which a client uses for its skew check. No deploy backend is configured,
// so advertisedRegistry/Ingress return empty — only Version is asserted.
func TestInfoAdvertisesVersion(t *testing.T) {
	s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, Version: "1.2.3"}
	rec := httptest.NewRecorder()
	s.handleInfo(rec, httptest.NewRequest(http.MethodGet, "/.cornus/v1/info", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var info api.ServerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != "1.2.3" {
		t.Fatalf("info.Version = %q, want 1.2.3", info.Version)
	}
}

func TestParseAdvertiseRegistry(t *testing.T) {
	tests := []struct {
		in         string
		wantHost   string
		wantScheme string
	}{
		{"reg.example:5000", "reg.example:5000", ""},
		{"https://reg.example:5000", "reg.example:5000", "https"},
		{"http://reg.example:5000/", "reg.example:5000", "http"},
		{"localhost:30500", "localhost:30500", ""},
	}
	for _, tt := range tests {
		host, scheme := parseAdvertiseRegistry(tt.in)
		if host != tt.wantHost || scheme != tt.wantScheme {
			t.Errorf("parseAdvertiseRegistry(%q) = (%q,%q), want (%q,%q)", tt.in, host, scheme, tt.wantHost, tt.wantScheme)
		}
	}
}

func TestAdvertisedRegistryEnv(t *testing.T) {
	// No TLS configured -> a scheme-less env value defaults to http.
	s := &Server{cfg: config.Config{HTTPAddr: ":5000"}}
	t.Setenv("CORNUS_ADVERTISE_REGISTRY", "reg.example:5000")
	info := s.advertisedRegistry(context.Background())
	if info.RegistryHost != "reg.example:5000" || info.RegistryScheme != "http" {
		t.Fatalf("info = %+v", info)
	}

	// TLS configured -> https default.
	sTLS := &Server{cfg: config.Config{HTTPAddr: ":5000"}, TLSCertFile: "c", TLSKeyFile: "k"}
	if got := sTLS.advertisedRegistry(context.Background()); got.RegistryScheme != "https" {
		t.Fatalf("scheme = %q, want https", got.RegistryScheme)
	}

	// Explicit scheme in the value wins over the server default.
	t.Setenv("CORNUS_ADVERTISE_REGISTRY", "https://reg.example:5000")
	if got := s.advertisedRegistry(context.Background()); got.RegistryScheme != "https" {
		t.Fatalf("scheme = %q, want https", got.RegistryScheme)
	}
}

func TestLocalPushTarget(t *testing.T) {
	s := &Server{cfg: config.Config{HTTPAddr: ":5000"}}
	t.Setenv("CORNUS_ADVERTISE_REGISTRY", "localhost:30500")
	ctx := context.Background()

	tests := []struct {
		name   string
		target string
		push   bool
		want   string
	}{
		{"push false is untouched", "localhost:30500/demo-web:latest", false, "localhost:30500/demo-web:latest"},
		{"advertised host redirects to loopback", "localhost:30500/demo-web:latest", true, "127.0.0.1:5000/demo-web:latest"},
		{"nested repo path preserved", "localhost:30500/team/app:v2", true, "127.0.0.1:5000/team/app:v2"},
		{"external registry untouched", "ghcr.io/foo/bar:latest", true, "ghcr.io/foo/bar:latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.localPushTarget(ctx, tt.target, tt.push); got != tt.want {
				t.Fatalf("localPushTarget(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestLocalPushTargets(t *testing.T) {
	// Regression test: a compose build-group shares one build across several
	// services' tags (the group's first member is the Target, the rest are
	// additional Tags). Every one of them must be redirected the same way, not
	// just the Target — previously only Target was rewritten, so a build group
	// with more than one member had all but its first tag pushed at the
	// unreachable advertised host from inside the build pod.
	s := &Server{cfg: config.Config{HTTPAddr: ":5000"}}
	t.Setenv("CORNUS_ADVERTISE_REGISTRY", "localhost:30500")
	ctx := context.Background()

	target, tags := s.localPushTargets(ctx,
		"localhost:30500/kenall-moto:latest",
		[]string{
			"localhost:30500/kenall-init:latest",
			"localhost:30500/kenall-api:latest",
			"ghcr.io/foo/bar:latest",
		},
		true,
	)
	if want := "127.0.0.1:5000/kenall-moto:latest"; target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	wantTags := []string{
		"127.0.0.1:5000/kenall-init:latest",
		"127.0.0.1:5000/kenall-api:latest",
		"ghcr.io/foo/bar:latest",
	}
	if len(tags) != len(wantTags) {
		t.Fatalf("tags = %v, want %v", tags, wantTags)
	}
	for i, want := range wantTags {
		if tags[i] != want {
			t.Fatalf("tags[%d] = %q, want %q", i, tags[i], want)
		}
	}

	// push=false leaves everything untouched.
	target, tags = s.localPushTargets(ctx, "localhost:30500/kenall-moto:latest",
		[]string{"localhost:30500/kenall-init:latest"}, false)
	if target != "localhost:30500/kenall-moto:latest" || tags[0] != "localhost:30500/kenall-init:latest" {
		t.Fatalf("push=false was not a no-op: target=%q tags=%v", target, tags)
	}

	// No advertised host (single-node quick start) leaves everything untouched.
	sNoAdv := &Server{cfg: config.Config{HTTPAddr: ":5000"}}
	target, tags = sNoAdv.localPushTargets(ctx, "localhost:5000/demo-web:latest",
		[]string{"localhost:5000/demo-worker:latest"}, true)
	if target != "localhost:5000/demo-web:latest" || tags[0] != "localhost:5000/demo-worker:latest" {
		t.Fatalf("no-advertise was not a no-op: target=%q tags=%v", target, tags)
	}

	// A nil tags slice stays nil.
	_, nilTags := s.localPushTargets(ctx, "localhost:30500/kenall-moto:latest", nil, true)
	if nilTags != nil {
		t.Fatalf("nilTags = %v, want nil", nilTags)
	}
}

func TestLocalPushTargetNoAdvertiseIsUntouched(t *testing.T) {
	// No CORNUS_ADVERTISE_REGISTRY and a backend that does not advertise ->
	// advertisedRegistry is empty, so the single-node quick start's push (its ref
	// host doubles as the registry) is never redirected. CORNUS_DEPLOY_BACKEND is
	// set to kubernetes so the backend-introspection path actually runs (else the
	// non-k8s guard short-circuits before getBackend).
	t.Setenv("CORNUS_DEPLOY_BACKEND", "kubernetes")
	s := &Server{
		cfg:        config.Config{HTTPAddr: ":5000"},
		newBackend: func() (deploy.Backend, error) { return nil, errors.New("no backend") },
	}
	const ref = "localhost:5000/demo-web:latest"
	if got := s.localPushTarget(context.Background(), ref, true); got != ref {
		t.Fatalf("got %q, want unchanged %q", got, ref)
	}
}

func TestAdvertisedRegistryNonK8sBackendSkipsIntrospection(t *testing.T) {
	// On a non-kubernetes backend, advertisedRegistry must not construct a backend
	// just to find out it does not advertise: newBackend must never be called.
	t.Setenv("CORNUS_DEPLOY_BACKEND", "dockerhost")
	called := false
	s := &Server{
		cfg:        config.Config{HTTPAddr: ":5000"},
		newBackend: func() (deploy.Backend, error) { called = true; return nil, errors.New("boom") },
	}
	if got := s.advertisedRegistry(context.Background()); got != (api.ServerInfo{}) {
		t.Fatalf("info = %+v, want empty", got)
	}
	if called {
		t.Fatal("newBackend was constructed for a non-kubernetes backend")
	}
}

// healthyBackend is a fakeBackend that also implements deploy.HealthReporter, so
// advertisedBackend can be exercised on both sides of the capability.
type healthyBackend struct{ *fakeBackend }

func (healthyBackend) ReportsHealth() bool { return true }

// TestAdvertisedBackend proves GET /.cornus/v1/info reports the deploy backend's
// name and whether it reports container health, so a client can refuse a
// `depends_on: condition: service_healthy` it could never satisfy. A backend that
// cannot be constructed (or no factory at all) reports nothing, which the client
// reads as "unknown, assume capable".
func TestAdvertisedBackend(t *testing.T) {
	t.Run("no health capability", func(t *testing.T) {
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, newBackend: func() (deploy.Backend, error) { return &fakeBackend{}, nil }}
		got := s.advertisedBackend()
		if got == nil || got.Name != "fake" || got.ReportsHealth {
			t.Fatalf("advertisedBackend() = %+v, want name=fake reportsHealth=false", got)
		}
	})
	t.Run("health capability", func(t *testing.T) {
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, newBackend: func() (deploy.Backend, error) { return healthyBackend{&fakeBackend{}}, nil }}
		got := s.advertisedBackend()
		if got == nil || !got.ReportsHealth {
			t.Fatalf("advertisedBackend() = %+v, want reportsHealth=true", got)
		}
	})
	t.Run("backend unavailable", func(t *testing.T) {
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, newBackend: func() (deploy.Backend, error) { return nil, errors.New("no daemon") }}
		if got := s.advertisedBackend(); got != nil {
			t.Fatalf("advertisedBackend() = %+v, want nil", got)
		}
	})
	t.Run("no factory", func(t *testing.T) {
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}}
		if got := s.advertisedBackend(); got != nil {
			t.Fatalf("advertisedBackend() = %+v, want nil", got)
		}
	})

	// getBackend caches only on SUCCESS, so a failing factory would otherwise be
	// re-invoked — holding backendMu — on every /info call, and /info is on the
	// hot path for every CLI command's version-skew check. One attempt per
	// process, whether or not it succeeds.
	t.Run("failing backend constructed at most once", func(t *testing.T) {
		var calls int
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, newBackend: func() (deploy.Backend, error) {
			calls++
			return nil, errors.New("no daemon")
		}}
		for i := 0; i < 5; i++ {
			if got := s.advertisedBackend(); got != nil {
				t.Fatalf("advertisedBackend() = %+v, want nil", got)
			}
		}
		if calls != 1 {
			t.Fatalf("backend factory called %d times across 5 /info resolutions, want 1", calls)
		}
	})

	t.Run("succeeding backend constructed at most once", func(t *testing.T) {
		var calls int
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, newBackend: func() (deploy.Backend, error) {
			calls++
			return healthyBackend{&fakeBackend{}}, nil
		}}
		for i := 0; i < 5; i++ {
			if got := s.advertisedBackend(); got == nil || !got.ReportsHealth {
				t.Fatalf("advertisedBackend() = %+v, want reportsHealth=true", got)
			}
		}
		if calls != 1 {
			t.Fatalf("backend factory called %d times across 5 /info resolutions, want 1", calls)
		}
	})

	// Concurrent /info requests must not race on the memo.
	t.Run("concurrent resolution", func(t *testing.T) {
		s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, newBackend: func() (deploy.Backend, error) {
			return healthyBackend{&fakeBackend{}}, nil
		}}
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := s.advertisedBackend(); got == nil || !got.ReportsHealth {
					t.Errorf("advertisedBackend() = %+v, want reportsHealth=true", got)
				}
			}()
		}
		wg.Wait()
	})
}
