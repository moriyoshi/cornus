package sqliteab

import (
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/blockcache"
	"cornus/pkg/wire"
)

// buildLargeDBDirect writes a rows-row DB straight to dir/bench.db with the
// ordinary OS VFS (fast native inserts), so a read benchmark can then serve it
// through a COLD block-proxy cache. Each row's blob is ~valSize bytes.
func buildLargeDBDirect(t *testing.T, dir string, rows, valSize int) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "bench.db"))
	if err != nil {
		t.Fatalf("open direct: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, p := range []string{"PRAGMA page_size=4096", "PRAGMA journal_mode=DELETE", "PRAGMA synchronous=OFF"} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	tx, _ := db.Begin()
	stmt, err := tx.Prepare("INSERT INTO t(v) VALUES(?)")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < rows; i++ {
		if _, err := stmt.Exec(randRow(rng, valSize)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// openReadDB opens the existing dir/bench.db read-only over a cold block-proxy
// stack with the given chunk size + coherence features, and a tiny SQLite page
// cache so page reads hit the VFS (and thus the block cache) rather than SQLite's
// own cache.
func openReadDB(t *testing.T, dir string, chunk int64, features uint32, readahead int64, mkStore func(int64) blockcache.Store) (*sql.DB, *blockStack) {
	return openReadDBLat(t, dir, chunk, features, readahead, mkStore, 0)
}

func openReadDBLat(t *testing.T, dir string, chunk int64, features uint32, readahead int64, mkStore func(int64) blockcache.Store, latency time.Duration) (*sql.DB, *blockStack) {
	t.Helper()
	st, err := newBlockStackChunk(dir, yamux.SendBatchedPipelined, 4, features, chunk, readahead, mkStore, latency)
	if err != nil {
		t.Fatalf("stack: %v", err)
	}
	vfsName, err := registerVFS(st.Root)
	if err != nil {
		st.Close()
		t.Fatalf("registerVFS: %v", err)
	}
	db, err := sql.Open("sqlite3", "bench.db?vfs="+vfsName+"&mode=ro")
	if err != nil {
		st.Close()
		t.Fatalf("open ro: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA cache_size=64"); err != nil {
		db.Close()
		st.Close()
		t.Fatalf("cache_size: %v", err)
	}
	return db, st
}

// TestReadAmplification measures how many bytes the block proxy actually FETCHES
// from the caller to serve (a) a sparse set of random point reads and (b) a full
// sequential scan, across block/chunk sizes. It is the evidence for whether the
// read/transfer granularity (idea "3": adaptive block size) is worth pursuing:
// large blocks over-fetch on sparse random reads but minimize round-trips on
// scans. Run: go test -run TestReadAmplification -v (skipped under -short).
func TestReadAmplification(t *testing.T) {
	if testing.Short() {
		t.Skip("read-amplification measurement; slow")
	}
	const (
		rows    = 160000 // ~80 MiB DB at ~512 B/row -> many 1 MiB blocks
		valSize = 512
		nRandom = 40 // sparse: fewer than the number of 1 MiB blocks
	)
	dir := t.TempDir()
	buildLargeDBDirect(t, dir, rows, valSize)

	configs := []struct {
		name      string
		chunk     int64
		feat      uint32
		readahead int64
		disk      bool
	}{
		{"16KiB", 16 << 10, 0, 0, false},
		{"1MiB", 1 << 20, 0, 0, false},
		{"1MiB+fill", 1 << 20, wire.FeatSubBlockFill, 0, false},               // pure demand
		{"1MiB+fill+ra64", 1 << 20, wire.FeatSubBlockFill, 64 << 10, false},   // + 64 KiB readahead
		{"1MiB+fill+ra256", 1 << 20, wire.FeatSubBlockFill, 256 << 10, false}, // + 256 KiB readahead
		{"1MiB+fill(disk)", 1 << 20, wire.FeatSubBlockFill, 0, true},          // demand-fill on the on-disk cache
		{"1MiB+fill+ra64(disk)", 1 << 20, wire.FeatSubBlockFill, 64 << 10, true},
	}
	t.Logf("%-22s %-24s %-24s", "config", "random-reads fetched", "scan fetched / round-trips")
	for _, c := range configs {
		var mk func(int64) blockcache.Store
		if c.disk {
			mk = diskStore(t)
		}
		// --- random point reads (sparse working set) ---
		db, st := openReadDB(t, dir, c.chunk, c.feat, c.readahead, mk)
		var warm []byte
		_ = db.QueryRow("SELECT v FROM t WHERE id=1").Scan(&warm) // load page1+schema+root
		st.ResetCounters()
		rng := rand.New(rand.NewSource(99))
		for i := 0; i < nRandom; i++ {
			id := rng.Intn(rows) + 1
			var v []byte
			if err := db.QueryRow("SELECT v FROM t WHERE id=?", id).Scan(&v); err != nil {
				t.Fatalf("point read: %v", err)
			}
		}
		randFetched := st.FromCaller()
		db.Close()
		st.Close()

		// --- sequential scan (touches everything). Skipped for the DiskStore configs:
		// scan fetch bytes are store-independent (a protocol property), and a
		// page-by-page scan does one fsync'd disk fill per page, which is
		// pathologically slow — the point of the disk rows is to confirm the random
		// demand-fill CACHING carries over (few bytes fetched), not to time a scan.
		scanLine := "scan skipped (disk)"
		if !c.disk {
			mk2 := mk
			db2, st2 := openReadDB(t, dir, c.chunk, c.feat, c.readahead, mk2)
			st2.ResetCounters()
			t0 := time.Now()
			var sum int64
			if err := db2.QueryRow("SELECT sum(length(v)) FROM t").Scan(&sum); err != nil {
				t.Fatalf("scan: %v", err)
			}
			scanFetched := st2.FromCaller()
			scanMS := time.Since(t0).Milliseconds()
			db2.Close()
			st2.Close()
			scanLine = fmt.Sprintf("%.1f MiB, ~%d rt, %dms", float64(scanFetched)/(1<<20), scanFetched/fetchUnit(c.chunk, c.feat, c.readahead), scanMS)
		}

		t.Logf("%-22s %-24s %s",
			c.name,
			fmt.Sprintf("%.1f MiB (%.0f KiB/q)", float64(randFetched)/(1<<20), float64(randFetched)/float64(nRandom)/1024),
			scanLine)
	}
}

// TestSpeculativePrefetch measures speculative (background) readahead's
// latency-hiding: a sequential SQLite scan over a cold cache on a simulated
// high-latency link (each block-protocol reply delayed), with prefetch OFF
// (readahead 0 -> every fetch blocks) vs ON (readahead 256k -> the next range is
// prefetched in the background so the reader rarely blocks). Prefetch should be
// substantially faster. Run explicitly (skipped under -short).
func TestSpeculativePrefetch(t *testing.T) {
	if testing.Short() {
		t.Skip("speculative-prefetch latency measurement; slow")
	}
	const (
		rows    = 40000 // ~20 MiB DB
		valSize = 512
		latency = 2 * time.Millisecond
	)
	dir := t.TempDir()
	buildLargeDBDirect(t, dir, rows, valSize)

	scan := func(features uint32, readahead int64) (time.Duration, int64) {
		db, st := openReadDBLat(t, dir, 1<<20, features, readahead, nil, latency)
		defer st.Close()
		defer db.Close()
		t0 := time.Now()
		var sum int64
		if err := db.QueryRow("SELECT sum(length(v)) FROM t").Scan(&sum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return time.Since(t0), st.FromCaller()
	}
	for _, m := range []struct {
		name string
		feat uint32
	}{
		{"demand-fill", wire.FeatSubBlockFill},
		{"classic", 0},
	} {
		noPfT, _ := scan(m.feat, 0)
		pfT, pfBytes := scan(m.feat, 256<<10)
		t.Logf("%-12s scan @ %v latency:  no-prefetch=%-7v  prefetch=%-6v  speedup=%.1fx  (prefetch fetched %.1f MiB)",
			m.name, latency, noPfT.Round(time.Millisecond), pfT.Round(time.Millisecond),
			float64(noPfT)/float64(pfT), float64(pfBytes)/(1<<20))
		if pfT >= noPfT {
			t.Fatalf("%s: prefetch (%v) should beat pure demand (%v) under latency", m.name, pfT, noPfT)
		}
	}
}

// fetchUnit returns the effective per-miss fetch size for the round-trip estimate:
// under demand-fill the readahead window (>= one sub-block), else the chunk size.
func fetchUnit(chunk int64, feat uint32, readahead int64) int64 {
	if feat&wire.FeatSubBlockFill == 0 {
		return chunk
	}
	if readahead > 4096 {
		return readahead
	}
	return 4096
}
