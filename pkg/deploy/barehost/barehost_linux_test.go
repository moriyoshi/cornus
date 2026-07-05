//go:build linux

package barehost

import (
	"os/exec"
	"strings"
	"testing"

	"cornus/pkg/deploy"
)

// TestNewConstructsRealBackend exercises the real New() path — building the
// in-process content store + snapshotter and the runtime driver — without root
// (the snapshotter selection falls back to native when overlay is unsupported).
// It needs a runc binary on PATH; skips otherwise.
func TestNewConstructsRealBackend(t *testing.T) {
	if _, err := exec.LookPath("runc"); err != nil {
		t.Skip("runc not installed; skipping real-backend construction test")
	}
	b, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()
	if b.Name() != "bare" {
		t.Errorf("Name() = %q, want bare", b.Name())
	}
}

func TestConfigResolveDefaults(t *testing.T) {
	t.Setenv("CORNUS_BARE_RUNTIME", "")
	t.Setenv("CORNUS_BARE_SNAPSHOTTER", "")
	got, err := Config{DataDir: "/data"}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Runtime != DefaultRuntime {
		t.Errorf("Runtime = %q, want default %q", got.Runtime, DefaultRuntime)
	}
	if got.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", got.DataDir)
	}
}

func TestConfigResolveEnvOverrides(t *testing.T) {
	t.Setenv("CORNUS_BARE_RUNTIME", "crun")
	t.Setenv("CORNUS_BARE_SNAPSHOTTER", "native")
	got, err := Config{DataDir: "/data"}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Runtime != "crun" {
		t.Errorf("Runtime = %q, want crun", got.Runtime)
	}
	if got.Snapshotter != "native" {
		t.Errorf("Snapshotter = %q, want native", got.Snapshotter)
	}
}

func TestConfigResolveRequiresDataDir(t *testing.T) {
	if _, err := (Config{}).resolve(); err == nil {
		t.Fatal("resolve with empty DataDir: want error, got nil")
	}
}

func TestNewRejectsMissingRuntime(t *testing.T) {
	_, err := New(Config{DataDir: "/data", Runtime: "definitely-not-a-real-runtime-binary"})
	if err == nil {
		t.Fatal("New with a missing runtime binary: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("error = %q, want it to mention the missing binary", err)
	}
}

func TestBackendIdentity(t *testing.T) {
	// newBackend skips the PATH probe so the identity checks need no real runtime.
	b := newBackend(Config{DataDir: "/data", Runtime: "runc"}, newFakeRuntime(), nil, false, WithRemote(true))
	if b.Name() != "bare" {
		t.Errorf("Name() = %q, want bare", b.Name())
	}
	if !b.Remote() {
		t.Errorf("Remote() = false, want true after WithRemote(true)")
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestInstanceName(t *testing.T) {
	if got := instanceName("web", 0); got != "cornus-web-0" {
		t.Errorf("instanceName = %q, want cornus-web-0", got)
	}
}

// Compile-time proof the skeleton satisfies the interfaces it must; the var in
// backend_linux.go covers deploy.Backend, this covers the optional ones the
// full-parity scope commits to.
var (
	_ deploy.Backend       = (*Backend)(nil)
	_ deploy.RemoteCapable = (*Backend)(nil)
)
