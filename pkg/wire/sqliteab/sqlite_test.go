package sqliteab

import (
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"
	_ "github.com/mattn/go-sqlite3"
	"github.com/psanford/sqlite3vfs"

	"cornus/pkg/blockcache"
	"cornus/pkg/wire"
)

// diskStore returns a factory that builds an on-disk cache store (the production
// backend), rooted in a fresh temp dir per stack. nil selects MemStore.
func diskStore(tb testing.TB) func(int64) blockcache.Store {
	return func(chunk int64) blockcache.Store {
		s, err := blockcache.NewDiskStore(tb.TempDir(), chunk)
		if err != nil {
			tb.Fatalf("NewDiskStore: %v", err)
		}
		return s
	}
}

// vfsSeq gives every registered VFS a unique name (RegisterVFS is a process-wide
// registry with no unregister; one name per stack avoids collisions).
var vfsSeq atomic.Uint64

func registerVFS(root p9.File) (string, error) {
	name := fmt.Sprintf("blockproxy-%d", vfsSeq.Add(1))
	if err := sqlite3vfs.RegisterVFS(name, &blockVFS{root: root}); err != nil {
		return "", err
	}
	return name, nil
}

// openDB stands up a stack + VFS + *sql.DB backed by the block proxy over the
// given yamux send path. It pins one connection so the no-op VFS locking is
// sound, and returns a teardown.
func openDB(tb testing.TB, mode yamux.SendMode, depth int, features uint32, sync string, mkStore func(int64) blockcache.Store) (*sql.DB, *blockStack) {
	tb.Helper()
	st, err := newBlockStackChunk(tb.TempDir(), mode, depth, features, chunkSize, 0, mkStore, 0)
	if err != nil {
		tb.Fatalf("newBlockStack: %v", err)
	}
	vfsName, err := registerVFS(st.Root)
	if err != nil {
		st.Close()
		tb.Fatalf("registerVFS: %v", err)
	}
	db, err := sql.Open("sqlite3", "bench.db?vfs="+vfsName)
	if err != nil {
		st.Close()
		tb.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA page_size=4096",
		"PRAGMA journal_mode=DELETE", // rollback journal: the 9p-compatible durable path
		"PRAGMA synchronous=" + sync,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			st.Close()
			tb.Fatalf("%s: %v", pragma, err)
		}
	}
	return db, st
}

// randRow returns an ~n-byte pseudo-random printable blob for a table value.
func randRow(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + rng.Intn(26))
	}
	return b
}

// TestSQLiteOverBlockProxy runs a real SQLite workload over both send paths and
// proves end-to-end correctness: the in-mount row count and PRAGMA
// integrity_check are consistent, AND the bytes that propagated through the block
// proxy to the authoritative on-disk file form a valid, coherent SQLite database
// when reopened with the ordinary OS VFS.
func TestSQLiteOverBlockProxy(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode yamux.SendMode
	}{
		{"sync", yamux.SendSync},
		{"batched", yamux.SendBatchedPipelined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rows = 5000
			db, st := openDB(t, tc.mode, 4, 0, "FULL", nil)
			defer st.Close()
			defer db.Close()

			if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)"); err != nil {
				t.Fatalf("create: %v", err)
			}
			rng := rand.New(rand.NewSource(1))
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			stmt, err := tx.Prepare("INSERT INTO t(v) VALUES(?)")
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			var wantBytes int64
			for i := 0; i < rows; i++ {
				r := randRow(rng, 100)
				wantBytes += int64(len(r))
				if _, err := stmt.Exec(r); err != nil {
					t.Fatalf("insert %d: %v", i, err)
				}
			}
			stmt.Close()
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}

			// In-mount coherence.
			var count int
			if err := db.QueryRow("SELECT count(*) FROM t").Scan(&count); err != nil {
				t.Fatalf("count: %v", err)
			}
			if count != rows {
				t.Fatalf("row count = %d, want %d", count, rows)
			}
			var sum int64
			if err := db.QueryRow("SELECT sum(length(v)) FROM t").Scan(&sum); err != nil {
				t.Fatalf("sum: %v", err)
			}
			if sum != wantBytes {
				t.Fatalf("sum(length(v)) = %d, want %d", sum, wantBytes)
			}
			var ic string
			if err := db.QueryRow("PRAGMA integrity_check").Scan(&ic); err != nil {
				t.Fatalf("integrity_check: %v", err)
			}
			if ic != "ok" {
				t.Fatalf("integrity_check = %q, want ok", ic)
			}
			db.Close()

			// Durability: the authoritative on-disk file (written through the block
			// proxy) is itself a valid, coherent SQLite database read with the
			// ordinary OS VFS — the ultimate write-through correctness proof.
			onDisk := filepath.Join(st.Dir, "bench.db")
			raw, err := sql.Open("sqlite3", onDisk)
			if err != nil {
				t.Fatalf("open on-disk: %v", err)
			}
			defer raw.Close()
			var dcount int
			if err := raw.QueryRow("SELECT count(*) FROM t").Scan(&dcount); err != nil {
				t.Fatalf("on-disk count: %v", err)
			}
			if dcount != rows {
				t.Fatalf("on-disk row count = %d, want %d", dcount, rows)
			}
			var dic string
			if err := raw.QueryRow("PRAGMA integrity_check").Scan(&dic); err != nil {
				t.Fatalf("on-disk integrity_check: %v", err)
			}
			if dic != "ok" {
				t.Fatalf("on-disk integrity_check = %q, want ok", dic)
			}
			t.Logf("%s: %d rows, integrity ok in-mount and on-disk", tc.name, rows)
		})
	}
}

// insertWorkload runs one timed unit: rowsPerIter inserts in a single
// transaction (the bulk-load / batched-commit DB shape). Returns bytes written.
func insertWorkload(tb testing.TB, db *sql.DB, rng *rand.Rand, rowsPerIter int) int64 {
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare("INSERT INTO t(v) VALUES(?)")
	if err != nil {
		tb.Fatalf("prepare: %v", err)
	}
	var bytes int64
	for j := 0; j < rowsPerIter; j++ {
		r := randRow(rng, 100)
		bytes += int64(len(r))
		if _, err := stmt.Exec(r); err != nil {
			tb.Fatalf("insert: %v", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit: %v", err)
	}
	return bytes
}

func benchInsert(b *testing.B, mode yamux.SendMode, depth int, features uint32, sync string, mkStore func(int64) blockcache.Store) {
	const rowsPerIter = 1000
	db, st := openDB(b, mode, depth, features, sync, mkStore)
	defer st.Close()
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)"); err != nil {
		b.Fatalf("create: %v", err)
	}
	rng := rand.New(rand.NewSource(1))
	// Warm one transaction so the DB file/pager is established before timing.
	insertWorkload(b, db, rng, rowsPerIter)

	b.ReportAllocs()
	b.SetBytes(int64(rowsPerIter) * 100)
	b.ResetTimer()
	var totalRows int64
	for i := 0; i < b.N; i++ {
		insertWorkload(b, db, rng, rowsPerIter)
		totalRows += rowsPerIter
	}
	b.StopTimer()
	b.ReportMetric(float64(totalRows)/b.Elapsed().Seconds(), "rows/s")
}

// BenchmarkInsertTxn_Sync vs _Batched is the A/B: identical SQLite bulk-insert
// workload over the classic synchronous send path vs the batched-pipelined one.
func BenchmarkInsertTxn_Sync(b *testing.B) { benchInsert(b, yamux.SendSync, 4, 0, "NORMAL", nil) }
func BenchmarkInsertTxn_Batched(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, 0, "NORMAL", nil)
}

// The synchronous=FULL variants add a second fsync per commit (the strict
// durability setting), stressing the block-proxy Tfsync path.
func BenchmarkInsertTxnFull_Sync(b *testing.B) { benchInsert(b, yamux.SendSync, 4, 0, "FULL", nil) }
func BenchmarkInsertTxnFull_Batched(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, 0, "FULL", nil)
}

// The coherence-mode A/B: identical SQLite insert workload (batched send path,
// synchronous=FULL — the durable DB shape) over the classic per-write full-block
// hashing vs sub-block hashing (Feature 2) vs deferred hash-at-fsync (Feature 1)
// vs both, on the in-memory cache (Mem) and the production on-disk cache (Disk).
func BenchmarkCoherenceFull_Classic(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, 0, "FULL", nil)
}
func BenchmarkCoherenceFull_SubBlock(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, wire.FeatSubBlockHash, "FULL", nil)
}
func BenchmarkCoherenceFull_Defer(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, wire.FeatDeferHash, "FULL", nil)
}
func BenchmarkCoherenceFull_SubDefer(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, wire.FeatSubBlockHash|wire.FeatDeferHash, "FULL", nil)
}

// benchSequentialWrite writes a 32 MiB file in sequential 1 MiB (full-block) writes
// via the p9 client — the bulk/sequential-write shape. Each full-block write is
// hashed straight from the write buffer on the caller (no read-back).
func benchSequentialWrite(b *testing.B, features uint32) {
	st, err := newBlockStackChunk(b.TempDir(), yamux.SendBatchedPipelined, 4, features, chunkSize, 0, nil, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	_, nf, err := st.Root.Walk(nil)
	if err != nil {
		b.Fatal(err)
	}
	f, _, _, err := nf.Create("seq", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		b.Fatalf("create: %v", err)
	}
	buf := make([]byte, chunkSize)
	for i := range buf {
		buf[i] = byte(i)
	}
	const fileSize = 32 << 20
	b.SetBytes(fileSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for off := int64(0); off < fileSize; off += chunkSize {
			if _, err := f.WriteAt(buf, off); err != nil {
				b.Fatalf("write: %v", err)
			}
		}
	}
}

func BenchmarkSeqWrite_Classic(b *testing.B)  { benchSequentialWrite(b, 0) }
func BenchmarkSeqWrite_SubBlock(b *testing.B) { benchSequentialWrite(b, wire.FeatSubBlockHash) }

// The same coherence A/B over the production ON-DISK cache store.
func BenchmarkCoherenceFullDisk_Classic(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, 0, "FULL", diskStore(b))
}
func BenchmarkCoherenceFullDisk_SubBlock(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, wire.FeatSubBlockHash, "FULL", diskStore(b))
}
func BenchmarkCoherenceFullDisk_Defer(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, wire.FeatDeferHash, "FULL", diskStore(b))
}
func BenchmarkCoherenceFullDisk_SubDefer(b *testing.B) {
	benchInsert(b, yamux.SendBatchedPipelined, 4, wire.FeatSubBlockHash|wire.FeatDeferHash, "FULL", diskStore(b))
}

// TestSQLiteCoherenceModes proves a real SQLite workload stays correct end to end
// under every block-coherence mode, on BOTH cache backends (in-memory and the
// production on-disk store): integrity ok in-mount, and the authoritative on-disk
// file (written through the proxy) is a valid, coherent database.
func TestSQLiteCoherenceModes(t *testing.T) {
	backends := []struct {
		name string
		disk bool
	}{{"mem", false}, {"disk", true}}
	modes := []struct {
		name     string
		features uint32
	}{
		{"classic", 0},
		{"subblock", wire.FeatSubBlockHash},
		{"defer", wire.FeatDeferHash},
		{"subblock+defer", wire.FeatSubBlockHash | wire.FeatDeferHash},
		{"subfill", wire.FeatSubBlockFill},
		{"subfill+defer", wire.FeatSubBlockFill | wire.FeatDeferHash},
	}
	for _, be := range backends {
		for _, tc := range modes {
			t.Run(be.name+"/"+tc.name, func(t *testing.T) {
				const rows = 4000
				var mk func(int64) blockcache.Store
				if be.disk {
					mk = diskStore(t)
				}
				db, st := openDB(t, yamux.SendBatchedPipelined, 4, tc.features, "FULL", mk)
				defer st.Close()
				defer db.Close()
				if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)"); err != nil {
					t.Fatalf("create: %v", err)
				}
				rng := rand.New(rand.NewSource(2))
				insertWorkload(t, db, rng, rows)
				// Update-in-place a slice of rows (RMW pages) to exercise partial writes.
				if _, err := db.Exec("UPDATE t SET v = randomblob(100) WHERE id % 3 = 0"); err != nil {
					t.Fatalf("update: %v", err)
				}
				var count int
				if err := db.QueryRow("SELECT count(*) FROM t").Scan(&count); err != nil || count != rows {
					t.Fatalf("count = %d err=%v, want %d", count, err, rows)
				}
				var ic string
				if err := db.QueryRow("PRAGMA integrity_check").Scan(&ic); err != nil || ic != "ok" {
					t.Fatalf("integrity_check = %q err=%v", ic, err)
				}
				db.Close()
				raw, err := sql.Open("sqlite3", filepath.Join(st.Dir, "bench.db"))
				if err != nil {
					t.Fatalf("open on-disk: %v", err)
				}
				defer raw.Close()
				var dic string
				if err := raw.QueryRow("PRAGMA integrity_check").Scan(&dic); err != nil || dic != "ok" {
					t.Fatalf("on-disk integrity_check = %q err=%v", dic, err)
				}
				var dcount int
				if err := raw.QueryRow("SELECT count(*) FROM t").Scan(&dcount); err != nil || dcount != rows {
					t.Fatalf("on-disk count = %d err=%v, want %d", dcount, err, rows)
				}
				t.Logf("%s: %d rows, RMW update, integrity ok in-mount and on-disk", tc.name, rows)
			})
		}
	}
}
