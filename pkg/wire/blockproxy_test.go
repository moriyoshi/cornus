package wire

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// blockHarness wires a kernel-side p9.Client -> ServeBlockProxy -> ServeBlockServer
// over a confined writable export of dir, with cache as the server-side cache.
func blockHarness(t *testing.T, dir string, cache *blockcache.Cache) *p9.Client {
	t.Helper()
	kc1, kc2 := net.Pipe()
	rs1, rs2 := net.Pipe()
	go ServeBlockProxy(kc2, rs1, cache, "m")
	go ServeBlockServer(rs2, dir, cache.ChunkSize())
	cl, err := p9.NewClient(kc1, p9.WithMessageSize(1<<20))
	if err != nil {
		t.Fatalf("p9 client: %v", err)
	}
	t.Cleanup(func() {
		cl.Close()
		kc1.Close()
		kc2.Close()
		rs1.Close()
		rs2.Close()
	})
	return cl
}

func walkOpen(t *testing.T, root p9.File, name string, mode p9.OpenFlags) p9.File {
	t.Helper()
	_, f, err := root.Walk([]string{name})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	if _, _, err := f.Open(mode); err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	return f
}

func readAllP9(t *testing.T, f p9.File, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	return buf[:got]
}

func TestBlockProxyWriteThroughReadCoherent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	f := walkOpen(t, root, "f", p9.ReadWrite)

	// Initial read populates the cache.
	if got := readAllP9(t, f, 11); !bytes.Equal(got, []byte("hello world")) {
		t.Fatalf("initial read = %q", got)
	}

	// Write-through reaches the caller's on-disk file.
	if n, err := f.WriteAt([]byte("HELLO"), 0); err != nil || n != 5 {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "f"))
	if !bytes.Equal(onDisk, []byte("HELLO world")) {
		t.Fatalf("caller file = %q, want HELLO world", onDisk)
	}

	// The write updated the cache coherently (RMW self-verify stored it).
	id := blockcache.FileID{Mount: "m", Path: "f", Writable: true}
	cbuf := make([]byte, 11)
	if n, ok, _ := cache.Store().Get(id, 0, cbuf); !ok || !bytes.Equal(cbuf[:n], []byte("HELLO world")) {
		t.Fatalf("cache block after write = %q ok=%v", cbuf[:n], ok)
	}

	// Read-after-write via the proxy returns the updated bytes.
	if got := readAllP9(t, f, 11); !bytes.Equal(got, []byte("HELLO world")) {
		t.Fatalf("read-after-write = %q", got)
	}
}

func TestBlockProxyCreateWriteRead(t *testing.T) {
	dir := t.TempDir()
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	// Create a fresh file, write to it, and confirm it lands on disk + reads back.
	_, nf, err := root.Walk(nil) // clone root to create under it
	if err != nil {
		t.Fatal(err)
	}
	created, _, _, err := nf.Create("new.db", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	payload := bytes.Repeat([]byte("Z"), 100) // spans 2 chunks at chunk=64
	if n, err := created.WriteAt(payload, 0); err != nil || n != 100 {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "new.db"))
	if !bytes.Equal(onDisk, payload) {
		t.Fatalf("created file on disk mismatch (%d bytes)", len(onDisk))
	}
	if got := readAllP9(t, created, 100); !bytes.Equal(got, payload) {
		t.Fatalf("read-after-create-write mismatch")
	}
}

func TestBlockProxyTruncate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), bytes.Repeat([]byte("x"), 200), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	f := walkOpen(t, root, "f", p9.ReadWrite)
	_ = readAllP9(t, f, 200) // warm the cache
	if err := f.SetAttr(p9.SetAttrMask{Size: true}, p9.SetAttr{Size: 50}); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "f")); fi.Size() != 50 {
		t.Fatalf("on-disk size after truncate = %d, want 50", fi.Size())
	}
	// A fresh handle reads exactly 50 bytes then EOF.
	g := walkOpen(t, root, "f", p9.ReadOnly)
	if got := readAllP9(t, g, 200); len(got) != 50 {
		t.Fatalf("post-truncate read = %d bytes, want 50", len(got))
	}
}

func TestBlockProxyContainment(t *testing.T) {
	dir := t.TempDir()
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := root.Walk([]string{".."}); err == nil {
		t.Fatal("walking .. should be denied by the confined guard")
	}
}

func TestBlockProxyUnlinkRecreate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	f := walkOpen(t, root, "f", p9.ReadOnly)
	_ = readAllP9(t, f, 3) // warm cache with "old"
	_ = f.Close()

	if err := root.UnlinkAt("f", 0); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	_, nf, err := root.Walk(nil)
	if err != nil {
		t.Fatal(err)
	}
	created, _, _, err := nf.Create("f", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := created.WriteAt([]byte("brandnew"), 0); err != nil {
		t.Fatalf("write new: %v", err)
	}
	g := walkOpen(t, root, "f", p9.ReadOnly)
	if got := readAllP9(t, g, 8); !bytes.Equal(got, []byte("brandnew")) {
		t.Fatalf("recreated file read = %q, want brandnew (stale cache?)", got)
	}
}
