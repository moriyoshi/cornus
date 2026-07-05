package wire

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// blockHarnessFeat wires the block proxy/server with a negotiated coherence
// feature set (FeatSubBlockHash / FeatDeferHash), over net.Pipe — the features
// are transport-agnostic, so a pipe is enough to prove protocol correctness.
func blockHarnessFeat(t *testing.T, dir string, cache *blockcache.Cache, features uint32) *p9.Client {
	t.Helper()
	kc1, kc2 := net.Pipe()
	rs1, rs2 := net.Pipe()
	go ServeBlockProxy(kc2, rs1, cache, "m", WithBlockFeatures(features))
	go ServeBlockServer(rs2, dir, cache.ChunkSize(), WithBlockFeatures(features))
	// Match the p9 message size to the block chunk size, as production does
	// (msize == chunkSize): a kernel write then never exceeds one block, so the
	// opWrite frame stays within the block server's maxFrame (chunk + slack).
	cl, err := p9.NewClient(kc1, p9.WithMessageSize(uint32(cache.ChunkSize())))
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

// TestBlockProxyFeatureModes runs the same write / read-back / RMW / fsync
// coherence sequence under every negotiated coherence mode, with a chunk size of
// several sub-blocks so the sub-block splitting is exercised. In all modes the
// authoritative on-disk file AND the cache-served reads must stay coherent.
func TestBlockProxyFeatureModes(t *testing.T) {
	const chunk = 4 * subBlockSize // 16 KiB: 4 sub-blocks per block
	modes := []struct {
		name string
		feat uint32
	}{
		{"classic", 0},
		{"subblock", FeatSubBlockHash},
		{"defer", FeatDeferHash},
		{"subblock+defer", FeatSubBlockHash | FeatDeferHash},
		{"subfill", FeatSubBlockFill},
		{"subfill+defer", FeatSubBlockFill | FeatDeferHash},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			dir := t.TempDir()
			cache := blockcache.New(blockcache.NewMemStore(chunk), chunk)
			cl := blockHarnessFeat(t, dir, cache, m.feat)
			root, err := cl.Attach("")
			if err != nil {
				t.Fatal(err)
			}

			// Create + full write spanning multiple blocks and sub-blocks (not a
			// whole number of blocks, so the tail block is partial).
			_, nf, err := root.Walk(nil)
			if err != nil {
				t.Fatal(err)
			}
			created, _, _, err := nf.Create("db", p9.ReadWrite, 0o644, 0, 0)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			payload := make([]byte, int(chunk)*2+5000)
			for i := range payload {
				payload[i] = byte(i*7 + 1)
			}
			if _, err := created.WriteAt(payload, 0); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := created.FSync(); err != nil {
				t.Fatalf("fsync: %v", err)
			}

			onDisk, _ := os.ReadFile(filepath.Join(dir, "db"))
			if !bytes.Equal(onDisk, payload) {
				t.Fatalf("on-disk mismatch after write (%d vs %d bytes)", len(onDisk), len(payload))
			}
			g := walkOpen(t, root, "db", p9.ReadOnly)
			if got := readAllP9(t, g, len(payload)); !bytes.Equal(got, payload) {
				t.Fatalf("read-through mismatch after write")
			}

			// In-place RMW: an aligned sub-block, then an unaligned span crossing a
			// sub-block boundary — exercises the write-through + sub-block verify.
			patch := bytes.Repeat([]byte{0xAB}, int(subBlockSize))
			if _, err := created.WriteAt(patch, int64(subBlockSize)*2); err != nil {
				t.Fatalf("rmw aligned: %v", err)
			}
			odd := []byte("OVERWRITE-UNALIGNED-SPAN-1234567890")
			if _, err := created.WriteAt(odd, subBlockSize-7); err != nil {
				t.Fatalf("rmw unaligned: %v", err)
			}
			if err := created.FSync(); err != nil {
				t.Fatalf("fsync2: %v", err)
			}
			copy(payload[int64(subBlockSize)*2:], patch)
			copy(payload[subBlockSize-7:], odd)

			onDisk2, _ := os.ReadFile(filepath.Join(dir, "db"))
			if !bytes.Equal(onDisk2, payload) {
				t.Fatalf("on-disk mismatch after RMW")
			}
			h2 := walkOpen(t, root, "db", p9.ReadOnly)
			if got := readAllP9(t, h2, len(payload)); !bytes.Equal(got, payload) {
				t.Fatalf("read-after-RMW mismatch (cache incoherent)")
			}
		})
	}
}

// TestBlockProxyDeferMultiFrame forces the deferred-fsync reconciliation list to
// span MULTIPLE frames: a small chunk (so maxFrame — and thus units-per-frame — is
// small) plus many sub-blocks dirtied before a single fsync. It composes
// FeatSubBlockHash + FeatDeferHash (sub-block-granular dirty tracking) and checks
// the streamed reconciliation keeps the cache coherent with the on-disk file.
func TestBlockProxyDeferMultiFrame(t *testing.T) {
	const chunk = 2 * subBlockSize // 8 KiB: 2 sub-blocks per block, small frame cap
	dir := t.TempDir()
	cache := blockcache.New(blockcache.NewMemStore(chunk), chunk)
	cl := blockHarnessFeat(t, dir, cache, FeatSubBlockHash|FeatDeferHash)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	_, nf, err := root.Walk(nil)
	if err != nil {
		t.Fatal(err)
	}
	created, _, _, err := nf.Create("db", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// ~700 sub-blocks of dirty data (well over one frame's worth) written before a
	// single fsync, so drainDirtyUnits streams several reconciliation frames.
	payload := make([]byte, int(subBlockSize)*700)
	for i := range payload {
		payload[i] = byte(i*13 + 5)
	}
	// Write in msize-sized pieces (the p9 client splits large writes anyway).
	for off := 0; off < len(payload); off += int(chunk) {
		end := off + int(chunk)
		if end > len(payload) {
			end = len(payload)
		}
		if _, err := created.WriteAt(payload[off:end], int64(off)); err != nil {
			t.Fatalf("write @%d: %v", off, err)
		}
	}
	if err := created.FSync(); err != nil {
		t.Fatalf("fsync: %v", err)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "db")); !bytes.Equal(onDisk, payload) {
		t.Fatalf("on-disk mismatch after multi-frame fsync")
	}
	g := walkOpen(t, root, "db", p9.ReadOnly)
	if got := readAllP9(t, g, len(payload)); !bytes.Equal(got, payload) {
		t.Fatalf("read-through mismatch after multi-frame reconciliation")
	}
}
