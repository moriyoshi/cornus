package server

import (
	"testing"

	"cornus/pkg/config"
)

// TestLocalRegistryRef checks the skip-pull predicate: bare and loopback-host
// refs are cornus's own registry (local); external hosts are not.
func TestLocalRegistryRef(t *testing.T) {
	local := []string{
		"app:v1",                // bare
		"team/app:v1",           // bare with path
		"127.0.0.1:5000/app:v1", // loopback host
		"localhost:5000/app:v1", // loopback name
		"127.0.0.1/app",         // loopback, no port
	}
	external := []string{
		"docker.io/library/nginx:latest",
		"ghcr.io/moriyoshi/cornus:latest",
		"reg.example.com:5000/app:v1",
	}
	for _, ref := range local {
		if !localRegistryRef(ref) {
			t.Errorf("localRegistryRef(%q) = false, want true", ref)
		}
	}
	for _, ref := range external {
		if localRegistryRef(ref) {
			t.Errorf("localRegistryRef(%q) = true, want false", ref)
		}
	}
}

// TestResolveRegistrySource covers CORNUS_REGISTRY_SOURCE resolution: the
// host-native token (explicit or the default on a host backend), off, the pure
// vs union store decision (--storage), backend resolution, and mirror
// exclusivity — all fail-closed.
func TestResolveRegistrySource(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		backend    string
		storage    string // CORNUS_STORAGE / --storage
		mirror     string
		wantSource string
		wantPure   bool
		wantErr    bool
	}{
		// Default: zero-config host backend re-exports its local store (pure).
		{name: "default dockerhost (unset backend)", wantSource: "docker-daemon", wantPure: true},
		{name: "default dockerhost explicit", backend: "dockerhost", wantSource: "docker-daemon", wantPure: true},
		{name: "default containerd", backend: "containerd", wantSource: "containerd", wantPure: true},
		// Non-host backends keep the classic CAS by default.
		{name: "default bare = classic", backend: "bare", wantSource: "", wantPure: false},
		{name: "default kubernetes = classic", backend: "kubernetes", wantSource: "", wantPure: false},
		// Explicit --storage opts out of the default (classic CAS), even on dockerhost.
		{name: "default + explicit storage = classic", storage: "/data", wantSource: "", wantPure: false},
		// A configured mirror opts out of the default.
		{name: "default + mirror = classic", mirror: "docker.io", wantSource: "", wantPure: false},
		// Explicit host-native.
		{name: "host-native dockerhost", source: "host-native", wantSource: "docker-daemon", wantPure: true},
		{name: "host-native containerd", source: "host-native", backend: "containerd", wantSource: "containerd", wantPure: true},
		// host-native + explicit --storage = union (source kept, store kept).
		{name: "host-native union", source: "host-native", storage: "/data", wantSource: "docker-daemon", wantPure: false},
		// host-native rejects non-host backends and a mirror combo.
		{name: "host-native rejects bare", source: "host-native", backend: "bare", wantErr: true},
		{name: "host-native rejects kubernetes", source: "host-native", backend: "kubernetes", wantErr: true},
		{name: "host-native rejects mirror combo", source: "host-native", mirror: "docker.io", wantErr: true},
		// off forces the classic CAS even on a host backend.
		{name: "off on dockerhost = classic", source: "off", wantSource: "", wantPure: false},
		{name: "off on containerd = classic", source: "off", backend: "containerd", wantSource: "", wantPure: false},
		// Unknown value is rejected.
		{name: "unknown value", source: "docker-daemon", wantErr: true},
		{name: "unknown value 2", source: "podman", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CORNUS_REGISTRY_SOURCE", tc.source)
			t.Setenv("CORNUS_DEPLOY_BACKEND", tc.backend)
			t.Setenv("CORNUS_REGISTRY_MIRROR", tc.mirror)
			plan, err := resolveRegistrySource(config.Config{StorageURL: tc.storage})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveRegistrySource() = %+v, nil; want error", plan)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRegistrySource() unexpected error: %v", err)
			}
			if plan.source != tc.wantSource || plan.pure != tc.wantPure {
				t.Fatalf("resolveRegistrySource() = {source:%q pure:%v}, want {source:%q pure:%v}",
					plan.source, plan.pure, tc.wantSource, tc.wantPure)
			}
		})
	}
}
