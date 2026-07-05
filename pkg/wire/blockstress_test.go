package wire

import (
	"fmt"
	"sync"
	"testing"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// TestBlockProxyRepeatedAppend mimics a shell append loop (`echo >> wal` ×N):
// repeated walk/open/write/fsync/clunk cycles against one growing file, which is
// the pattern the kube async E2E hung on. Run under -timeout to catch a hang.
func TestBlockProxyRepeatedAppend(t *testing.T) {
	dir := t.TempDir()
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	// Create the file.
	_, nf, err := root.Walk(nil)
	if err != nil {
		t.Fatal(err)
	}
	created, _, _, err := nf.Create("wal", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = created.Close()

	var off int64
	for i := 0; i < 200; i++ {
		line := []byte(fmt.Sprintf("line-%d\n", i))
		_, f, err := root.Walk([]string{"wal"})
		if err != nil {
			t.Fatalf("iter %d walk: %v", i, err)
		}
		if _, _, err := f.Open(p9.ReadWrite); err != nil {
			t.Fatalf("iter %d open: %v", i, err)
		}
		if _, err := f.WriteAt(line, off); err != nil {
			t.Fatalf("iter %d write: %v", i, err)
		}
		off += int64(len(line))
		if err := f.FSync(); err != nil {
			t.Fatalf("iter %d fsync: %v", i, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("iter %d close: %v", i, err)
		}
	}
}

// TestBlockProxyConcurrentWrites mimics kernel writeback flushing many dirty pages
// of one file concurrently, then fsync — the burst pattern writeback caching produces.
func TestBlockProxyConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	cache := blockcache.New(blockcache.NewMemStore(64), 64)
	cl := blockHarness(t, dir, cache)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	_, nf, err := root.Walk(nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err := nf.Create("big", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Preallocate so writes land at fixed offsets (not appends).
	if err := f.SetAttr(p9.SetAttrMask{Size: true}, p9.SetAttr{Size: 128 * 1024}); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	const writers = 32
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			buf := make([]byte, 512)
			for j := range buf {
				buf[j] = byte('A' + w%26)
			}
			if _, err := f.WriteAt(buf, int64(w)*4096); err != nil {
				errs[w] = err
			}
		}(w)
	}
	wg.Wait()
	for w, e := range errs {
		if e != nil {
			t.Fatalf("writer %d: %v", w, e)
		}
	}
	if err := f.FSync(); err != nil {
		t.Fatalf("fsync: %v", err)
	}
}
