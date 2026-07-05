package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveLocalCacheOpts_ExplicitKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localcache")
	in := []CacheOption{{Type: "local", Attrs: map[string]string{"dest": "myapp", "mode": "max"}}}

	out, err := resolveLocalCacheOpts(in, root, "localhost:5000/app:v1", "dest")
	if err != nil {
		t.Fatalf("resolveLocalCacheOpts: %v", err)
	}
	want := filepath.Join(root, "myapp")
	if got := out[0].Attrs["dest"]; got != want {
		t.Fatalf("dest = %q, want %q", got, want)
	}
	if out[0].Attrs["mode"] != "max" {
		t.Fatalf("mode attr not preserved: %v", out[0].Attrs)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("cache dir not created: %v", err)
	}
	// Input must not be mutated (Attrs is shared with the wire/request).
	if in[0].Attrs["dest"] != "myapp" {
		t.Fatalf("input Attrs mutated: %v", in[0].Attrs)
	}
}

func TestResolveLocalCacheOpts_DerivedKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localcache")
	cases := map[string]string{
		"localhost:5000/app:v1":        "app",
		"localhost:5000/team/app:v1":   "team/app",
		"registry.example.com/x/y:tag": "x/y",
	}
	for target, key := range cases {
		out, err := resolveLocalCacheOpts([]CacheOption{{Type: "local", Attrs: map[string]string{}}}, root, target, "src")
		if err != nil {
			t.Fatalf("target %q: %v", target, err)
		}
		want := filepath.Join(root, filepath.FromSlash(key))
		if got := out[0].Attrs["src"]; got != want {
			t.Fatalf("target %q: src = %q, want %q", target, got, want)
		}
	}
}

func TestResolveLocalCacheOpts_Confined(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localcache")
	for _, key := range []string{"../../etc", "/etc/passwd", "a/../../b", "../escape"} {
		out, err := resolveLocalCacheOpts([]CacheOption{{Type: "local", Attrs: map[string]string{"dest": key}}}, root, "localhost:5000/app:v1", "dest")
		if err != nil {
			// Rejecting outright is acceptable; if accepted it must stay under root.
			continue
		}
		got := out[0].Attrs["dest"]
		if got != root && !strings.HasPrefix(got, root+string(os.PathSeparator)) {
			t.Fatalf("key %q escaped root: got %q (root %q)", key, got, root)
		}
	}
}

func TestResolveLocalCacheOpts_PassThrough(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localcache")
	in := []CacheOption{
		{Type: "registry", Attrs: map[string]string{"ref": "localhost:5000/app:cache"}},
		{Type: "inline", Attrs: map[string]string{}},
	}
	out, err := resolveLocalCacheOpts(in, root, "localhost:5000/app:v1", "dest")
	if err != nil {
		t.Fatalf("resolveLocalCacheOpts: %v", err)
	}
	if out[0].Attrs["ref"] != "localhost:5000/app:cache" {
		t.Fatalf("registry ref altered: %v", out[0].Attrs)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cache root created for non-local options: err=%v", err)
	}
}

func TestResolveLocalCacheOpts_NoTargetNoKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localcache")
	_, err := resolveLocalCacheOpts([]CacheOption{{Type: "local", Attrs: map[string]string{}}}, root, "", "dest")
	if err == nil {
		t.Fatal("expected error when both key and target are absent")
	}
}

// writeCacheEntry creates a localcache key dir under root with one file, then
// backdates the whole subtree's mtimes to age.
func writeCacheEntry(t *testing.T, root, key string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(localCacheRoot(root), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "index.json")
	if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	for _, p := range []string{f, dir} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPruneLocalCache_RemovesStaleKeepsFresh proves the prune removes a key dir
// whose newest mtime predates the TTL and leaves a freshly-modified one.
func TestPruneLocalCache_RemovesStaleKeepsFresh(t *testing.T) {
	root := t.TempDir()
	stale := writeCacheEntry(t, root, "stale-app", 48*time.Hour)
	fresh := writeCacheEntry(t, root, "fresh-app", 0)

	freed, err := PruneLocalCache(root, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneLocalCache: %v", err)
	}
	if freed != 1 {
		t.Fatalf("freed = %d, want 1", freed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale entry should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh entry should survive: %v", err)
	}
}

// TestPruneLocalCache_KeepsFreshSiblingUnderSharedPrefix proves a stale key that
// shares a top-level prefix with a fresh one is RETAINED (never over-deletes a
// fresh cache), since pruning keys on the subtree's newest mtime.
func TestPruneLocalCache_KeepsFreshSiblingUnderSharedPrefix(t *testing.T) {
	root := t.TempDir()
	writeCacheEntry(t, root, filepath.Join("team", "stale"), 48*time.Hour)
	writeCacheEntry(t, root, filepath.Join("team", "fresh"), 0)

	freed, err := PruneLocalCache(root, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneLocalCache: %v", err)
	}
	if freed != 0 {
		t.Fatalf("freed = %d, want 0 (fresh sibling keeps the group)", freed)
	}
	if _, err := os.Stat(filepath.Join(localCacheRoot(root), "team", "fresh")); err != nil {
		t.Fatalf("fresh nested entry should survive: %v", err)
	}
}

// TestPruneLocalCache_MissingDirIsNoOp proves a missing localcache dir is not an
// error.
func TestPruneLocalCache_MissingDirIsNoOp(t *testing.T) {
	freed, err := PruneLocalCache(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("PruneLocalCache on missing dir: %v", err)
	}
	if freed != 0 {
		t.Fatalf("freed = %d, want 0", freed)
	}
}

// TestPruneLocalCache_RejectsNonPositiveTTL guards against a zero/negative TTL
// silently nuking every entry.
func TestPruneLocalCache_RejectsNonPositiveTTL(t *testing.T) {
	root := t.TempDir()
	writeCacheEntry(t, root, "app", time.Hour)
	if _, err := PruneLocalCache(root, 0); err == nil {
		t.Fatal("expected error for non-positive olderThan")
	}
}
