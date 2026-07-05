package dockerhost

// The property under test is a NEGATIVE one, and it is the whole point of the
// design: with a podman socket sitting at the stock path and neither selector
// set, startup must FAIL. If it connects anyway, ambient discovery has leaked
// back in — and a backend that finds a daemon on its own is one whose bug
// reports cannot answer "which daemon did it talk to?".

import (
	"strings"
	"testing"
)

// fakeEnv builds a getenv over a map, so resolution can be tested without
// touching process state.
func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestPodmanAccessRequiresAnExplicitChoice(t *testing.T) {
	_, err := resolvePodmanAccess(fakeEnv(nil))
	if err == nil {
		t.Fatal("resolvePodmanAccess with neither selector set succeeded; " +
			"want a startup failure rather than a probe of the stock socket paths")
	}
	// The message has to name both ways out, or the operator is told they are
	// wrong without being told what to do.
	for _, want := range []string{envPodmanSocket, envPodmanService, "podman.socket"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so it does not tell the operator how to proceed: %v", want, err)
		}
	}
}

// TestPodmanAccessDoesNotConsultAmbientVariables is the leak detector. Every
// variable here is one a "helpful" implementation would reach for.
func TestPodmanAccessDoesNotConsultAmbientVariables(t *testing.T) {
	for _, name := range []string{"CONTAINER_HOST", "DOCKER_HOST", "XDG_RUNTIME_DIR"} {
		t.Run(name, func(t *testing.T) {
			env := fakeEnv(map[string]string{name: "unix:///tmp/should-not-be-used.sock"})
			if _, err := resolvePodmanAccess(env); err == nil {
				t.Fatalf("resolvePodmanAccess succeeded with only %s set; "+
					"ambient discovery has leaked back in", name)
			}
		})
	}
}

func TestPodmanAccessSocket(t *testing.T) {
	for _, tc := range []struct {
		name, raw, wantPath string
	}{
		{"url form", "unix:///run/user/1000/podman/podman.sock", "/run/user/1000/podman/podman.sock"},
		{"bare path", "/run/podman/podman.sock", "/run/podman/podman.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePodmanAccess(fakeEnv(map[string]string{envPodmanSocket: tc.raw}))
			if err != nil {
				t.Fatalf("resolvePodmanAccess(%q): %v", tc.raw, err)
			}
			if got.Spawn {
				t.Error("Spawn = true, want false when a socket was named")
			}
			if p := got.Endpoint.UnixSocketPath(); p != tc.wantPath {
				t.Errorf("socket path = %q, want %q", p, tc.wantPath)
			}
		})
	}
}

func TestPodmanAccessTCPSocket(t *testing.T) {
	got, err := resolvePodmanAccess(fakeEnv(map[string]string{envPodmanSocket: "tcp://10.0.0.9:8080"}))
	if err != nil {
		t.Fatalf("resolvePodmanAccess: %v", err)
	}
	if !got.Endpoint.NonLocal() {
		t.Error("NonLocal() = false for a tcp podman endpoint, want true")
	}
}

func TestPodmanAccessService(t *testing.T) {
	got, err := resolvePodmanAccess(fakeEnv(map[string]string{envPodmanService: "1"}))
	if err != nil {
		t.Fatalf("resolvePodmanAccess: %v", err)
	}
	if !got.Spawn {
		t.Error("Spawn = false, want true when the service selector is set")
	}
}

// TestPodmanAccessRejectsBothSelectors: a precedence rule would silently ignore
// one of two things the operator explicitly asked for, and which one loses is
// exactly what nobody remembers during an incident.
func TestPodmanAccessRejectsBothSelectors(t *testing.T) {
	_, err := resolvePodmanAccess(fakeEnv(map[string]string{
		envPodmanSocket:  "/run/podman/podman.sock",
		envPodmanService: "1",
	}))
	if err == nil {
		t.Fatal("setting both selectors succeeded; want an error rather than a silent precedence")
	}
	if !strings.Contains(err.Error(), envPodmanSocket) || !strings.Contains(err.Error(), envPodmanService) {
		t.Errorf("error should name both selectors so the operator knows which to unset: %v", err)
	}
}

// TestPodmanAccessRoutesSSHToTheSSHPath: ssh:// is the spelling
// `podman system connection add` stores, so an operator copying their existing
// configuration lands here. It must be RECOGNIZED and deferred to the SSH
// resolver rather than parsed as a local socket.
//
// Resolution stops at recognition on purpose: opening the connection needs a
// context and does live I/O, neither of which belongs in a pure environment read.
func TestPodmanAccessRoutesSSHToTheSSHPath(t *testing.T) {
	const dest = "ssh://core@host:22/run/user/1000/podman/podman.sock"
	got, err := resolvePodmanAccess(fakeEnv(map[string]string{envPodmanSocket: dest}))
	if err != nil {
		t.Fatalf("resolvePodmanAccess(%q): %v", dest, err)
	}
	if got.SSH != dest {
		t.Errorf("SSH = %q, want the raw destination %q", got.SSH, dest)
	}
	if got.Spawn {
		t.Error("Spawn = true for an ssh:// destination")
	}
	// It must NOT have been parsed as a local unix socket, which would silently
	// dial a path on this machine instead of the remote one.
	if p := got.Endpoint.UnixSocketPath(); p != "" {
		t.Errorf("ssh:// destination was parsed as the local socket %q", p)
	}
}

func TestPodmanAccessRejectsUnsupportedScheme(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:8080",
		"npipe:////./pipe/podman", // a Windows spelling cornus does not support
		"podman.sock",             // bare relative name, not a path
	} {
		if _, err := resolvePodmanAccess(fakeEnv(map[string]string{envPodmanSocket: raw})); err == nil {
			t.Errorf("resolvePodmanAccess(%q) succeeded; want a refusal rather than a silent fallback", raw)
		}
	}
}

// TestPodmanSSHRequiresASocketPath: an ssh:// destination with no path names a
// host but not what to talk to on it.
func TestPodmanSSHRequiresASocketPath(t *testing.T) {
	for _, raw := range []string{"ssh://core@host", "ssh://core@host/"} {
		if _, err := podmanSSHEndpoint(t.Context(), raw); err == nil {
			t.Errorf("podmanSSHEndpoint(%q) succeeded; want an error naming the missing socket path", raw)
		}
	}
}
