package e2e

import (
	"strings"
	"testing"
)

// envMap turns a KEY=VALUE slice into a map for assertion convenience.
func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		m[k] = v
	}
	return m
}

func TestContainerdTargetServeEnv(t *testing.T) {
	// Neutralize ambient env so the defaults are what we assert on.
	t.Setenv("CORNUS_CONTAINERD_ADDRESS", "")
	t.Setenv("CORNUS_CONTAINERD_NAMESPACE", "")
	t.Setenv("CORNUS_CNI_BIN_DIR", "")
	t.Setenv("CNI_PATH", "")

	tgt := &ContainerdTarget{Namespace: "cornus-e2e"}
	if tgt.Name() != "containerd" {
		t.Fatalf("Name() = %q", tgt.Name())
	}
	m := envMap(t, tgt.ServeEnv())
	want := map[string]string{
		"CORNUS_DEPLOY_BACKEND":       "containerd",
		"CORNUS_CONTAINERD_ADDRESS":   "/run/containerd/containerd.sock",
		"CORNUS_CONTAINERD_NAMESPACE": "cornus-e2e",
		"CORNUS_BUILD_WORKER":         "containerd",
		"CORNUS_ALLOW_BIND_SOURCES":   "/",
		"CORNUS_REGISTRY_SOURCE":      "off",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("ServeEnv()[%s] = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["CORNUS_CNI_BIN_DIR"]; ok {
		t.Error("ServeEnv() should not pass CORNUS_CNI_BIN_DIR through when unset")
	}

	// Explicit fields and CNI passthrough win over defaults.
	t.Setenv("CORNUS_CNI_BIN_DIR", "/custom/cni")
	tgt = &ContainerdTarget{Address: "/tmp/ctd.sock", Namespace: "myns"}
	m = envMap(t, tgt.ServeEnv())
	if m["CORNUS_CONTAINERD_ADDRESS"] != "/tmp/ctd.sock" || m["CORNUS_CONTAINERD_NAMESPACE"] != "myns" {
		t.Errorf("explicit address/namespace not honored: %v", m)
	}
	if m["CORNUS_CNI_BIN_DIR"] != "/custom/cni" {
		t.Errorf("CORNUS_CNI_BIN_DIR passthrough = %q, want /custom/cni", m["CORNUS_CNI_BIN_DIR"])
	}
}

func TestContainerdTargetEnvDefaults(t *testing.T) {
	t.Setenv("CORNUS_CONTAINERD_ADDRESS", "/env/ctd.sock")
	t.Setenv("CORNUS_CONTAINERD_NAMESPACE", "envns")
	tgt := &ContainerdTarget{}
	if got := tgt.addr(); got != "/env/ctd.sock" {
		t.Errorf("addr() = %q, want the env value", got)
	}
	if got := tgt.ns(); got != "envns" {
		t.Errorf("ns() = %q, want the env value", got)
	}

	t.Setenv("CORNUS_CONTAINERD_ADDRESS", "")
	t.Setenv("CORNUS_CONTAINERD_NAMESPACE", "")
	if got := tgt.addr(); got != "/run/containerd/containerd.sock" {
		t.Errorf("addr() default = %q", got)
	}
	if got := tgt.ns(); got != "cornus" {
		t.Errorf("ns() default = %q", got)
	}
}

func TestBareTargetServeEnv(t *testing.T) {
	// Neutralize ambient env so the defaults are what we assert on.
	t.Setenv("CORNUS_BARE_RUNTIME", "")
	t.Setenv("CORNUS_BARE_SNAPSHOTTER", "")
	t.Setenv("CORNUS_CNI_BIN_DIR", "")
	t.Setenv("CNI_PATH", "")

	tgt := &BareTarget{}
	if tgt.Name() != "bare" {
		t.Fatalf("Name() = %q", tgt.Name())
	}
	if got := tgt.runtimeBin(); got != "runc" {
		t.Errorf("runtimeBin() default = %q, want runc", got)
	}
	m := envMap(t, tgt.ServeEnv())
	want := map[string]string{
		"CORNUS_DEPLOY_BACKEND":     "bare",
		"CORNUS_BARE_RUNTIME":       "runc",
		"CORNUS_ALLOW_BIND_SOURCES": "/",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("ServeEnv()[%s] = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["CORNUS_BARE_SNAPSHOTTER"]; ok {
		t.Error("ServeEnv() should not set CORNUS_BARE_SNAPSHOTTER when unset")
	}
	if _, ok := m["CORNUS_CNI_BIN_DIR"]; ok {
		t.Error("ServeEnv() should not pass CORNUS_CNI_BIN_DIR through when unset")
	}

	// Explicit runtime/snapshotter fields and CNI passthrough win.
	t.Setenv("CORNUS_CNI_BIN_DIR", "/custom/cni")
	tgt = &BareTarget{Runtime: "crun", Snapshotter: "native"}
	m = envMap(t, tgt.ServeEnv())
	if m["CORNUS_BARE_RUNTIME"] != "crun" || m["CORNUS_BARE_SNAPSHOTTER"] != "native" {
		t.Errorf("explicit runtime/snapshotter not honored: %v", m)
	}
	if m["CORNUS_CNI_BIN_DIR"] != "/custom/cni" {
		t.Errorf("CORNUS_CNI_BIN_DIR passthrough = %q, want /custom/cni", m["CORNUS_CNI_BIN_DIR"])
	}
}

func TestBareTargetRuntimeFromEnv(t *testing.T) {
	t.Setenv("CORNUS_BARE_RUNTIME", "youki")
	if got := (&BareTarget{}).runtimeBin(); got != "youki" {
		t.Errorf("runtimeBin() = %q, want youki (from env)", got)
	}
	// An explicit field wins over the env.
	if got := (&BareTarget{Runtime: "crun"}).runtimeBin(); got != "crun" {
		t.Errorf("runtimeBin() = %q, want crun (explicit field wins)", got)
	}
}

func TestCNIPluginDirs(t *testing.T) {
	t.Setenv("CORNUS_CNI_BIN_DIR", "")
	t.Setenv("CNI_PATH", "")
	if got := cniPluginDirs(); len(got) != 1 || got[0] != "/opt/cni/bin" {
		t.Errorf("default dirs = %v", got)
	}
	t.Setenv("CNI_PATH", "/a:/b")
	if got := cniPluginDirs(); len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("CNI_PATH dirs = %v", got)
	}
	t.Setenv("CORNUS_CNI_BIN_DIR", "/custom")
	if got := cniPluginDirs(); len(got) != 1 || got[0] != "/custom" {
		t.Errorf("CORNUS_CNI_BIN_DIR dirs = %v", got)
	}
}
