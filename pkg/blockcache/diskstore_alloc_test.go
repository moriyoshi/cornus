package blockcache

import (
	"runtime"
	"testing"
)

// TestDiskStoreRMWDoesNotAllocateAChunkPerWrite is the enforceable form of what
// was previously only a benchmark.
//
// A benchmark reports; it does not fail. The DiskStore scratch pool was added
// because "a 4 KiB SQLite page write allocated a fresh 1 MiB RMW buffer" (see
// getScratch), and with no ceiling anywhere, deleting the pool restored exactly
// that and every test still passed. Nothing would have surfaced it either: the
// cache stays correct without the pool, just far more expensive, so the symptom is
// a memory profile nobody was reading.
//
// The measurement is BYTES, not testing.AllocsPerRun's count. Removing the pool
// changes the count by one or two per op — small enough to sit inside normal
// variance and to tempt a loose ceiling that catches nothing. It changes the bytes
// by a whole chunk per op, which is unambiguous.
//
// # The hash matters, and getting it wrong made the first version of this test worthless
//
// WriteChunk's read-modify-write branch is reached only when the chunk is already
// PRESENT (`!full && !present` returns early), and at the end of that branch the
// chunk is KEPT only if hashChunk(result) equals the caller's hash — otherwise the
// server's base is considered divergent and the chunk is dropped. The first draft
// of this test passed callerHash 0. That never matches, so write #1 dropped the
// chunk and writes #2..#200 took the early return and did nothing at all. The test
// measured 199 no-ops, reported a comfortable figure, and passed identically with
// the pool removed.
//
// Hence the running hashes below, and — more importantly — the explicit assertion
// afterwards that the chunk is still present with the expected hash. Without that
// check, any future change that makes these writes no-ops silently empties this
// test again while leaving it green.
func TestDiskStoreRMWDoesNotAllocateAChunkPerWrite(t *testing.T) {
	const (
		chunkSize = 256 << 10 // large enough that a per-op chunk allocation dwarfs noise
		writeSize = 4 << 10   // the SQLite page write that motivated the pool
		ops       = 200
	)

	store, err := NewDiskStore(t.TempDir(), chunkSize)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id := FileID{Mount: "m", Path: "/db"}
	full := make([]byte, chunkSize)
	for i := range full {
		full[i] = byte(i)
	}
	// Seed the chunk so the writes below take the read-modify-write branch rather
	// than the whole-chunk one, which never touches the pool.
	if err := store.Put(id, 0, full); err != nil {
		t.Fatal(err)
	}

	page := make([]byte, writeSize)
	for i := range page {
		page[i] = 0xBB
	}

	// Precompute each step's offset and the hash the chunk will have AFTER that
	// write, so the measured loop does no hashing of its own. Step 0 is the warm-up
	// (pool populated, index loaded); steps 1..ops are measured.
	expected := append([]byte(nil), full...)
	offs := make([]int64, ops+1)
	hashes := make([]uint64, ops+1)
	for i := 0; i <= ops; i++ {
		off := int64((i % 16) * writeSize)
		copy(expected[off:], page)
		offs[i] = off
		hashes[i] = hashChunk(expected)
	}

	if err := store.WriteChunk(id, 0, offs[0], page, chunkSize, hashes[0]); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 1; i <= ops; i++ {
		if err := store.WriteChunk(id, 0, offs[i], page, chunkSize, hashes[i]); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	runtime.ReadMemStats(&after)

	// Positive control, and the reason this test is not the no-op it started as:
	// every write must have been KEPT. A dropped chunk turns each later write into
	// an early return, which allocates nothing and would satisfy any ceiling.
	got, ok, err := store.ChunkHash(id, 0)
	if err != nil {
		t.Fatalf("ChunkHash: %v", err)
	}
	if !ok || got != hashes[ops] {
		t.Fatalf("after %d read-modify-write ops the chunk is present=%v hash=%#x, want present with %#x. "+
			"The writes were rejected and took the `!full && !present` early return, so the measurement "+
			"below describes no-ops rather than read-modify-writes.", ops, ok, got, hashes[ops])
	}

	perOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(ops)

	// The ceiling is a quarter of a chunk: comfortably above the real per-op cost
	// (index bookkeeping and I/O buffers) and far below the chunk-sized allocation
	// the pool exists to prevent. A ceiling tight enough to track the true figure
	// would fail on unrelated allocation changes; this one fails only on the
	// regression it names.
	const ceiling = chunkSize / 4
	if perOp > ceiling {
		t.Errorf("read-modify-write allocated %.0f bytes per %d-byte write (ceiling %d, chunk %d).\n"+
			"That is chunk-scale, which means each partial write allocates a fresh RMW buffer instead of "+
			"drawing one from DiskStore.scratch — the exact cost the pool was added to remove. A 4 KiB "+
			"page write should not cost a chunk.", perOp, writeSize, ceiling, chunkSize)
	}
	t.Logf("read-modify-write: %.0f bytes allocated per %d-byte write (chunk %d)", perOp, writeSize, chunkSize)
}

// TestDiskStoreScratchIsReused pins the pool's mechanism directly, as a companion
// to the byte ceiling above.
//
// The ceiling is a statistical measurement and could in principle be satisfied by
// some future allocation-free path that is not the pool. This asserts the property
// that actually holds: a buffer returned to the pool comes back out rather than
// being freshly made. It is deliberately about the BACKING ARRAY, which is the only
// observable difference between a working pool and one whose Get is a no-op.
func TestDiskStoreScratchIsReused(t *testing.T) {
	store, err := NewDiskStore(t.TempDir(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := store.getScratch(1024)
	first[0] = 0x5A // a marker the pooled array carries back if it is truly reused
	firstCap := cap(first)
	store.putScratch(first)

	second := store.getScratch(1024)
	defer store.putScratch(second)

	if cap(second) != firstCap {
		t.Errorf("getScratch returned a buffer with cap %d after one with cap %d went back to the pool; "+
			"the pooled buffer was not reused, so every partial write allocates afresh",
			cap(second), firstCap)
	}
	if second[0] != 0x5A {
		t.Error("the buffer from the pool is not the one that was returned to it (the marker byte is gone), " +
			"so getScratch is allocating rather than drawing from DiskStore.scratch")
	}
	if int64(firstCap) < store.chunkSize {
		t.Errorf("scratch capacity %d is below the chunk size %d, so a full-chunk read-modify-write "+
			"would reallocate anyway", firstCap, store.chunkSize)
	}
}
