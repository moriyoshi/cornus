package clientconn

import (
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/clientconfig"
)

// writeConfig saves f to a temp file and returns a Resolver pointed at it.
func writeConfig(t *testing.T, f *clientconfig.File) *Resolver {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := clientconfig.Save(path, f); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return &Resolver{ConfigFile: path}
}

func TestResolveSSHMutualExclusion(t *testing.T) {
	r := writeConfig(t, &clientconfig.File{
		CurrentContext: "both",
		Contexts: map[string]*clientconfig.Context{
			"both": {
				PortForward: &clientconfig.PortForward{Namespace: "ns", Service: "svc"},
				SSHTunnel:   &clientconfig.SSHTunnel{Addr: "host:22"},
			},
		},
	})
	_, err := r.Resolve("")
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestResolveSSHExplicitServerWins(t *testing.T) {
	// An explicit Server makes the ssh-tunnel block inert with no dial and no error.
	r := writeConfig(t, &clientconfig.File{
		CurrentContext: "srv",
		Contexts: map[string]*clientconfig.Context{
			"srv": {
				Server:    "http://example:5000",
				SSHTunnel: &clientconfig.SSHTunnel{Addr: "unreachable:22"},
			},
		},
	})
	cn, err := r.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer cn.Cleanup()
	if cn.Endpoint != "http://example:5000" {
		t.Errorf("Endpoint = %q, want the explicit server", cn.Endpoint)
	}
	if cn.DialContext != nil {
		t.Error("DialContext should be nil when the ssh-tunnel block is inert")
	}
}

func TestResolveSSHFailFast(t *testing.T) {
	// An unreachable SSH endpoint surfaces its error from Resolve rather than
	// hanging (mirrors the pf-only fail-fast idiom).
	r := writeConfig(t, &clientconfig.File{
		CurrentContext: "ssh",
		Contexts: map[string]*clientconfig.Context{
			"ssh": {
				SSHTunnel: &clientconfig.SSHTunnel{
					Addr:        "127.0.0.1:1", // connection refused
					Insecure:    true,
					NoAgent:     true,
					NoSSHConfig: true,
				},
			},
		},
	})
	if _, err := r.Resolve(""); err == nil {
		t.Fatal("expected an error dialing an unreachable SSH endpoint")
	}
}
