package dockerhost

// Flavor-to-engine wiring.
//
// The ordering inside New() is the load-bearing part. Options used to be applied
// AFTER the engine was constructed, which was fine while there was only one
// engine and impossible once WithFlavor decides which to build. A regression
// there would not fail to compile — it would silently produce a Docker engine
// for a podman backend, which then talks Docker's dialect to a libpod socket and
// 404s on every call.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestNewAppliesOptionsBeforeBuildingTheEngine is the ordering guard.
//
// It works by asking for the podman flavor with NO endpoint selector set: if the
// flavor reached engine construction, resolvePodmanAccess refuses and New fails
// with that refusal. If the ordering regressed, New would quietly build a Docker
// engine from DOCKER_HOST and SUCCEED — so the assertion is on the failure.
func TestNewAppliesOptionsBeforeBuildingTheEngine(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1") // a Docker engine would build fine from this
	t.Setenv(envPodmanSocket, "")
	t.Setenv(envPodmanService, "")

	_, err := New(WithFlavor(FlavorPodman))
	if err == nil {
		t.Fatal("New(WithFlavor(FlavorPodman)) succeeded with no podman endpoint configured; " +
			"the flavor never reached engine construction, so a Docker engine was built for a podman backend")
	}
	if !strings.Contains(err.Error(), envPodmanSocket) {
		t.Errorf("error should come from podman endpoint resolution, got: %v", err)
	}
}

// TestNewDockerFlavorIsUnchanged: the zero flavor must still build a Docker
// engine from DOCKER_HOST, since every pre-podman caller relies on it.
func TestNewDockerFlavorIsUnchanged(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	b, err := New()
	if err != nil {
		t.Fatalf("New() with no flavor: %v", err)
	}
	if b.Name() != "dockerhost" {
		t.Errorf("Name() = %q, want dockerhost", b.Name())
	}
	if _, ok := b.api.(*engineClient); !ok {
		t.Errorf("api is %T, want *engineClient for the default flavor", b.api)
	}
}

// TestWithEngineBypassesConstruction lets tests drive a fake runtime without an
// endpoint, and is also what proves the seam is really an interface.
func TestWithEngineBypassesConstruction(t *testing.T) {
	srv, _ := libpodPingServer(t, "5.8.2")
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	b, err := New(WithFlavor(FlavorPodman), WithEngine(e))
	if err != nil {
		t.Fatalf("New with an injected engine: %v", err)
	}
	if b.Name() != "podman" {
		t.Errorf("Name() = %q, want podman", b.Name())
	}
	if _, ok := b.api.(*podmanEngine); !ok {
		t.Errorf("api is %T, want *podmanEngine", b.api)
	}
}

// TestPodmanServiceReportsMissingBinary covers the spawn path's most likely
// real-world failure. podman is not installed on every host, and the message has
// to name both the variable that asked for this and the alternative.
func TestPodmanServiceReportsMissingBinary(t *testing.T) {
	if _, err := exec.LookPath("podman"); err == nil {
		t.Skip("podman is installed here; this test covers the absent case")
	}
	t.Setenv(envPodmanService, "1")
	t.Setenv(envPodmanSocket, "")

	_, err := New(WithFlavor(FlavorPodman))
	if err == nil {
		t.Fatal("New succeeded with CORNUS_PODMAN_SERVICE=1 and no podman binary")
	}
	for _, want := range []string{envPodmanService, envPodmanSocket, "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so the operator knows both what asked for this "+
				"and what to do instead; got: %v", want, err)
		}
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error should wrap exec.ErrNotFound so callers can distinguish "+
			"'not installed' from 'failed to start'; got: %v", err)
	}
}

// TestPodmanServiceSocketPathIsPerProcess: two cornus servers on one host must
// not fight over the same socket path.
func TestPodmanServiceSocketPathIsPerProcess(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	p, err := podmanServiceSocketPath()
	if err != nil {
		t.Fatalf("podmanServiceSocketPath: %v", err)
	}
	if !strings.Contains(p, "cornus-podman-") {
		t.Errorf("socket path %q does not identify cornus", p)
	}
	// The pid is what makes it per-process; a fixed name would collide.
	if strings.HasSuffix(p, "cornus-podman-.sock") {
		t.Errorf("socket path %q carries no pid, so two servers would collide", p)
	}
}

// TestCloseStopsNothingWhenNoServiceWasStarted: Close is called on every
// backend, and the overwhelming majority never spawn a service.
func TestCloseIsNilSafeWithoutAService(t *testing.T) {
	b := &Backend{}
	if err := b.Close(); err != nil {
		t.Errorf("Close on a backend that started no service: %v", err)
	}
}
