//go:build linux

package builder

import (
	"testing"

	"github.com/tonistiigi/fsutil"
)

// TestFrontendAttrsTargetStage checks the SolveInput -> frontend-attrs mapping:
// build.target rides as FrontendAttrs["target"], build args become build-arg:
// entries, and named local mounts become context: entries while the reserved
// context/dockerfile mounts do not.
func TestFrontendAttrsTargetStage(t *testing.T) {
	attrs := frontendAttrs(SolveInput{
		Target:         "localhost:5000/app:v1",
		TargetStage:    "builder",
		DockerfileName: "Dockerfile",
		BuildArgs:      map[string]string{"K": "v"},
		// Values are nil-interface fsutil.FS: frontendAttrs only reads the keys.
		Mounts: map[string]fsutil.FS{"context": nil, "dockerfile": nil, "extra": nil},
	})
	if attrs["target"] != "builder" {
		t.Errorf(`attrs["target"] = %q want "builder"`, attrs["target"])
	}
	if attrs["filename"] != "Dockerfile" {
		t.Errorf(`attrs["filename"] = %q`, attrs["filename"])
	}
	if attrs["build-arg:K"] != "v" {
		t.Errorf(`attrs["build-arg:K"] = %q`, attrs["build-arg:K"])
	}
	if attrs["context:extra"] != "local:extra" {
		t.Errorf(`attrs["context:extra"] = %q want "local:extra"`, attrs["context:extra"])
	}
	if _, ok := attrs["context:context"]; ok {
		t.Error("reserved context mount leaked into a context: attr")
	}
	if _, ok := attrs["context:dockerfile"]; ok {
		t.Error("reserved dockerfile mount leaked into a context: attr")
	}
}

// TestFrontendAttrsNoTargetStage checks that an empty TargetStage sets no
// "target" attr (so the final stage builds, matching prior behavior).
func TestFrontendAttrsNoTargetStage(t *testing.T) {
	attrs := frontendAttrs(SolveInput{DockerfileName: "Dockerfile"})
	if _, ok := attrs["target"]; ok {
		t.Errorf(`empty TargetStage should not set attrs["target"], got %q`, attrs["target"])
	}
}

// TestFrontendAttrsBuildKeys checks the SolveInput -> frontend-attrs mapping for
// the extended compose build keys: pull, platforms, network (compose "default"
// -> buildkit "sandbox"), extra_hosts (host:ip -> host=ip csv), shm_size (bytes),
// and labels (label: prefix).
func TestFrontendAttrsBuildKeys(t *testing.T) {
	attrs := frontendAttrs(SolveInput{
		DockerfileName: "Dockerfile",
		Pull:           true,
		Platforms:      []string{"linux/amd64", "linux/arm64"},
		Network:        "default",
		ExtraHosts:     []string{"db:10.0.0.1"},
		ShmSize:        134217728,
		Labels:         map[string]string{"com.example.k": "v"},
	})
	if attrs["image-resolve-mode"] != "pull" {
		t.Errorf(`attrs["image-resolve-mode"] = %q want "pull"`, attrs["image-resolve-mode"])
	}
	if attrs["platform"] != "linux/amd64,linux/arm64" {
		t.Errorf(`attrs["platform"] = %q`, attrs["platform"])
	}
	if attrs["force-network-mode"] != "sandbox" {
		t.Errorf(`attrs["force-network-mode"] = %q want "sandbox"`, attrs["force-network-mode"])
	}
	if attrs["add-hosts"] != "db=10.0.0.1" {
		t.Errorf(`attrs["add-hosts"] = %q want "db=10.0.0.1"`, attrs["add-hosts"])
	}
	if attrs["shm-size"] != "134217728" {
		t.Errorf(`attrs["shm-size"] = %q`, attrs["shm-size"])
	}
	if attrs["label:com.example.k"] != "v" {
		t.Errorf(`attrs["label:com.example.k"] = %q`, attrs["label:com.example.k"])
	}
}

// TestFrontendAttrsNetworkHost checks that network "host"/"none" pass through
// verbatim (only compose's "default" is remapped to buildkit's "sandbox").
func TestFrontendAttrsNetworkHost(t *testing.T) {
	for _, nm := range []string{"host", "none"} {
		attrs := frontendAttrs(SolveInput{DockerfileName: "Dockerfile", Network: nm})
		if attrs["force-network-mode"] != nm {
			t.Errorf(`network %q -> attrs["force-network-mode"] = %q`, nm, attrs["force-network-mode"])
		}
	}
}

// TestCacheEntriesRegistryImport checks that a registry cache import (the shape
// a cache_from ref is folded into) maps 1:1 to a BuildKit CacheOptionsEntry.
func TestCacheEntriesRegistryImport(t *testing.T) {
	entries := cacheEntries([]CacheOption{{Type: "registry", Attrs: map[string]string{"ref": "reg/app:cache"}}})
	if len(entries) != 1 {
		t.Fatalf("entries = %+v want 1", entries)
	}
	if entries[0].Type != "registry" || entries[0].Attrs["ref"] != "reg/app:cache" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if cacheEntries(nil) != nil {
		t.Error("cacheEntries(nil) should be nil")
	}
}
