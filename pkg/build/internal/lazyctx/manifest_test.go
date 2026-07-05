package lazyctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func build(t *testing.T, dir string, ig Ignore) *Manifest {
	t.Helper()
	m, err := Build(dir, ig)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func TestManifestDeterministicAndOrdered(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b.txt", "b")
	writeFile(t, dir, "a/c.txt", "c")
	writeFile(t, dir, "a/b.txt", "ab")

	m1 := build(t, dir, nil)
	m2 := build(t, dir, nil)
	if m1.Digest() != m2.Digest() {
		t.Fatalf("digest not stable: %s vs %s", m1.Digest(), m2.Digest())
	}
	// Entries sorted by path.
	var paths []string
	for _, e := range m1.Entries {
		paths = append(paths, e.Path)
	}
	for i := 1; i < len(paths); i++ {
		if paths[i-1] >= paths[i] {
			t.Errorf("entries not sorted: %v", paths)
			break
		}
	}
}

func TestManifestSensitivity(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "data.bin", "hello")
	writeFile(t, dir, "keep.txt", "k")
	base := build(t, dir, nil).Digest()

	// Size change => different digest.
	if err := os.WriteFile(f, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if build(t, dir, nil).Digest() == base {
		t.Error("size change did not alter digest")
	}

	// mtime change => different digest.
	dir2 := t.TempDir()
	g := writeFile(t, dir2, "data.bin", "hello")
	d0 := build(t, dir2, nil).Digest()
	ts := time.Unix(1000000000, 0)
	if err := os.Chtimes(g, ts, ts); err != nil {
		t.Fatal(err)
	}
	if build(t, dir2, nil).Digest() == d0 {
		t.Error("mtime change did not alter digest")
	}

	// Adding a path => different digest.
	dir3 := t.TempDir()
	writeFile(t, dir3, "x", "1")
	da := build(t, dir3, nil).Digest()
	writeFile(t, dir3, "y", "2")
	if build(t, dir3, nil).Digest() == da {
		t.Error("added path did not alter digest")
	}
}

func TestManifestHonorsIgnore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.txt", "k")
	writeFile(t, dir, "skip.log", "noise")
	writeFile(t, dir, "vendor/lib.a", "v")

	ig := func(rel string) bool {
		return strings.HasSuffix(rel, ".log") || rel == "vendor" || strings.HasPrefix(rel, "vendor/")
	}
	m := build(t, dir, ig)
	for _, e := range m.Entries {
		if strings.HasSuffix(e.Path, ".log") || strings.HasPrefix(e.Path, "vendor") {
			t.Errorf("ignored path in manifest: %s", e.Path)
		}
	}
	// The ignored subtree changing must not change the digest.
	d0 := m.Digest()
	writeFile(t, dir, "vendor/another.a", "w")
	if build(t, dir, ig).Digest() != d0 {
		t.Error("change under ignored dir altered the digest")
	}
}

func TestManifestSymlinkRecordedByTarget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "inside.txt", "data")
	if err := os.Symlink("inside.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	m := build(t, dir, nil)
	var found bool
	for _, e := range m.Entries {
		if e.Path == "link" {
			found = true
			if e.Linkname != "inside.txt" {
				t.Errorf("symlink target = %q, want inside.txt", e.Linkname)
			}
		}
	}
	if !found {
		t.Error("symlink not recorded")
	}
	if m.Digest() == "" {
		t.Error("empty digest")
	}
}
