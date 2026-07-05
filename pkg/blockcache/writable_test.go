package blockcache

import (
	"bytes"
	"testing"
)

// forEachStore runs fn against a fresh MemStore and a fresh DiskStore at the
// given chunk size, so the writable ops keep parity across backends.
func forEachStore(t *testing.T, chunk int64, fn func(t *testing.T, s Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) { fn(t, NewMemStore(chunk)) })
	t.Run("disk", func(t *testing.T) {
		s, err := NewDiskStore(t.TempDir(), chunk)
		if err != nil {
			t.Fatal(err)
		}
		fn(t, s)
	})
}

func wID(path string) FileID { return FileID{Mount: "deploy/x/m0", Path: path, Writable: true} }

func getChunk(t *testing.T, s Store, id FileID, idx int, n int) ([]byte, bool) {
	t.Helper()
	buf := make([]byte, n)
	got, ok, err := s.Get(id, idx, buf)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		return nil, false
	}
	return buf[:got], true
}

func TestWritableFullChunk(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		data := seq(chunk)
		if err := s.WriteChunk(id, 0, 0, data, chunk, hashChunk(data)); err != nil {
			t.Fatal(err)
		}
		got, ok := getChunk(t, s, id, 0, int(chunk))
		if !ok || !bytes.Equal(got, data) {
			t.Fatalf("full-chunk write not served back: ok=%v", ok)
		}
		h, present, err := s.ChunkHash(id, 0)
		if err != nil || !present || h != hashChunk(data) {
			t.Fatalf("ChunkHash = %d present=%v err=%v", h, present, err)
		}
	})
}

func TestWritablePartialRMWMatch(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		base := seq(chunk)
		if err := s.WriteChunk(id, 0, 0, base, chunk, hashChunk(base)); err != nil {
			t.Fatal(err)
		}
		// Splice 4 bytes at offset 4; compute the caller's authoritative hash.
		patch := []byte{'W', 'X', 'Y', 'Z'}
		want := append([]byte(nil), base...)
		copy(want[4:], patch)
		if err := s.WriteChunk(id, 0, 4, patch, chunk, hashChunk(want)); err != nil {
			t.Fatal(err)
		}
		got, ok := getChunk(t, s, id, 0, int(chunk))
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("RMW match: got=%v ok=%v want=%v", got, ok, want)
		}
	})
}

func TestWritablePartialRMWMismatchDrops(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		base := seq(chunk)
		if err := s.WriteChunk(id, 0, 0, base, chunk, hashChunk(base)); err != nil {
			t.Fatal(err)
		}
		// Supply a WRONG caller hash: the reconstructed chunk must be dropped, not
		// stored, so a later read miss-fills correctly rather than serving garbage.
		patch := []byte{1, 2, 3, 4}
		if err := s.WriteChunk(id, 0, 4, patch, chunk, 0xDEADBEEF); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, id, 0, int(chunk)); ok {
			t.Fatal("chunk with a hash mismatch should have been dropped")
		}
	})
}

func TestWritablePartialAbsentNoop(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		patch := []byte{1, 2, 3, 4}
		if err := s.WriteChunk(id, 2, 4, patch, chunk, 0x1234); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, id, 2, chunk); ok {
			t.Fatal("partial write to an absent chunk must not create it")
		}
	})
}

func TestWritableResizeShrinkDropsStraddlerBeyond(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		// Four full chunks => size 64.
		for _, idx := range []int{0, 1, 2, 3} {
			d := seq(chunk)
			if err := s.WriteChunk(id, idx, 0, d, chunk, hashChunk(d)); err != nil {
				t.Fatal(err)
			}
		}
		// Shrink to 40 (= 2*16 + 8): chunks 0,1 fully within stay warm; chunk 2
		// now straddles (valid length 8, its full-chunk hash is stale) -> dropped;
		// chunk 3 is wholly beyond -> dropped.
		if err := s.Resize(id, 40); err != nil {
			t.Fatal(err)
		}
		for _, idx := range []int{0, 1} {
			if _, ok := getChunk(t, s, id, idx, chunk); !ok {
				t.Fatalf("fully-within chunk %d should stay warm across shrink", idx)
			}
		}
		if _, ok := getChunk(t, s, id, 2, chunk); ok {
			t.Fatal("new straddling chunk 2 should be dropped")
		}
		if _, ok := getChunk(t, s, id, 3, chunk); ok {
			t.Fatal("chunk 3 wholly beyond new size should be dropped")
		}
	})
}

func TestWritableResizeGrowDropsOldStraddler(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		// chunk 0 full (16) + chunk 1 short (8) => current size 24.
		d0 := seq(chunk)
		if err := s.WriteChunk(id, 0, 0, d0, chunk, hashChunk(d0)); err != nil {
			t.Fatal(err)
		}
		short := seq(8)
		if err := s.WriteChunk(id, 1, 0, short, 8, hashChunk(short)); err != nil {
			t.Fatal(err)
		}
		// Grow to 80: the old straddling chunk 1 (was 8 bytes, now should be full)
		// must be dropped; the fully-within chunk 0 stays warm.
		if err := s.Resize(id, 80); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, id, 1, 8); ok {
			t.Fatal("old straddling chunk 1 should be dropped after a grow")
		}
		if _, ok := getChunk(t, s, id, 0, int(chunk)); !ok {
			t.Fatal("fully-within chunk 0 should stay warm across grow")
		}
	})
}

// TestWritableResizeShrinkThenGrow guards the presence-bit clearing across a
// shrink then grow: a chunk dropped by the shrink must NOT reappear as present
// (serving stale/zero bytes from the re-grown sparse hole) after the grow.
func TestWritableResizeShrinkThenGrow(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		for _, idx := range []int{0, 1, 2, 3} {
			d := seq(chunk)
			if err := s.WriteChunk(id, idx, 0, d, chunk, hashChunk(d)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Resize(id, 32); err != nil { // drops chunks 2,3
			t.Fatal(err)
		}
		if err := s.Resize(id, 64); err != nil { // re-grows the backing (holes)
			t.Fatal(err)
		}
		for _, idx := range []int{2, 3} {
			if got, ok := getChunk(t, s, id, idx, chunk); ok {
				t.Fatalf("chunk %d reappeared as present after shrink+grow: %v", idx, got)
			}
		}
	})
}

func TestWritableWriteThroughAndHashRange(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		base := seq(chunk)
		// WriteThrough a full chunk: present, but hash is unknown (0) until reconciled.
		if err := s.WriteThrough(id, 0, 0, base, chunk); err != nil {
			t.Fatal(err)
		}
		if got, ok := getChunk(t, s, id, 0, int(chunk)); !ok || !bytes.Equal(got, base) {
			t.Fatalf("write-through full chunk not served back: ok=%v", ok)
		}
		if h, present, _ := s.ChunkHash(id, 0); !present || h != 0 {
			t.Fatalf("write-through hash should be unknown(0), got %d present=%v", h, present)
		}
		// HashRange reconciles a sub-range against the caller's authoritative hash.
		wantSub := hashChunk(base[4:12])
		if h, present, err := s.HashRange(id, 0, 4, 8); err != nil || !present || h != wantSub {
			t.Fatalf("HashRange(4,8) = %d present=%v err=%v, want %d", h, present, err, wantSub)
		}
		// Partial write-through to a present chunk splices in place.
		patch := []byte{'A', 'B', 'C', 'D'}
		if err := s.WriteThrough(id, 0, 4, patch, chunk); err != nil {
			t.Fatal(err)
		}
		want := append([]byte(nil), base...)
		copy(want[4:], patch)
		if got, ok := getChunk(t, s, id, 0, int(chunk)); !ok || !bytes.Equal(got, want) {
			t.Fatalf("partial write-through: got=%v ok=%v want=%v", got, ok, want)
		}
		if h, present, _ := s.HashRange(id, 0, 4, 4); !present || h != hashChunk(patch) {
			t.Fatalf("HashRange after partial write-through mismatch: %d present=%v", h, present)
		}
		// Partial write-through to an absent chunk is a no-op.
		if err := s.WriteThrough(id, 5, 4, patch, chunk); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, id, 5, chunk); ok {
			t.Fatal("partial write-through to an absent chunk must not create it")
		}
		// HashRange rejects a range beyond the chunk's valid length / an absent chunk.
		if _, present, _ := s.HashRange(id, 0, 12, 8); present {
			t.Fatal("HashRange beyond valid length should report not-present")
		}
		if _, present, _ := s.HashRange(id, 9, 0, 4); present {
			t.Fatal("HashRange on an absent chunk should report not-present")
		}
	})
}

func TestWritableDemandFill(t *testing.T) {
	const chunk = 3 * SubBlockSize
	sb := int(SubBlockSize)
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		mid := bytes.Repeat([]byte{0x11}, sb)
		// PutSub the middle sub-block only.
		if err := s.PutSub(id, 0, SubBlockSize, mid); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, sb)
		if n, ok, err := s.GetSub(id, 0, SubBlockSize, got); err != nil || !ok || !bytes.Equal(got[:n], mid) {
			t.Fatalf("GetSub(mid) ok=%v err=%v", ok, err)
		}
		if _, ok, _ := s.GetSub(id, 0, 0, make([]byte, sb)); ok {
			t.Fatal("GetSub of an unfilled sub-block should miss")
		}
		if _, ok := getChunk(t, s, id, 0, int(chunk)); ok {
			t.Fatal("whole Get on a partially demand-filled chunk should miss")
		}
		// Fill the other two sub-blocks.
		z0 := bytes.Repeat([]byte{0x22}, sb)
		z2 := bytes.Repeat([]byte{0x33}, sb)
		if err := s.PutSub(id, 0, 0, z0); err != nil {
			t.Fatal(err)
		}
		if err := s.PutSub(id, 0, 2*SubBlockSize, z2); err != nil {
			t.Fatal(err)
		}
		all := make([]byte, chunk)
		n, ok, err := s.GetSub(id, 0, 0, all)
		if err != nil || !ok || n != int(chunk) {
			t.Fatalf("GetSub(all) ok=%v err=%v n=%d", ok, err, n)
		}
		want := append(append(append([]byte{}, z0...), mid...), z2...)
		if !bytes.Equal(all, want) {
			t.Fatal("GetSub(all) content mismatch")
		}
		// Never promoted by a whole-chunk op, so whole Get still misses on both stores.
		if _, ok := getChunk(t, s, id, 0, int(chunk)); ok {
			t.Fatal("whole Get on a sub-filled (unpromoted) chunk should miss")
		}
		// A full write promotes it: whole Get and GetSub both hit.
		full := seq(int(chunk))
		if err := s.WriteChunk(id, 0, 0, full, chunk, hashChunk(full)); err != nil {
			t.Fatal(err)
		}
		if g, ok := getChunk(t, s, id, 0, int(chunk)); !ok || !bytes.Equal(g, full) {
			t.Fatal("whole Get after a full write should hit")
		}
		if n, ok, _ := s.GetSub(id, 0, SubBlockSize, got); !ok || !bytes.Equal(got[:n], full[sb:2*sb]) {
			t.Fatal("GetSub after a full write should hit")
		}
	})
}

func TestWritableDropAndInvalidate(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		d := seq(chunk)
		if err := s.WriteChunk(id, 0, 0, d, chunk, hashChunk(d)); err != nil {
			t.Fatal(err)
		}
		if err := s.Drop(id, 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, id, 0, int(chunk)); ok {
			t.Fatal("dropped chunk still present")
		}
		if err := s.WriteChunk(id, 1, 0, d, chunk, hashChunk(d)); err != nil {
			t.Fatal(err)
		}
		if err := s.Invalidate(id); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, id, 1, chunk); ok {
			t.Fatal("invalidated entry still present")
		}
	})
}

func TestWritableRenameKeepsWarm(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		oldID, newID := wID("tmp"), wID("db")
		d := seq(chunk)
		if err := s.WriteChunk(oldID, 0, 0, d, chunk, hashChunk(d)); err != nil {
			t.Fatal(err)
		}
		if err := s.Rename(oldID, newID); err != nil {
			t.Fatal(err)
		}
		if _, ok := getChunk(t, s, oldID, 0, chunk); ok {
			t.Fatal("old bucket should be gone after rename")
		}
		got, ok := getChunk(t, s, newID, 0, chunk)
		if !ok || !bytes.Equal(got, d) {
			t.Fatal("renamed bucket lost its warm block")
		}
	})
}

func TestWritableHintRoundTrip(t *testing.T) {
	const chunk = 16
	forEachStore(t, chunk, func(t *testing.T, s Store) {
		id := wID("f")
		if _, _, ok, _ := s.Hint(id); ok {
			t.Fatal("no hint expected before SetHint")
		}
		if err := s.SetHint(id, 4096, 1234567890); err != nil {
			t.Fatal(err)
		}
		size, mtime, ok, err := s.Hint(id)
		if err != nil || !ok || size != 4096 || mtime != 1234567890 {
			t.Fatalf("Hint = %d,%d,%v,%v", size, mtime, ok, err)
		}
	})
}

// TestWritableStableBucketAcrossVersions proves a writable id keys on the stable
// bucket (Size/MTime ignored), unlike a read-only id.
func TestWritableStableBucketAcrossVersions(t *testing.T) {
	a := FileID{Mount: "m", Path: "f", Size: 10, MTimeNs: 1, Writable: true}
	b := FileID{Mount: "m", Path: "f", Size: 999, MTimeNs: 2, Writable: true}
	if a.Key() != b.Key() {
		t.Fatal("writable bucket key must be stable across size/mtime")
	}
	ro1 := FileID{Mount: "m", Path: "f", Size: 10, MTimeNs: 1}
	ro2 := FileID{Mount: "m", Path: "f", Size: 10, MTimeNs: 2}
	if ro1.Key() == ro2.Key() {
		t.Fatal("read-only key must change with mtime")
	}
	if a.Key() == ro1.Key() {
		t.Fatal("writable and read-only ids must not collide")
	}
}
