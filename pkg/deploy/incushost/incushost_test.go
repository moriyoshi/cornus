package incushost

import "testing"

// TestConfigResolveLayersExplicitConfigOverEnvOverDefaults pins the three-level
// precedence an operator relies on: an explicit Config field beats
// CORNUS_INCUS_SOCKET / CORNUS_INCUS_PROJECT, which beat the built-in defaults.
// Getting this backwards would silently point a deploy at the wrong daemon or
// the wrong Incus project.
func TestConfigResolveLayersExplicitConfigOverEnvOverDefaults(t *testing.T) {
	t.Run("defaults when nothing is set", func(t *testing.T) {
		t.Setenv("CORNUS_INCUS_SOCKET", "")
		t.Setenv("CORNUS_INCUS_PROJECT", "")
		got := Config{}.resolve()
		if got.Socket != DefaultSocket {
			t.Errorf("socket = %q, want %q", got.Socket, DefaultSocket)
		}
		if got.Project != DefaultProject {
			t.Errorf("project = %q, want %q", got.Project, DefaultProject)
		}
	})

	t.Run("environment fills empty fields", func(t *testing.T) {
		t.Setenv("CORNUS_INCUS_SOCKET", "/run/incus/unix.socket")
		t.Setenv("CORNUS_INCUS_PROJECT", "team")
		got := Config{}.resolve()
		if got.Socket != "/run/incus/unix.socket" || got.Project != "team" {
			t.Fatalf("resolve = %+v", got)
		}
	})

	t.Run("explicit config wins over the environment", func(t *testing.T) {
		t.Setenv("CORNUS_INCUS_SOCKET", "/run/incus/unix.socket")
		t.Setenv("CORNUS_INCUS_PROJECT", "team")
		got := Config{Socket: "/tmp/incus.sock", Project: "explicit", DataDir: "/data"}.resolve()
		if got.Socket != "/tmp/incus.sock" || got.Project != "explicit" {
			t.Fatalf("resolve = %+v", got)
		}
		if got.DataDir != "/data" {
			t.Errorf("resolve must not touch DataDir, got %q", got.DataDir)
		}
	})
}

// TestInstanceNameIsAPerReplicaDNSLabel pins the instance naming: Incus requires
// a DNS label, and every other path in this backend (ownership filtering,
// replica ordering, cp/exec targeting) assumes the cornus-<app>-<replica>
// shape.
func TestInstanceNameIsAPerReplicaDNSLabel(t *testing.T) {
	if got := instanceName("web", 0); got != "cornus-web-0" {
		t.Fatalf("instanceName = %q", got)
	}
	if got := instanceName("web", 11); got != "cornus-web-11" {
		t.Fatalf("instanceName = %q", got)
	}
}
