//go:build linux

package hostrun

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetnsAlive(t *testing.T) {
	if NetnsAlive(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("missing path must not be alive")
	}
	// A leftover empty regular file (the pin after its mount went away).
	stale := filepath.Join(t.TempDir(), "stale")
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if NetnsAlive(stale) {
		t.Fatal("plain file must not count as a live netns")
	}
	// The process's own net namespace is nsfs-backed.
	if !NetnsAlive("/proc/self/ns/net") {
		t.Fatal("/proc/self/ns/net should be a live netns")
	}
}
