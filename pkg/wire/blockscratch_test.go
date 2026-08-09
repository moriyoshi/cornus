package wire

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hugelgupf/p9/p9"
)

// TestScratchListSurvivesGC is the MECHANISM test for scratchList, and the two
// runtime.GC() calls are the whole assertion.
//
// A byte ceiling (below) is a statistical measurement that some future
// allocation-free path could satisfy without the free list existing. This asserts
// the one property that actually distinguishes a working free list from the
// sync.Pool it replaced: a buffer put back before a collection is the SAME
// BACKING ARRAY afterwards. Run this against a sync.Pool and the marker byte is
// gone; run it without the collections and it passes with either, i.e. it
// degenerates into testing nothing.
func TestScratchListSurvivesGC(t *testing.T) {
	const size = 1 << 20
	l := newScratchList(size, blockScratchKeep)

	bp := l.get()
	if cap(*bp) != size {
		t.Fatalf("get returned cap %d, want %d", cap(*bp), size)
	}
	(*bp)[0] = 0x5A
	firstCap := cap(*bp)
	l.put(bp)

	runtime.GC()
	runtime.GC()

	got := l.get()
	if cap(*got) != firstCap {
		t.Fatalf("after two collections got cap %d, want %d", cap(*got), firstCap)
	}
	if (*got)[0] != 0x5A {
		t.Fatal("the buffer returned after two collections is not the one that went in " +
			"(the marker byte is gone) — the reuse is GC-drainable, which is the exact " +
			"failure the free list replaced")
	}
}

// TestScratchListBoundsRetention is the counterweight to "GC-proof": it pins the
// memory promise. Retaining every buffer would be simpler and would pin ~17 MiB
// per writable mount for the life of the stream.
func TestScratchListBoundsRetention(t *testing.T) {
	const size = 4096
	l := newScratchList(size, blockScratchKeep)

	bufs := make([]*[]byte, 0, blockScratchKeep+5)
	for i := 0; i < blockScratchKeep+5; i++ {
		bufs = append(bufs, l.get())
	}
	for _, bp := range bufs {
		l.put(bp)
	}
	if got := len(l.free); got != blockScratchKeep {
		t.Fatalf("free list holds %d buffers, want %d: the overflow must go to the "+
			"GC-drainable tier, not be pinned", got, blockScratchKeep)
	}

	// A wrong-sized buffer must not be retained: a later get() has to be able to
	// assume the full frame size, since it reads a whole request into it.
	drain(l)
	short := make([]byte, size/2)
	l.put(&short)
	if len(l.free) != 0 {
		t.Fatal("a buffer smaller than the list's size was retained; a later get() would " +
			"return a buffer too small for a frame")
	}
}

func drain(l *scratchList) {
	for {
		select {
		case <-l.free:
		default:
			return
		}
	}
}

// TestBlockServerWriteDoesNotAllocateABufferPerRequest is the BYTE ceiling: a
// 1 MiB write must not cost a fresh whole-frame buffer on the caller.
//
// Two design choices carry the test.
//
// It drives the blockServer DIRECTLY rather than through a p9 client, because
// p9's own per-Twrite payload buffer is ~1 MiB and 69% of this path's allocated
// bytes. Including it would force a ceiling squeezed between 1 and 2 MiB, which
// would be fragile and would fail on unrelated p9 changes. Driven directly,
// reqScratch is the only frame-scale allocation in the window.
//
// It calls runtime.GC() every iteration, and that is the point rather than
// hygiene: the defect being fixed IS "the collector empties the pool between one
// request and the next". Without a forced collection a sync.Pool passes this
// comfortably, and the test would certify the bug.
func TestBlockServerWriteDoesNotAllocateABufferPerRequest(t *testing.T) {
	const (
		chunk = 1 << 20
		ops   = 64
	)
	dir := t.TempDir()
	inner, err := writableConfinedAttacher(dir)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fileOpCounter{}
	srvConn, cliConn := sockPair(t)
	go serveBlockServerFS(srvConn, &countingAttacher{inner: inner, c: fc}, chunk)

	// Advertise FeatNoCache so the negotiated mode skips the coherence read-back
	// and its scratch buffer, leaving reqScratch as the thing under measurement.
	bc, err := newBlockClient(cliConn, helloParams{
		version: blockProtoVersion, chunkSize: chunk,
		maxInflight: blockMaxInflight, features: FeatNoCache,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	ctx := context.Background()

	var w msgW
	w.u64(1)
	if _, err := bc.do(ctx, opAttach, w.b); err != nil {
		t.Fatalf("attach: %v", err)
	}
	w = msgW{}
	w.u64(1)
	w.u64(2)
	w.str("seq")
	w.u32(uint32(p9.ReadWrite))
	w.u32(0o644)
	w.u32(uint32(os.Getuid()))
	w.u32(uint32(os.Getgid()))
	if _, err := bc.do(ctx, opCreate, w.b); err != nil {
		t.Fatalf("create: %v", err)
	}

	// One reusable payload and one reusable meta buffer, sent as frame bulk, so
	// the SENDER contributes nothing per op and what is measured is the server.
	payload := make([]byte, chunk)
	payload[0], payload[chunk-1] = 0xA5, 0x5A
	write := func(i int) {
		var m msgW
		m.u64(2)
		m.u64(uint64(i) * chunk)
		m.u32(uint32(len(payload)))
		if _, err := bc.doParts(ctx, opWrite, m.b, payload); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	write(0) // warm the free list before measuring

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 1; i <= ops; i++ {
		write(i)
		runtime.GC()
	}
	runtime.ReadMemStats(&after)

	// Positive control, and the reason this is not the no-op it could silently
	// become: every write must have REACHED the export. A write rejected early —
	// an oversized frame, a bad handle — allocates nothing and satisfies any
	// ceiling, so a green run over 64 rejected frames is the failure mode here.
	if got := fc.stats().writes; got != int64(ops+1) {
		t.Fatalf("the export saw %d writes, want %d — the measurement below describes "+
			"work that did not happen", got, ops+1)
	}
	if got := fc.stats().wbytes; got != int64(ops+1)*chunk {
		t.Fatalf("the export took %d bytes, want %d", got, int64(ops+1)*chunk)
	}
	probe, err := os.ReadFile(filepath.Join(dir, "seq"))
	if err != nil {
		t.Fatal(err)
	}
	if len(probe) != (ops+1)*chunk || probe[ops*chunk] != 0xA5 {
		t.Fatalf("file is %d bytes and the last chunk's marker is %#x; the writes did not land",
			len(probe), probe[min(ops*chunk, len(probe)-1)])
	}

	perOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(ops)
	t.Logf("read-modify-write: %.0f bytes allocated per %d-byte write", perOp, chunk)
	// Loose on purpose: comfortably above the real residue (frame structs, reply
	// payloads, call bookkeeping — single-digit KiB) and far below the whole frame
	// a drained pool costs. A ceiling tracking the true figure would fail on
	// unrelated allocation changes; this one fails only on the regression it names.
	if ceiling := float64(chunk / 4); perOp > ceiling {
		t.Fatalf("a %d-byte write allocated %.0f bytes on the caller (ceiling %.0f). "+
			"That is frame-scale, which means every request draws a fresh whole-frame "+
			"buffer instead of one the free list held across the collection. The "+
			"runtime.GC() in this loop is the point: a sync.Pool passes without it.",
			chunk, perOp, ceiling)
	}
}
