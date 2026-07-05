package builder

import (
	"strings"
	"testing"
)

// clearWorkerEnv pins every worker-selection environment knob to empty (an
// empty value counts as unset in resolveWorkerConfig) so ambient host settings
// cannot leak into the table cases.
func clearWorkerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		buildWorkerEnv,
		containerdAddressEnv,
		containerdAddressStdEnv,
		containerdNamespaceEnv,
		containerdSnapshotterEnv,
	} {
		t.Setenv(k, "")
	}
}

func TestResolveWorkerConfigDefaults(t *testing.T) {
	clearWorkerEnv(t)
	cfg, err := resolveWorkerConfig(Config{Root: "/data"})
	if err != nil {
		t.Fatalf("resolveWorkerConfig: %v", err)
	}
	if cfg.Worker != WorkerRunc {
		t.Errorf("Worker = %q, want %q", cfg.Worker, WorkerRunc)
	}
	if cfg.Containerd.Address != defaultContainerdAddress {
		t.Errorf("Address = %q, want %q", cfg.Containerd.Address, defaultContainerdAddress)
	}
	if cfg.Containerd.Namespace != defaultContainerdNamespace {
		t.Errorf("Namespace = %q, want %q", cfg.Containerd.Namespace, defaultContainerdNamespace)
	}
	if cfg.Containerd.Snapshotter != defaultContainerdSnapshotter {
		t.Errorf("Snapshotter = %q, want %q", cfg.Containerd.Snapshotter, defaultContainerdSnapshotter)
	}
	if cfg.Root != "/data" {
		t.Errorf("Root = %q, want unchanged", cfg.Root)
	}
}

func TestResolveWorkerConfigFromEnv(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv(buildWorkerEnv, WorkerContainerd)
	t.Setenv(containerdAddressEnv, "/run/custom/ctd.sock")
	t.Setenv(containerdNamespaceEnv, "myns")
	t.Setenv(containerdSnapshotterEnv, "native")
	cfg, err := resolveWorkerConfig(Config{Root: "/data"})
	if err != nil {
		t.Fatalf("resolveWorkerConfig: %v", err)
	}
	if cfg.Worker != WorkerContainerd {
		t.Errorf("Worker = %q, want %q", cfg.Worker, WorkerContainerd)
	}
	if cfg.Containerd.Address != "/run/custom/ctd.sock" {
		t.Errorf("Address = %q, want env value", cfg.Containerd.Address)
	}
	if cfg.Containerd.Namespace != "myns" {
		t.Errorf("Namespace = %q, want env value", cfg.Containerd.Namespace)
	}
	if cfg.Containerd.Snapshotter != "native" {
		t.Errorf("Snapshotter = %q, want env value", cfg.Containerd.Snapshotter)
	}
}

func TestResolveWorkerConfigStandardAddressFallback(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv(containerdAddressStdEnv, "/run/std/ctd.sock")
	cfg, err := resolveWorkerConfig(Config{Root: "/data"})
	if err != nil {
		t.Fatalf("resolveWorkerConfig: %v", err)
	}
	if cfg.Containerd.Address != "/run/std/ctd.sock" {
		t.Errorf("Address = %q, want CONTAINERD_ADDRESS fallback", cfg.Containerd.Address)
	}

	// The CORNUS_* variable wins over the standard fallback.
	t.Setenv(containerdAddressEnv, "/run/cornus/ctd.sock")
	cfg, err = resolveWorkerConfig(Config{Root: "/data"})
	if err != nil {
		t.Fatalf("resolveWorkerConfig: %v", err)
	}
	if cfg.Containerd.Address != "/run/cornus/ctd.sock" {
		t.Errorf("Address = %q, want CORNUS_CONTAINERD_ADDRESS to win", cfg.Containerd.Address)
	}
}

func TestResolveWorkerConfigExplicitConfigWinsOverEnv(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv(buildWorkerEnv, WorkerContainerd)
	t.Setenv(containerdAddressEnv, "/run/env/ctd.sock")
	cfg, err := resolveWorkerConfig(Config{
		Root:       "/data",
		Worker:     WorkerRunc,
		Containerd: ContainerdConfig{Address: "/run/explicit/ctd.sock"},
	})
	if err != nil {
		t.Fatalf("resolveWorkerConfig: %v", err)
	}
	if cfg.Worker != WorkerRunc {
		t.Errorf("Worker = %q, want explicit Config value to win", cfg.Worker)
	}
	if cfg.Containerd.Address != "/run/explicit/ctd.sock" {
		t.Errorf("Address = %q, want explicit Config value to win", cfg.Containerd.Address)
	}
}

func TestResolveWorkerConfigInvalidWorker(t *testing.T) {
	clearWorkerEnv(t)
	for _, src := range []struct {
		name string
		cfg  Config
		env  string
	}{
		{name: "config", cfg: Config{Root: "/data", Worker: "podman"}},
		{name: "env", cfg: Config{Root: "/data"}, env: "podman"},
	} {
		t.Run(src.name, func(t *testing.T) {
			if src.env != "" {
				t.Setenv(buildWorkerEnv, src.env)
			}
			_, err := resolveWorkerConfig(src.cfg)
			if err == nil {
				t.Fatal("expected an error for an unknown worker")
			}
			if !strings.Contains(err.Error(), "podman") || !strings.Contains(err.Error(), buildWorkerEnv) {
				t.Errorf("error %q should name the bad worker and the env knob", err)
			}
		})
	}
}
