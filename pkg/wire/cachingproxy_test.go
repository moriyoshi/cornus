package wire

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// proxyHarness wires an in-process caller export -> caching proxy -> client,
// mirroring kernel-9p mount -> ServeCachingProxy -> caller. It returns a p9
// client that stands in for the kernel and the caller's read-byte counter.
func proxyHarness(t *testing.T, dir string, cache *blockcache.Cache) (*p9.Client, *atomic.Int64) {
	t.Helper()
	var reads atomic.Int64
	attacher, err := confinedAttacherCounted(dir, nil, &reads)
	if err != nil {
		t.Fatalf("attacher: %v", err)
	}

	// Caller export server <-> proxy's remote client, over pipe A.
	callerConn, proxyRemote := net.Pipe()
	callerSrv := p9.NewServer(attacher)
	go func() { _ = callerSrv.Handle(callerConn, callerConn) }()

	// Proxy server (answers the "kernel") <-> test client, over pipe B.
	kernelConn, testConn := net.Pipe()
	go ServeCachingProxy(kernelConn, proxyRemote, cache, "ctx")

	cl, err := p9.NewClient(testConn, p9.WithMessageSize(1<<20))
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	t.Cleanup(func() {
		_ = cl.Close()
		_ = testConn.Close()
		_ = kernelConn.Close()
		_ = proxyRemote.Close()
		_ = callerConn.Close()
	})
	return cl, &reads
}

// readFile walks to rel, opens read-only, and reads the whole file through the
// client.
func readFile(t *testing.T, cl *p9.Client, rel string) []byte {
	t.Helper()
	root, err := cl.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer root.Close()
	_, f, err := root.Walk(strings.Split(rel, "/"))
	if err != nil {
		t.Fatalf("walk %s: %v", rel, err)
	}
	defer f.Close()
	if _, _, err := f.Open(p9.ReadOnly); err != nil {
		t.Fatalf("open %s: %v", rel, err)
	}
	var out []byte
	buf := make([]byte, 4096)
	var off int64
	for {
		n, err := f.ReadAt(buf, off)
		out = append(out, buf[:n]...)
		off += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if n == 0 {
			break
		}
	}
	return out
}

func TestCachingProxyServesAndCaches(t *testing.T) {
	dir := t.TempDir()
	content := seqBytes(5000)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	// Small chunk size exercises multi-chunk reads and partial last chunk.
	cache := blockcache.New(blockcache.NewMemStore(512), 512)
	cl, reads := proxyHarness(t, dir, cache)

	got := readFile(t, cl, "big.bin")
	if !bytes.Equal(got, content) {
		t.Fatal("first read content mismatch")
	}
	first := reads.Load()
	if first < int64(len(content)) {
		t.Fatalf("caller served %d bytes, want >= %d", first, len(content))
	}

	// Second read (fresh fid) must be a full cache hit: the caller serves no more
	// content bytes.
	got2 := readFile(t, cl, "big.bin")
	if !bytes.Equal(got2, content) {
		t.Fatal("second read content mismatch")
	}
	if reads.Load() != first {
		t.Fatalf("second read hit the caller: reads %d -> %d", first, reads.Load())
	}
}

func TestCachingProxyWalkAndReaddir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := seqBytes(100)
	if err := os.WriteFile(filepath.Join(dir, "sub", "file.txt"), nested, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "top.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl, _ := proxyHarness(t, dir, cache)

	// Nested walk through the proxy resolves and reads correctly.
	if got := readFile(t, cl, "sub/file.txt"); !bytes.Equal(got, nested) {
		t.Fatal("nested read mismatch")
	}

	// Readdir of the root lists both entries.
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := root.Open(p9.ReadOnly); err != nil {
		t.Fatalf("open root: %v", err)
	}
	ents, err := root.Readdir(0, 4096)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range ents {
		if e.Name != "." && e.Name != ".." {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "sub" || names[1] != "top.txt" {
		t.Fatalf("readdir names = %v, want [sub top.txt]", names)
	}
}

func seqBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}
