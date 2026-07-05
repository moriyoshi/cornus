package blockcache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskStoreRoundTripAndPersistence(t *testing.T) {
	dir := t.TempDir()
	const chunk = 64
	data := seq(1000)
	id := FileID{Mount: "ctx", Path: "a/b.txt", Size: int64(len(data)), MTimeNs: 12345}

	store, err := NewDiskStore(dir, chunk)
	if err != nil {
		t.Fatal(err)
	}
	o := &origin{data: data}
	c := New(store, chunk)

	got := readAll(t, c, id, o, int64(len(data)), 128)
	if !bytes.Equal(got, data) {
		t.Fatal("first read mismatch")
	}

	// A brand-new Cache over a brand-new store reading the same on-disk root must
	// serve entirely from disk (no origin reads).
	store2, err := NewDiskStore(dir, chunk)
	if err != nil {
		t.Fatal(err)
	}
	c2 := New(store2, chunk)
	o2 := &origin{data: data}
	got2 := readAll(t, c2, id, o2, int64(len(data)), 128)
	if !bytes.Equal(got2, data) {
		t.Fatal("persisted read mismatch")
	}
	if o2.reads.Load() != 0 {
		t.Fatalf("persisted read hit origin for %d bytes", o2.reads.Load())
	}
}

func TestDiskStoreInvalidatesOnMTimeChange(t *testing.T) {
	dir := t.TempDir()
	const chunk = 64
	v1 := bytes.Repeat([]byte{'A'}, 200)
	store, _ := NewDiskStore(dir, chunk)
	c := New(store, chunk)

	id1 := FileID{Mount: "m", Path: "f", Size: 200, MTimeNs: 1}
	o1 := &origin{data: v1}
	_ = readAll(t, c, id1, o1, 200, 200)

	// Same path, changed content + mtime => new FileID => must refetch, never
	// serve v1's bytes.
	v2 := bytes.Repeat([]byte{'B'}, 200)
	id2 := FileID{Mount: "m", Path: "f", Size: 200, MTimeNs: 2}
	o2 := &origin{data: v2}
	got := readAll(t, c, id2, o2, 200, 200)
	if !bytes.Equal(got, v2) {
		t.Fatal("stale content served after mtime change")
	}
	if o2.reads.Load() == 0 {
		t.Fatal("expected origin refetch after mtime change")
	}
}

func TestDiskStoreTornWriteNotPresent(t *testing.T) {
	// Simulate a crash between the data write and the sidecar update by writing a
	// .data file with no matching .idx: Get must report a miss.
	dir := t.TempDir()
	store, _ := NewDiskStore(dir, 64)
	id := FileID{Mount: "m", Path: "f", Size: 64, MTimeNs: 1}
	key := id.Key()
	shard := filepath.Join(dir, key[:2])
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shard, key+".data"), bytes.Repeat([]byte{'X'}, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, ok, err := store.Get(id, 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	if ok || n != 0 {
		t.Fatalf("torn write reported present: ok=%v n=%d", ok, n)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	const chunk = 64
	store, _ := NewDiskStore(dir, chunk)
	c := New(store, chunk)

	// Populate three distinct files.
	for i, mt := range []int64{1, 2, 3} {
		data := bytes.Repeat([]byte{byte('A' + i)}, 500)
		id := FileID{Mount: "m", Path: "f" + string(rune('0'+i)), Size: 500, MTimeNs: mt}
		o := &origin{data: data}
		_ = readAll(t, c, id, o, 500, 500)
	}

	// TTL pass: nothing old yet.
	freed, err := Prune(dir, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 0 {
		t.Fatalf("TTL prune removed %d fresh files", freed)
	}

	// Size-cap pass: force eviction down to roughly one file. Each backing file
	// is ~500 bytes, so a 700-byte cap must drop at least one.
	freed, err = Prune(dir, 0, 700)
	if err != nil {
		t.Fatal(err)
	}
	if freed == 0 {
		t.Fatal("size-cap prune removed nothing")
	}

	// Missing root is not an error.
	if _, err := Prune(filepath.Join(dir, "nope"), time.Hour, 0); err != nil {
		t.Fatalf("prune of missing root errored: %v", err)
	}
}

func TestDiskUsage(t *testing.T) {
	dir := t.TempDir()

	// Missing root is zero, not an error.
	if b, n, err := DiskUsage(filepath.Join(dir, "absent")); err != nil || b != 0 || n != 0 {
		t.Fatalf("DiskUsage(absent) = %d,%d,%v", b, n, err)
	}

	const chunk = 64
	store, _ := NewDiskStore(dir, chunk)
	c := New(store, chunk)
	for i, mt := range []int64{1, 2} {
		data := bytes.Repeat([]byte{byte('A' + i)}, 300)
		id := FileID{Mount: "m", Path: "f" + string(rune('0'+i)), Size: 300, MTimeNs: mt}
		o := &origin{data: data}
		_ = readAll(t, c, id, o, 300, 300)
	}
	b, n, err := DiskUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("DiskUsage file count = %d, want 2", n)
	}
	if b != 600 { // two 300-byte sparse files, apparent size
		t.Fatalf("DiskUsage bytes = %d, want 600", b)
	}
}

func TestDiskStoreDirectGetMiss(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewDiskStore(dir, 64)
	id := FileID{Mount: "m", Path: "absent", Size: 64, MTimeNs: 1}
	n, ok, err := store.Get(id, 0, make([]byte, 64))
	if err != nil || ok || n != 0 {
		t.Fatalf("absent Get: n=%d ok=%v err=%v", n, ok, err)
	}
	_ = io.EOF
}
