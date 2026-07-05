package blockcache

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// DiskStore is the default on-disk Store backend. Each cached file is one sparse
// backing file plus a small sidecar index:
//
//	<root>/<aa>/<key>.data   sparse, ftruncated to the entry's length; chunk idx
//	                         lives at byte offset idx*chunkSize
//	<root>/<aa>/<key>.idx    JSON diskIndex — the presence bitmap is the authority
//	                         for which chunks are populated; Hashes[idx] carries
//	                         each present chunk's xxh3 content hash
//
// key is FileID.Key() (a sha256 hex digest); the two-character shard <aa> bounds
// directory fan-out. A chunk's data is fsync'd before its presence bit is set and
// the sidecar rewritten, so a torn write never reads back as present.
//
// Read-only entries key on (mount, path, size, mtime) and are validated by the
// id echo; a changed origin file is a new key. Writable (block-protocol) entries
// key on the stable bucket (mount, path) with size/mtime zeroed and are validated
// per block by the stored hash — see FileID.
type DiskStore struct {
	root      string
	chunkSize int64
	locks     stripedRW
}

// NewDiskStore returns an on-disk store rooted at root (created if absent) using
// the given chunk size (falling back to DefaultChunkSize when <= 0).
func NewDiskStore(root string, chunkSize int64) (*DiskStore, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &DiskStore{root: root, chunkSize: chunkSize}, nil
}

// Close implements Store.
func (d *DiskStore) Close() error { return nil }

type diskIndex struct {
	// id echo — the staleness validator. For read-only entries this is the
	// content-version identity (size+mtime at open); writable entries carry 0/0.
	Size     int64 `json:"size"`
	MTimeNs  int64 `json:"mtime_ns"`
	Writable bool  `json:"writable,omitempty"`
	// backing state
	CurSize   int64    `json:"cur_size"`
	ChunkSize int64    `json:"chunk_size"`
	Bitmap    []byte   `json:"bitmap"`               // per-CHUNK presence (fully cached)
	SubBitmap []byte   `json:"sub_bitmap,omitempty"` // per-SUB-BLOCK presence (demand-fill)
	Hashes    []uint64 `json:"hashes,omitempty"`     // parallel to chunk index; 0 = unknown
	// writable open-time validation hint (caller size+mtime at last clean sync)
	HintSet     bool  `json:"hint_set,omitempty"`
	HintSize    int64 `json:"hint_size,omitempty"`
	HintMTimeNs int64 `json:"hint_mtime_ns,omitempty"`
}

func (d *DiskStore) shardDir(key string) string { return filepath.Join(d.root, key[:2]) }
func (d *DiskStore) dataPath(key string) string { return filepath.Join(d.shardDir(key), key+".data") }
func (d *DiskStore) idxPath(key string) string  { return filepath.Join(d.shardDir(key), key+".idx") }

func numChunks(size, chunkSize int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + chunkSize - 1) / chunkSize)
}

func bitSet(bm []byte, i int) bool {
	byteIdx := i / 8
	if byteIdx >= len(bm) {
		return false
	}
	return bm[byteIdx]&(1<<uint(i%8)) != 0
}

func setBit(bm []byte, i int) { bm[i/8] |= 1 << uint(i%8) }

func clearBit(bm []byte, i int) {
	if i/8 < len(bm) {
		bm[i/8] &^= 1 << uint(i%8)
	}
}

// grow extends the bitmap and hash arrays so index idx is addressable.
func (di *diskIndex) grow(idx int) {
	if idx/8 >= len(di.Bitmap) {
		grown := make([]byte, idx/8+1)
		copy(grown, di.Bitmap)
		di.Bitmap = grown
	}
	if idx >= len(di.Hashes) {
		grown := make([]uint64, idx+1)
		copy(grown, di.Hashes)
		di.Hashes = grown
	}
}

func (di *diskIndex) hashAt(idx int) uint64 {
	if idx < len(di.Hashes) {
		return di.Hashes[idx]
	}
	return 0
}

func (di *diskIndex) setHash(idx int, h uint64) {
	if idx < len(di.Hashes) {
		di.Hashes[idx] = h
	}
}

// matches reports whether a loaded index is the entry for id at this store's
// chunk size (else it is stale and must be discarded and rebuilt). Writable
// entries have no size/mtime validator — their identity is the stable bucket key
// and content correctness comes from per-block hashes.
func (d *DiskStore) matches(di diskIndex, id FileID) bool {
	if di.ChunkSize != d.chunkSize || di.Writable != id.Writable {
		return false
	}
	if id.Writable {
		return true
	}
	return di.Size == id.Size && di.MTimeNs == id.MTimeNs
}

// Get implements Store.
func (d *DiskStore) Get(id FileID, idx int, buf []byte) (int, bool, error) {
	key := id.Key()
	rw := d.locks.get(key)
	rw.RLock()
	defer rw.RUnlock()

	idxData, ok, err := d.readIndex(key)
	if err != nil || !ok {
		return 0, false, err
	}
	if !d.matches(idxData, id) {
		return 0, false, nil // stale; treated as a miss, rebuilt on the next Put
	}
	if !bitSet(idxData.Bitmap, idx) {
		return 0, false, nil
	}
	f, err := os.Open(d.dataPath(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	defer f.Close()
	// buf is sized to the chunk's valid length, so a populated chunk fills it
	// exactly. A short read means the backing file is inconsistent with the
	// bitmap, so treat it as a miss and refetch.
	n, _ := f.ReadAt(buf, int64(idx)*d.chunkSize)
	if n == len(buf) {
		return n, true, nil
	}
	return 0, false, nil
}

// Put implements Store: a read-only miss-fill of a full chunk, computing and
// recording its content hash.
func (d *DiskStore) Put(id FileID, idx int, data []byte) error {
	return d.putChunk(id, idx, data, hashChunk(data))
}

// PutHashed implements Store: fill chunk idx with data whose hash is already
// known (from the caller's RBLOCK).
func (d *DiskStore) PutHashed(id FileID, idx int, data []byte, hash uint64) error {
	return d.putChunk(id, idx, data, hash)
}

// putChunk stores a FULL chunk (fresh-or-append), fsync-before-presence-bit.
func (d *DiskStore) putChunk(id FileID, idx int, data []byte, hash uint64) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()

	idxData, err := d.loadOrFresh(key, id, int64(idx)*d.chunkSize+int64(len(data)))
	if err != nil {
		return err
	}
	if err := d.writeData(key, int64(idx)*d.chunkSize, data); err != nil {
		return err
	}
	idxData.grow(idx)
	setBit(idxData.Bitmap, idx)
	d.setChunkSubBits(&idxData, idx)
	idxData.setHash(idx, hash)
	return d.writeIndex(key, idxData)
}

func (d *DiskStore) subsPerChunk() int { return int((d.chunkSize + SubBlockSize - 1) / SubBlockSize) }

// ensureSubBitmap grows di.SubBitmap so global sub-block index sub is addressable.
func ensureSubBitmap(di *diskIndex, sub int) {
	if need := sub/8 + 1; need > len(di.SubBitmap) {
		grown := make([]byte, need)
		copy(grown, di.SubBitmap)
		di.SubBitmap = grown
	}
}

// setChunkSubBits marks every sub-block of chunk idx present (a whole-chunk fill).
func (d *DiskStore) setChunkSubBits(di *diskIndex, idx int) {
	first := int(int64(idx) * d.chunkSize / SubBlockSize)
	last := int((int64(idx+1)*d.chunkSize - 1) / SubBlockSize)
	ensureSubBitmap(di, last)
	for s := first; s <= last; s++ {
		setBit(di.SubBitmap, s)
	}
}

// clearChunkSubBits clears every sub-block of chunk idx (a drop/invalidation).
func (d *DiskStore) clearChunkSubBits(di *diskIndex, idx int) {
	first := int(int64(idx) * d.chunkSize / SubBlockSize)
	last := int((int64(idx+1)*d.chunkSize - 1) / SubBlockSize)
	for s := first; s <= last; s++ {
		clearBit(di.SubBitmap, s)
	}
}

// GetSub implements Store: serve [subOff, subOff+len(buf)) of chunk idx if every
// covering sub-block is present (either the whole chunk, or demand-filled).
func (d *DiskStore) GetSub(id FileID, idx int, subOff int64, buf []byte) (int, bool, error) {
	key := id.Key()
	rw := d.locks.get(key)
	rw.RLock()
	defer rw.RUnlock()
	di, ok, err := d.readIndex(key)
	if err != nil || !ok || !d.matches(di, id) {
		return 0, false, err
	}
	absOff := int64(idx)*d.chunkSize + subOff
	first, last := subSpan(absOff, int64(len(buf)))
	for s := first; s <= last; s++ {
		if !bitSet(di.SubBitmap, s) && !bitSet(di.Bitmap, int(int64(s)*SubBlockSize/d.chunkSize)) {
			return 0, false, nil
		}
	}
	if err := d.readData(key, absOff, buf); err != nil {
		return 0, false, nil
	}
	return len(buf), true, nil
}

// PutSub implements Store: fill [subOff, subOff+len(data)) of chunk idx and mark
// its sub-blocks present (the demand-fill read-fill path).
func (d *DiskStore) PutSub(id FileID, idx int, subOff int64, data []byte) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()
	absOff := int64(idx)*d.chunkSize + subOff
	di, err := d.loadOrFresh(key, id, absOff+int64(len(data)))
	if err != nil {
		return err
	}
	if err := d.writeData(key, absOff, data); err != nil {
		return err
	}
	first, last := subSpan(absOff, int64(len(data)))
	ensureSubBitmap(&di, last)
	for s := first; s <= last; s++ {
		setBit(di.SubBitmap, s)
	}
	return d.writeIndex(key, di)
}

// WriteChunk implements Store.
func (d *DiskStore) WriteChunk(id FileID, idx int, chunkOff int64, data []byte, chunkLen int64, callerHash uint64) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()

	full := chunkOff == 0 && int64(len(data)) == chunkLen
	present := false
	if di, ok, err := d.readIndex(key); err == nil && ok && d.matches(di, id) {
		present = bitSet(di.Bitmap, idx)
	}
	// Partial write to an absent chunk: leave absent (the caller is already
	// authoritative post-write-through; the next read miss-fills the whole chunk).
	if !full && !present {
		return nil
	}

	idxData, err := d.loadOrFresh(key, id, int64(idx)*d.chunkSize+chunkOff+int64(len(data)))
	if err != nil {
		return err
	}

	if full {
		if err := d.writeData(key, int64(idx)*d.chunkSize, data); err != nil {
			return err
		}
		idxData.grow(idx)
		setBit(idxData.Bitmap, idx)
		d.setChunkSubBits(&idxData, idx)
		idxData.setHash(idx, callerHash)
		return d.writeIndex(key, idxData)
	}

	// Partial write to a present chunk: read-modify-write, then self-verify the
	// resulting chunk against the caller's authoritative hash.
	cur := make([]byte, chunkLen)
	if err := d.readData(key, int64(idx)*d.chunkSize, cur); err != nil {
		return err
	}
	copy(cur[chunkOff:], data)
	if err := d.writeData(key, int64(idx)*d.chunkSize+chunkOff, data); err != nil {
		return err
	}
	idxData.grow(idx)
	if hashChunk(cur) == callerHash {
		setBit(idxData.Bitmap, idx)
		d.setChunkSubBits(&idxData, idx)
		idxData.setHash(idx, callerHash)
	} else {
		// The server's base diverged from the caller's file: drop rather than
		// store bytes we cannot vouch for. The next read miss-fills correctly.
		clearBit(idxData.Bitmap, idx)
		d.clearChunkSubBits(&idxData, idx)
		idxData.setHash(idx, 0)
	}
	return d.writeIndex(key, idxData)
}

// WriteThrough implements Store: apply data in place WITHOUT computing/verifying a
// content hash (the deferred / sub-block-hash write path). It marks the chunk
// present with an UNKNOWN (0) hash, to be reconciled later via HashRange against
// the caller's authoritative hash.
func (d *DiskStore) WriteThrough(id FileID, idx int, chunkOff int64, data []byte, chunkLen int64) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()

	full := chunkOff == 0 && int64(len(data)) == chunkLen
	absFirst, absLast := subSpan(int64(idx)*d.chunkSize+chunkOff, int64(len(data)))
	cached := false
	if di, ok, err := d.readIndex(key); err == nil && ok && d.matches(di, id) {
		if bitSet(di.Bitmap, idx) {
			cached = true
		} else {
			for s := absFirst; s <= absLast; s++ {
				if bitSet(di.SubBitmap, s) {
					cached = true
					break
				}
			}
		}
	}
	// Write to a fully-uncached region: leave absent (next read miss-fills it).
	if !full && !cached {
		return nil
	}
	idxData, err := d.loadOrFresh(key, id, int64(idx)*d.chunkSize+chunkOff+int64(len(data)))
	if err != nil {
		return err
	}
	// Write only the touched sub-range in place — no read-modify-write, no hash.
	if err := d.writeData(key, int64(idx)*d.chunkSize+chunkOff, data); err != nil {
		return err
	}
	idxData.grow(idx)
	if full {
		setBit(idxData.Bitmap, idx)
		d.setChunkSubBits(&idxData, idx)
	} else {
		// The chunk may be only partially cached (demand-filled): update just the
		// written sub-blocks so a later read of them serves the new bytes.
		ensureSubBitmap(&idxData, absLast)
		for s := absFirst; s <= absLast; s++ {
			setBit(idxData.SubBitmap, s)
		}
	}
	idxData.setHash(idx, 0) // unknown until reconciled via HashRange
	return d.writeIndex(key, idxData)
}

// HashRange implements Store.
func (d *DiskStore) HashRange(id FileID, idx int, off, length int64) (uint64, bool, error) {
	key := id.Key()
	rw := d.locks.get(key)
	rw.RLock()
	defer rw.RUnlock()
	di, ok, err := d.readIndex(key)
	if err != nil || !ok || !d.matches(di, id) {
		return 0, false, err
	}
	// Present iff every covering sub-block is set (or the whole chunk is).
	absFirst, absLast := subSpan(int64(idx)*d.chunkSize+off, length)
	for s := absFirst; s <= absLast; s++ {
		if !bitSet(di.SubBitmap, s) && !bitSet(di.Bitmap, idx) {
			return 0, false, nil
		}
	}
	validLen := d.chunkSize
	if rem := di.CurSize - int64(idx)*d.chunkSize; rem < validLen {
		validLen = rem
	}
	if off < 0 || length < 0 || off+length > validLen {
		return 0, false, nil
	}
	buf := make([]byte, length)
	if err := d.readData(key, int64(idx)*d.chunkSize+off, buf); err != nil {
		return 0, false, err
	}
	return hashChunk(buf), true, nil
}

// ChunkHash implements Store.
func (d *DiskStore) ChunkHash(id FileID, idx int) (uint64, bool, error) {
	key := id.Key()
	rw := d.locks.get(key)
	rw.RLock()
	defer rw.RUnlock()
	di, ok, err := d.readIndex(key)
	if err != nil || !ok || !d.matches(di, id) || !bitSet(di.Bitmap, idx) {
		return 0, false, err
	}
	return di.hashAt(idx), true, nil
}

// Drop implements Store.
func (d *DiskStore) Drop(id FileID, idx int) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()
	di, ok, err := d.readIndex(key)
	if err != nil || !ok || !d.matches(di, id) {
		return err
	}
	clearBit(di.Bitmap, idx)
	d.clearChunkSubBits(&di, idx)
	di.setHash(idx, 0)
	return d.writeIndex(key, di)
}

// Resize implements Store.
func (d *DiskStore) Resize(id FileID, newSize int64) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()
	if err := os.MkdirAll(d.shardDir(key), 0o755); err != nil {
		return err
	}
	di, ok, err := d.readIndex(key)
	if err != nil {
		return err
	}
	if !ok || !d.matches(di, id) {
		// Fresh writable entry sized to newSize (no chunks yet).
		di = d.freshIndex(id, newSize)
		if err := d.truncateData(key, newSize); err != nil {
			return err
		}
		return d.writeIndex(key, di)
	}
	oldSize := di.CurSize
	// Drop every chunk at or beyond the new EOF chunk, plus the old straddling
	// final chunk (its valid length changed): both boundaries are unsafe to keep.
	dropFrom := int(newSize / d.chunkSize)
	for i := dropFrom; i < len(di.Bitmap)*8; i++ { // len*8 = number of bit positions
		clearBit(di.Bitmap, i)
		d.clearChunkSubBits(&di, i)
		di.setHash(i, 0)
	}
	if oldSize > 0 {
		oldStraddle := int((oldSize - 1) / d.chunkSize)
		clearBit(di.Bitmap, oldStraddle)
		d.clearChunkSubBits(&di, oldStraddle)
		di.setHash(oldStraddle, 0)
	}
	di.CurSize = newSize
	n := numChunks(newSize, d.chunkSize)
	if (n+7)/8 < len(di.Bitmap) {
		di.Bitmap = di.Bitmap[:(n+7)/8]
	}
	if n < len(di.Hashes) {
		di.Hashes = di.Hashes[:n]
	}
	if err := d.truncateData(key, newSize); err != nil {
		return err
	}
	return d.writeIndex(key, di)
}

// Invalidate implements Store.
func (d *DiskStore) Invalidate(id FileID) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()
	_ = os.Remove(d.dataPath(key))
	_ = os.Remove(d.idxPath(key))
	return nil
}

// Rename implements Store: move a bucket, taking the two key locks in sorted
// order so A->B racing B->A cannot deadlock.
func (d *DiskStore) Rename(oldID, newID FileID) error {
	oldKey, newKey := oldID.Key(), newID.Key()
	if oldKey == newKey {
		return nil
	}
	l1, l2 := d.locks.get(oldKey), d.locks.get(newKey)
	if oldKey > newKey {
		l1, l2 = l2, l1
	}
	l1.Lock()
	defer l1.Unlock()
	if l2 != l1 {
		l2.Lock()
		defer l2.Unlock()
	}
	if _, err := os.Stat(d.dataPath(oldKey)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing cached under the old path
		}
		return err
	}
	if err := os.MkdirAll(d.shardDir(newKey), 0o755); err != nil {
		return err
	}
	// Overwrite any existing destination (rename-replace).
	_ = os.Remove(d.dataPath(newKey))
	_ = os.Remove(d.idxPath(newKey))
	if err := os.Rename(d.dataPath(oldKey), d.dataPath(newKey)); err != nil {
		return err
	}
	if err := os.Rename(d.idxPath(oldKey), d.idxPath(newKey)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Hint implements Store.
func (d *DiskStore) Hint(id FileID) (int64, int64, bool, error) {
	key := id.Key()
	rw := d.locks.get(key)
	rw.RLock()
	defer rw.RUnlock()
	di, ok, err := d.readIndex(key)
	if err != nil || !ok || !d.matches(di, id) || !di.HintSet {
		return 0, 0, false, err
	}
	return di.HintSize, di.HintMTimeNs, true, nil
}

// SetHint implements Store.
func (d *DiskStore) SetHint(id FileID, size, mtimeNs int64) error {
	key := id.Key()
	rw := d.locks.get(key)
	rw.Lock()
	defer rw.Unlock()
	if err := os.MkdirAll(d.shardDir(key), 0o755); err != nil {
		return err
	}
	di, ok, err := d.readIndex(key)
	if err != nil {
		return err
	}
	if !ok || !d.matches(di, id) {
		di = d.freshIndex(id, 0)
		if err := d.truncateData(key, 0); err != nil {
			return err
		}
	}
	di.HintSet = true
	di.HintSize = size
	di.HintMTimeNs = mtimeNs
	return d.writeIndex(key, di)
}

// loadOrFresh returns the current index for key/id, or a fresh one (discarding a
// stale pair) sized to at least minBacking.
func (d *DiskStore) loadOrFresh(key string, id FileID, minBacking int64) (diskIndex, error) {
	if err := os.MkdirAll(d.shardDir(key), 0o755); err != nil {
		return diskIndex{}, err
	}
	di, ok, err := d.readIndex(key)
	if err != nil {
		return diskIndex{}, err
	}
	if !ok || !d.matches(di, id) {
		_ = os.Remove(d.dataPath(key))
		_ = os.Remove(d.idxPath(key))
		backing := minBacking
		if !id.Writable && id.Size > backing {
			backing = id.Size
		}
		di = d.freshIndex(id, backing)
		if err := d.truncateData(key, backing); err != nil {
			return diskIndex{}, err
		}
		return di, nil
	}
	if minBacking > di.CurSize {
		di.CurSize = minBacking
		if err := d.truncateData(key, minBacking); err != nil {
			return diskIndex{}, err
		}
	}
	return di, nil
}

func (d *DiskStore) freshIndex(id FileID, backing int64) diskIndex {
	n := numChunks(backing, d.chunkSize)
	// Writable entries store a zeroed id echo (their identity is the stable key).
	size, mtime := id.Size, id.MTimeNs
	if id.Writable {
		size, mtime = 0, 0
	}
	return diskIndex{
		Size:      size,
		MTimeNs:   mtime,
		Writable:  id.Writable,
		CurSize:   backing,
		ChunkSize: d.chunkSize,
		Bitmap:    make([]byte, (n+7)/8),
		Hashes:    make([]uint64, n),
	}
}

// writeData writes data at absolute offset off in the backing file and fsyncs it,
// so a torn data write can never precede a set presence bit.
func (d *DiskStore) writeData(key string, off int64, data []byte) error {
	f, err := os.OpenFile(d.dataPath(key), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(data, off); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (d *DiskStore) readData(key string, off int64, buf []byte) error {
	f, err := os.Open(d.dataPath(key))
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := f.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	return err
}

func (d *DiskStore) truncateData(key string, size int64) error {
	f, err := os.OpenFile(d.dataPath(key), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

// readIndex loads the sidecar; ok is false when it does not exist yet.
func (d *DiskStore) readIndex(key string) (diskIndex, bool, error) {
	b, err := os.ReadFile(d.idxPath(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return diskIndex{}, false, nil
		}
		return diskIndex{}, false, err
	}
	var idx diskIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		// Corrupt sidecar: treat as absent so it is rebuilt.
		return diskIndex{}, false, nil
	}
	return idx, true, nil
}

// writeIndex writes the sidecar atomically (temp file + rename).
func (d *DiskStore) writeIndex(key string, idx diskIndex) error {
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.shardDir(key), key+".idx.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, d.idxPath(key))
}

// stripedRW is a fixed set of RWMutexes indexed by a hash of the file key, so
// per-file locking uses bounded memory (unrelated files may occasionally share a
// stripe, which only serializes them briefly).
type stripedRW [256]sync.RWMutex

func (s *stripedRW) get(key string) *sync.RWMutex {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &s[h.Sum32()%uint32(len(s))]
}
