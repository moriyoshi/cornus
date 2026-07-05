package server

// The podman backend is the only one with NO default endpoint, which makes its
// misconfiguration a different KIND of problem from the others'.
//
// Backend construction is lazy on purpose — "so the server can start even when
// no Docker host is reachable" — and that rationale is right for a daemon that
// is merely stopped. It is wrong for a configuration that can never work: Docker
// falls back to /var/run/docker.sock and containerd/incus to their stock
// sockets, but podman is told explicitly or not at all.
//
// Without the eager check these tests guard, `CORNUS_DEPLOY_BACKEND=podman` with
// no selector produced a server that started cleanly, logged nothing about it,
// and failed at the operator's first deploy. Verified by running it.

import (
	"strings"
	"testing"

	"cornus/pkg/config"
)

func TestValidateDeployBackendRejectsPodmanWithNoEndpoint(t *testing.T) {
	t.Setenv("CORNUS_PODMAN_SOCKET", "")
	t.Setenv("CORNUS_PODMAN_SERVICE", "")

	err := podmanEndpointErr(t)
	if err == nil {
		t.Fatal("validateDeployBackend(\"podman\") accepted a server with no podman endpoint configured; " +
			"the server would start and fail at the first deploy instead")
	}
	// The message must carry the way out, or it only tells the operator they lost.
	for _, want := range []string{"CORNUS_PODMAN_SOCKET", "CORNUS_PODMAN_SERVICE", "podman.socket"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q; got: %v", want, err)
		}
	}
}

// TestValidateDeployBackendAcceptsPodmanWithASocket: the check is STATIC. An
// endpoint that is named but unreachable must still start, because that is the
// case lazy construction exists for — the daemon may simply be down.
func TestValidateDeployBackendAcceptsPodmanWithASocket(t *testing.T) {
	t.Setenv("CORNUS_PODMAN_SOCKET", "/definitely/not/here/podman.sock")
	t.Setenv("CORNUS_PODMAN_SERVICE", "")

	if err := podmanEndpointErr(t); err != nil {
		t.Errorf("validateDeployBackend rejected a NAMED but unreachable socket: %v\n"+
			"the check must not reach the network — a stopped daemon should not stop the server "+
			"from starting and serving its registry", err)
	}
}

// The other backends must be unaffected: none of them gained a startup
// requirement, and a podman-shaped check leaking onto them would break servers
// that never mention podman.
func TestValidateDeployBackendUnaffectedForOtherBackends(t *testing.T) {
	t.Setenv("CORNUS_PODMAN_SOCKET", "")
	t.Setenv("CORNUS_PODMAN_SERVICE", "")
	for _, b := range []string{"", "dockerhost", "containerd", "bare", "incus", "kubernetes", "k8s"} {
		if err := resolveBackendErr(t, b); err != nil {
			t.Errorf("validateDeployBackend(%q) = %v, want nil (only podman needs an explicit endpoint)", b, err)
		}
	}
}

// podmanEndpointErr runs the startup resolution for the podman backend and
// returns whatever it refused with.
func podmanEndpointErr(t *testing.T) error {
	t.Helper()
	t.Setenv("CORNUS_DEPLOY_BACKEND", "podman")
	_, err := resolveRegistrySource(config.Config{})
	return err
}

// resolveBackendErr runs the same startup resolution for any backend.
func resolveBackendErr(t *testing.T, backend string) error {
	t.Helper()
	t.Setenv("CORNUS_DEPLOY_BACKEND", backend)
	_, err := resolveRegistrySource(config.Config{})
	return err
}
