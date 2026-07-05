package dockerhost

// Two properties, and the tension between them is the point.
//
//   - Endpoint RESOLUTION is eager: a missing selector is a static
//     misconfiguration that can never fix itself, so it is a startup error.
//   - Reaching the DAEMON is lazy: a stopped podman is a runtime condition that
//     may resolve, and the Docker path builds its client without any I/O. An
//     eager engine here made `cornus serve` fail at boot with "cannot reach the
//     libpod API" — found by running the binary, not by the test suite.

import (
	"context"
	"strings"
	"testing"
)

// TestPodmanImageAPIDoesNotDialAtConstruction is the regression guard. The
// socket does not exist; constructing must still succeed.
func TestPodmanImageAPIDoesNotDialAtConstruction(t *testing.T) {
	t.Setenv(envPodmanSocket, "/definitely/not/here/podman.sock")
	t.Setenv(envPodmanService, "")

	api, err := NewPodmanImageAPI()
	if err != nil {
		t.Fatalf("NewPodmanImageAPI dialed at construction: %v\n"+
			"a stopped podman must not stop the server from starting — the Docker path "+
			"builds its client with no I/O and this must match", err)
	}
	if api == nil {
		t.Fatal("NewPodmanImageAPI returned nil with no error")
	}
	// ...and the failure must surface when the image is actually asked for.
	if _, err := api.ImageSave(context.Background(), "alpine:latest"); err == nil {
		t.Error("ImageSave against a nonexistent socket succeeded")
	}
}

// TestPodmanImageAPIResolutionIsEager: the static half still fails fast.
func TestPodmanImageAPIRequiresAnEndpoint(t *testing.T) {
	t.Setenv(envPodmanSocket, "")
	t.Setenv(envPodmanService, "")
	if _, err := NewPodmanImageAPI(); err == nil {
		t.Error("NewPodmanImageAPI succeeded with no endpoint configured; " +
			"a missing selector can never resolve on its own and must be reported at startup")
	}
}

// TestPodmanImageAPIRejectsTheSpawnedService: that service belongs to a Backend
// which started it. Starting a second one for the registry would orphan it.
func TestPodmanImageAPIRejectsTheSpawnedService(t *testing.T) {
	t.Setenv(envPodmanSocket, "")
	t.Setenv(envPodmanService, "1")
	_, err := NewPodmanImageAPI()
	if err == nil {
		t.Fatal("NewPodmanImageAPI accepted CORNUS_PODMAN_SERVICE=1; the registry cannot share a per-Backend service")
	}
	if !strings.Contains(err.Error(), envPodmanSocket) {
		t.Errorf("error should point at the selector that DOES work here: %v", err)
	}
}
