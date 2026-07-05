package wire

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// TestConcurrentCallerReads measures the concurrent-caller win: many cold block
// reads issued at once, with a simulated per-read authoritative-storage cost, at
// the caller forced serial (1 slot) vs concurrent. It reveals both whether the
// request concurrency reaches the caller and how much parallel read handling helps
// when the per-read work is non-trivial (slow storage). Run explicitly.
func TestConcurrentCallerReads(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent-caller timing measurement")
	}
	const (
		nBlocks  = 32
		chunk    = 64 << 10
		readCost = 500 * time.Microsecond
	)
	dir := t.TempDir()
	data := make([]byte, chunk*nBlocks)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(slots int) time.Duration {
		prev := blockCallerReadSlots
		blockCallerReadSlots = slots
		hook := func() { time.Sleep(readCost) }
		blockCallerReadHook.Store(&hook)
		defer func() { blockCallerReadSlots = prev; blockCallerReadHook.Store(nil) }()

		cache := blockcache.New(blockcache.NewMemStore(chunk), chunk)
		cl := blockHarness(t, dir, cache)
		root, err := cl.Attach("")
		if err != nil {
			t.Fatal(err)
		}
		f := walkOpen(t, root, "f", p9.ReadOnly)
		t0 := time.Now()
		var wg sync.WaitGroup
		for b := 0; b < nBlocks; b++ {
			wg.Add(1)
			go func(b int) {
				defer wg.Done()
				buf := make([]byte, chunk)
				_, _ = f.ReadAt(buf, int64(b)*chunk)
			}(b)
		}
		wg.Wait()
		return time.Since(t0)
	}
	serial := run(1)
	concurrent := run(16)
	t.Logf("%d concurrent cold block reads @ %v caller read cost:  serial=%v  concurrent=%v  speedup=%.1fx",
		nBlocks, readCost, serial.Round(time.Millisecond), concurrent.Round(time.Millisecond),
		float64(serial)/float64(concurrent))
}

// TestConcurrentCallerWrites is the write counterpart: many block-aligned writes
// issued at once (as a kernel writeback burst would), with a simulated per-write
// authoritative-storage cost, at the caller forced serial (1 slot) vs concurrent.
// Each write targets a distinct block, so the proxy admits all of them (no
// same-block drop) and the measurement isolates the caller-side parallelism. Run
// explicitly.
func TestConcurrentCallerWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent-caller timing measurement")
	}
	const (
		nBlocks   = 32
		chunk     = 64 << 10
		writeCost = 500 * time.Microsecond
	)
	dir := t.TempDir()
	// Seed the file to nBlocks in size; the writes overwrite distinct whole blocks.
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, chunk*nBlocks), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(slots int) time.Duration {
		prev := blockCallerWriteSlots
		blockCallerWriteSlots = slots
		hook := func() { time.Sleep(writeCost) }
		blockCallerWriteHook.Store(&hook)
		defer func() { blockCallerWriteSlots = prev; blockCallerWriteHook.Store(nil) }()

		cache := blockcache.New(blockcache.NewMemStore(chunk), chunk)
		cl := blockHarness(t, dir, cache)
		root, err := cl.Attach("")
		if err != nil {
			t.Fatal(err)
		}
		f := walkOpen(t, root, "f", p9.ReadWrite)
		t0 := time.Now()
		var wg sync.WaitGroup
		for b := 0; b < nBlocks; b++ {
			wg.Add(1)
			go func(b int) {
				defer wg.Done()
				buf := make([]byte, chunk)
				for i := range buf {
					buf[i] = byte(b)
				}
				_, _ = f.WriteAt(buf, int64(b)*chunk)
			}(b)
		}
		wg.Wait()
		return time.Since(t0)
	}
	serial := run(1)
	concurrent := run(16)
	t.Logf("%d concurrent block writes @ %v caller write cost:  serial=%v  concurrent=%v  speedup=%.1fx",
		nBlocks, writeCost, serial.Round(time.Millisecond), concurrent.Round(time.Millisecond),
		float64(serial)/float64(concurrent))
}

// TestConcurrentCallerWriteHeavy is a sustained, write-heavy A/B: many writers,
// each on its OWN file, streaming page-sized (4 KiB) writes at once — the shape of
// a real writeback burst / a multi-DB (multi-tenant) write workload, which is where
// concurrent submission actually reaches the caller (a single SQLite connection
// writes serially and would never fill the pipe). Distinct files per writer means
// no same-block contention, so this isolates the caller's write parallelism. Serial
// (1 slot) vs concurrent caller, under a simulated per-write storage cost. Reports
// throughput (writes/s) so the win is visible as sustained rate, not just latency.
func TestConcurrentCallerWriteHeavy(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent-caller throughput measurement")
	}
	const (
		writers   = 16
		pages     = 128 // per writer -> 2048 writes total, 8 MiB written
		pageSize  = 4096
		chunk     = 64 << 10
		writeCost = 200 * time.Microsecond
	)
	dir := t.TempDir()
	seed := make([]byte, pages*pageSize)
	for w := 0; w < writers; w++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", w)), seed, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(slots int) time.Duration {
		prev := blockCallerWriteSlots
		blockCallerWriteSlots = slots
		hook := func() { time.Sleep(writeCost) }
		blockCallerWriteHook.Store(&hook)
		defer func() { blockCallerWriteSlots = prev; blockCallerWriteHook.Store(nil) }()

		cache := blockcache.New(blockcache.NewMemStore(chunk), chunk)
		cl := blockHarness(t, dir, cache)
		root, err := cl.Attach("")
		if err != nil {
			t.Fatal(err)
		}
		files := make([]p9.File, writers)
		for w := 0; w < writers; w++ { // open all files first (serial metadata, untimed)
			files[w] = walkOpen(t, root, fmt.Sprintf("f%d", w), p9.ReadWrite)
		}
		t0 := time.Now()
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(f p9.File) {
				defer wg.Done()
				buf := make([]byte, pageSize)
				for p := 0; p < pages; p++ {
					_, _ = f.WriteAt(buf, int64(p)*pageSize)
				}
			}(files[w])
		}
		wg.Wait()
		return time.Since(t0)
	}
	serial := run(1)
	concurrent := run(16)
	total := writers * pages
	t.Logf("%d writers x %d pages = %d x %dB writes @ %v caller cost:  serial=%v (%.0f w/s)  concurrent=%v (%.0f w/s)  speedup=%.1fx",
		writers, pages, total, pageSize, writeCost,
		serial.Round(time.Millisecond), float64(total)/serial.Seconds(),
		concurrent.Round(time.Millisecond), float64(total)/concurrent.Seconds(),
		float64(serial)/float64(concurrent))
}
