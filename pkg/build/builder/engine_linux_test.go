//go:build linux

package builder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/moby/buildkit/solver/bboltcachestorage"

	"cornus/pkg/registry"
	"cornus/pkg/storage"
)

// TestCacheStoreCloseReleasesLock pins the exact hazard behind the "bbolt stores
// never closed" leak: bboltcachestorage.NewStore opens cache.db with Timeout 0,
// so while one handle stays open a second NewStore on the same path blocks
// forever on the file lock. A construction that opens the store and then fails
// without closing it therefore deadlocks the next engine build. This test
// reproduces that (the second open blocks) and proves that closing the first
// store — what Engine.Close and the newController error paths now do — releases
// the lock and unblocks it. Pure unit test: no root, no daemon, no network. The
// full New()/Engine.Close path needs a runc worker (root) and is covered by the
// root-gated tests below.
func TestCacheStoreCloseReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")

	s1, err := bboltcachestorage.NewStore(path)
	if err != nil {
		t.Fatalf("first NewStore: %v", err)
	}

	opened := make(chan error, 1)
	go func() {
		s2, err := bboltcachestorage.NewStore(path)
		if err == nil {
			_ = s2.Close()
		}
		opened <- err
	}()

	select {
	case <-opened:
		t.Fatal("second NewStore returned while the first was still open; expected it to block on the file lock")
	case <-time.After(500 * time.Millisecond):
		// Still blocked, as expected: this is the deadlock a leaked handle causes.
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("second NewStore after close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second NewStore still blocked after the first was closed; lock not released")
	}
}

// TestBuildAndPush exercises the full in-process build engine: it builds a
// hermetic "FROM scratch" image (no network) and pushes it to an in-process
// cornus registry, then pulls the manifest back.
//
// It requires privileges the runc executor/snapshotter need (root, or a
// rootless user-namespace stack), so it is skipped on unprivileged hosts. It is
// the real end-to-end check that runs inside the packaged container (M6).
func TestBuildAndPush(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("build execution requires root or a rootless userns stack; skipping on unprivileged host")
	}

	// In-process cornus registry as the push target.
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	mux := http.NewServeMux()
	registry.New(st).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// Hermetic build context.
	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	df := "FROM scratch\nCOPY hello.txt /hello.txt\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close()

	target := host + "/demo:v1"
	res, err := eng.Build(context.Background(), Request{
		ContextDir: ctxDir,
		Target:     target,
		Push:       true,
		Insecure:   true,
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.ImageDigest == "" {
		t.Fatal("expected an image digest")
	}

	ref, err := name.ParseReference(target, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Image(ref); err != nil {
		t.Fatalf("pull built image: %v", err)
	}
}

// TestRegistryCache exercises the registry remote-cache backend end to end:
// exporting the build cache to the (insecure, in-process) registry and importing
// it into a fresh engine. Before the backend was wired, cache-to type=registry
// fails with "unknown cache exporter: registry". Requires root; skipped otherwise.
func TestRegistryCache(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("build execution requires root or a rootless userns stack; skipping on unprivileged host")
	}

	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	mux := http.NewServeMux()
	registry.New(st).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\nCOPY hello.txt /hello.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheRef := host + "/app:buildcache"
	cacheAttrs := map[string]string{"ref": cacheRef, "registry.insecure": "true"}

	// Export the cache to the registry.
	eng, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close()
	exportAttrs := map[string]string{"ref": cacheRef, "registry.insecure": "true", "mode": "max"}
	if _, err := eng.Build(context.Background(), Request{
		ContextDir:   ctxDir,
		Target:       host + "/app:v1",
		Push:         true,
		Insecure:     true,
		CacheExports: []CacheOption{{Type: "registry", Attrs: exportAttrs}},
	}, nil); err != nil {
		t.Fatalf("build with --cache-to type=registry: %v", err)
	}

	// The cache manifest must now exist in the registry.
	cref, err := name.ParseReference(cacheRef, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(cref); err != nil {
		t.Fatalf("cache manifest was not pushed to the registry: %v", err)
	}

	// A fresh engine (no local cache) importing that cache must build cleanly —
	// proving the registry importer resolver is wired.
	eng2, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("New engine 2: %v", err)
	}
	defer eng2.Close()
	if _, err := eng2.Build(context.Background(), Request{
		ContextDir:   ctxDir,
		Target:       host + "/app:v2",
		Push:         true,
		Insecure:     true,
		CacheImports: []CacheOption{{Type: "registry", Attrs: cacheAttrs}},
	}, nil); err != nil {
		t.Fatalf("build with --cache-from type=registry: %v", err)
	}
}

// TestLocalCache exercises the engine-managed type=local cache: --cache-to
// type=local,dest=<key> writes an OCI layout under <Root>/localcache/<key>
// (the key is a pseudo-path, not a real filesystem path), and a fresh engine
// (fresh bbolt) whose managed dir is seeded only from that export imports it via
// --cache-from type=local,src=<key> and reproduces the same image.
func TestLocalCache(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("build execution requires root or a rootless userns stack; skipping on unprivileged host")
	}

	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	mux := http.NewServeMux()
	registry.New(st).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\nCOPY hello.txt /hello.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const key = "demo"

	// Export the cache to the engine-managed local store under rootA.
	rootA := t.TempDir()
	eng, err := New(Config{Root: rootA})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	res1, err := eng.Build(context.Background(), Request{
		ContextDir:   ctxDir,
		Target:       host + "/app:v1",
		Push:         true,
		Insecure:     true,
		CacheExports: []CacheOption{{Type: "local", Attrs: map[string]string{"dest": key, "mode": "max"}}},
	}, nil)
	if err != nil {
		eng.Close()
		t.Fatalf("build with --cache-to type=local: %v", err)
	}
	eng.Close()

	// The managed dir must now hold an OCI layout (proof the key mapped to a real
	// on-disk store and the cache was exported there).
	cacheDir := filepath.Join(rootA, "localcache", key)
	if _, err := os.Stat(filepath.Join(cacheDir, "index.json")); err != nil {
		t.Fatalf("local cache not materialized at %s: %v", cacheDir, err)
	}

	// Seed a fresh engine's managed dir with only the exported store (fresh bbolt,
	// so the sole cache source is the imported local store), then import by key.
	rootB := t.TempDir()
	if err := os.CopyFS(filepath.Join(rootB, "localcache", key), os.DirFS(cacheDir)); err != nil {
		t.Fatalf("seed local cache: %v", err)
	}
	eng2, err := New(Config{Root: rootB})
	if err != nil {
		t.Fatalf("New engine 2: %v", err)
	}
	defer eng2.Close()
	res2, err := eng2.Build(context.Background(), Request{
		ContextDir:   ctxDir,
		Target:       host + "/app:v2",
		Push:         true,
		Insecure:     true,
		CacheImports: []CacheOption{{Type: "local", Attrs: map[string]string{"src": key}}},
	}, nil)
	if err != nil {
		t.Fatalf("build with --cache-from type=local: %v", err)
	}
	if res1.ImageDigest == "" || res2.ImageDigest != res1.ImageDigest {
		t.Fatalf("image digest mismatch: export=%q import=%q", res1.ImageDigest, res2.ImageDigest)
	}
}
