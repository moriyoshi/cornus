package blockcache

import "sync"

// MemStore is an in-memory Store backend. It is intended for tests and for
// deployments that do not want on-disk persistence. It is safe for concurrent
// use and unbounded, so callers that need eviction should prefer the on-disk
// store with Prune.
type MemStore struct {
	mu        sync.RWMutex
	chunkSize int64
	files     map[string]*memEntry // key = FileID.Key()
}

type memEntry struct {
	curSize int64
	chunks  map[int][]byte
	// present holds a sub-block presence bitmap for PARTIALLY cached chunks
	// (demand-filled via PutSub). A chunk in chunks with NO present entry is fully
	// present (a whole-chunk fill/write); with a present entry, only the set
	// sub-blocks are valid.
	present             map[int][]uint64
	hashes              map[int]uint64
	hintSet             bool
	hintSize, hintMTime int64
}

func newMemEntry() *memEntry {
	return &memEntry{chunks: map[int][]byte{}, present: map[int][]uint64{}, hashes: map[int]uint64{}}
}

func hasChunk(e *memEntry, idx int) bool { _, ok := e.chunks[idx]; return ok }

// rangePresent reports whether [off, off+length) of chunk idx is fully cached. A
// present chunk with no bitmap is fully present; otherwise every covering
// sub-block must be set.
func (e *memEntry) rangePresent(idx int, off, length int64) bool {
	if _, ok := e.chunks[idx]; !ok {
		return false
	}
	bm := e.present[idx]
	if bm == nil {
		return true
	}
	first, last := subSpan(off, length)
	return bmRangeAllSet(bm, first, last)
}

func (m *MemStore) subsPerChunk() int { return int((m.chunkSize + SubBlockSize - 1) / SubBlockSize) }

// NewMemStore returns an empty in-memory store using the given chunk size
// (falling back to DefaultChunkSize when <= 0). The chunk size must match the
// Cache the store is wrapped in, so the writable Resize/size math aligns.
func NewMemStore(chunkSize int64) *MemStore {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	return &MemStore{chunkSize: chunkSize, files: map[string]*memEntry{}}
}

// entry returns the file's entry, creating it if create is true. Callers hold mu.
func (m *MemStore) entry(id FileID, create bool) *memEntry {
	key := id.Key()
	e := m.files[key]
	if e == nil && create {
		e = newMemEntry()
		m.files[key] = e
	}
	return e
}

// Get implements Store. A partially cached (demand-filled) chunk is a miss for a
// whole-chunk read — every sub-block covering buf must be present.
func (m *MemStore) Get(id FileID, idx int, buf []byte) (int, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.files[id.Key()]
	if e == nil {
		return 0, false, nil
	}
	data, ok := e.chunks[idx]
	if !ok {
		return 0, false, nil
	}
	// A partially cached (demand-filled) chunk is a whole-chunk miss until a
	// whole-chunk op promotes it (matches DiskStore, where sub-fills never set the
	// per-chunk bit); the demand-fill read path uses GetSub instead.
	if _, partial := e.present[idx]; partial {
		return 0, false, nil
	}
	n := copy(buf, data)
	return n, true, nil
}

// GetSub implements Store: serve [subOff, subOff+len(buf)) if those sub-blocks are
// present (the demand-fill read path).
func (m *MemStore) GetSub(id FileID, idx int, subOff int64, buf []byte) (int, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.files[id.Key()]
	if e == nil {
		return 0, false, nil
	}
	data, ok := e.chunks[idx]
	if !ok {
		return 0, false, nil
	}
	if !e.rangePresent(idx, subOff, int64(len(buf))) {
		return 0, false, nil
	}
	if subOff+int64(len(buf)) > int64(len(data)) {
		return 0, false, nil
	}
	n := copy(buf, data[subOff:])
	return n, true, nil
}

// PutSub implements Store: fill [subOff, subOff+len(data)) and mark its sub-blocks
// present. A fresh chunk becomes partial (bitmap); an already-full chunk stays
// full (the bytes are authoritative either way).
func (m *MemStore) PutSub(id FileID, idx int, subOff int64, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(id, true)
	need := subOff + int64(len(data))
	buf := e.chunks[idx]
	wasFull := buf != nil && e.present[idx] == nil
	switch {
	case buf == nil:
		buf = make([]byte, need, m.chunkSize)
	case int64(len(buf)) < need:
		if int64(cap(buf)) >= need {
			old := int64(len(buf))
			buf = buf[:need]
			if subOff > old {
				clear(buf[old:subOff])
			}
		} else {
			nb := make([]byte, need, m.chunkSize)
			copy(nb, buf)
			buf = nb
		}
	}
	copy(buf[subOff:], data)
	e.chunks[idx] = buf
	if !wasFull {
		bm := e.present[idx]
		if bm == nil {
			bm = make([]uint64, bmWords(m.subsPerChunk()))
			e.present[idx] = bm
		}
		first, last := subSpan(subOff, int64(len(data)))
		for i := first; i <= last; i++ {
			bmSet(bm, i)
		}
	}
	if need > e.curSize {
		e.curSize = need
	}
	return nil
}

// Put implements Store: read-only fill, computing the chunk hash.
func (m *MemStore) Put(id FileID, idx int, data []byte) error {
	return m.PutHashed(id, idx, data, hashChunk(data))
}

// cloneChunk copies data into a fresh buffer whose capacity is at least the
// chunk size, so a later in-place RMW or growth within the chunk (WriteChunk)
// never reallocates — the growing DB file otherwise reallocates block 0 on every
// small write.
func (m *MemStore) cloneChunk(data []byte) []byte {
	c := int(m.chunkSize)
	if len(data) > c {
		c = len(data)
	}
	b := make([]byte, len(data), c)
	copy(b, data)
	return b
}

// PutHashed implements Store.
func (m *MemStore) PutHashed(id FileID, idx int, data []byte, hash uint64) error {
	cp := m.cloneChunk(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(id, true)
	e.chunks[idx] = cp
	delete(e.present, idx) // a whole-chunk fill is fully present
	e.hashes[idx] = hash
	if end := int64(idx)*m.chunkSize + int64(len(data)); end > e.curSize {
		e.curSize = end
	}
	return nil
}

// WriteChunk implements Store.
func (m *MemStore) WriteChunk(id FileID, idx int, chunkOff int64, data []byte, chunkLen int64, callerHash uint64) error {
	full := chunkOff == 0 && int64(len(data)) == chunkLen
	m.mu.Lock()
	defer m.mu.Unlock()
	// A partial write to an absent chunk is a no-op; don't materialize an entry.
	if e := m.files[id.Key()]; !full && (e == nil || !hasChunk(e, idx)) {
		return nil
	}
	e := m.entry(id, true)
	cur, present := e.chunks[idx]
	if !full && !present {
		return nil // partial write to an absent chunk: leave absent
	}
	if end := int64(idx)*m.chunkSize + chunkOff + int64(len(data)); end > e.curSize {
		e.curSize = end
	}
	if full {
		e.chunks[idx] = m.cloneChunk(data)
		delete(e.present, idx)
		e.hashes[idx] = callerHash
		return nil
	}
	// Partial write to a present chunk: RMW + self-verify, in place. Cached chunks
	// carry capacity >= chunkSize (see cloneChunk), so reslicing cur up to the
	// block's current valid length and overlaying data never reallocates. Growing
	// a block zero-fills the newly exposed region to match the previous
	// out-of-place semantics (a make()-zeroed buffer). Mutating in place is safe
	// because Get returns copies and WriteChunk holds the write lock, so no reader
	// aliases cur's backing array; on a hash mismatch the chunk is dropped, so the
	// mutation is never observed. This kills the per-small-write chunk-sized
	// allocation + copy that dominates a database write workload.
	if int64(cap(cur)) >= chunkLen && chunkOff+int64(len(data)) <= chunkLen {
		oldLen := int64(len(cur))
		cur = cur[:chunkLen]
		if oldLen < chunkLen {
			clear(cur[oldLen:chunkLen])
		}
		copy(cur[chunkOff:], data)
		if hashChunk(cur) == callerHash {
			e.chunks[idx] = cur
			e.hashes[idx] = callerHash
		} else {
			delete(e.chunks, idx)
			delete(e.hashes, idx)
		}
		return nil
	}
	// Defensive fallback (a chunk cached without spare capacity): out-of-place.
	merged := make([]byte, chunkLen, m.chunkSize)
	copy(merged, cur)
	copy(merged[chunkOff:], data)
	if hashChunk(merged) == callerHash {
		e.chunks[idx] = merged
		e.hashes[idx] = callerHash
	} else {
		delete(e.chunks, idx)
		delete(e.hashes, idx)
	}
	return nil
}

// WriteThrough implements Store: splice data in place without hashing, marking
// the chunk present with an unknown (0) hash (reconciled later via HashRange).
func (m *MemStore) WriteThrough(id FileID, idx int, chunkOff int64, data []byte, chunkLen int64) error {
	full := chunkOff == 0 && int64(len(data)) == chunkLen
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.files[id.Key()]; !full && (e == nil || !hasChunk(e, idx)) {
		return nil
	}
	e := m.entry(id, true)
	if end := int64(idx)*m.chunkSize + chunkOff + int64(len(data)); end > e.curSize {
		e.curSize = end
	}
	if full {
		e.chunks[idx] = m.cloneChunk(data)
		delete(e.present, idx)
		e.hashes[idx] = 0
		return nil
	}
	// Partial splice in place. Chunks carry capacity >= chunkSize (cloneChunk), so
	// growing to the written extent and overlaying data never reallocates — the
	// same in-place trick WriteChunk uses, so the deferred/sub-block write path
	// keeps the ~13x allocation win rather than copying the whole chunk per write.
	cur := e.chunks[idx]
	want := chunkOff + int64(len(data))
	if int64(cap(cur)) >= want {
		oldLen := int64(len(cur))
		if want > oldLen {
			cur = cur[:want]
			if chunkOff > oldLen {
				clear(cur[oldLen:chunkOff])
			}
		}
		copy(cur[chunkOff:], data)
		e.chunks[idx] = cur
	} else {
		// Fallback (a chunk cached without spare capacity): grow out of place.
		merged := make([]byte, want, m.chunkSize)
		copy(merged, cur)
		copy(merged[chunkOff:], data)
		e.chunks[idx] = merged
	}
	e.hashes[idx] = 0
	// On a partially cached (demand-filled) chunk, the written sub-blocks are now
	// authoritative and present.
	if bm := e.present[idx]; bm != nil {
		first, last := subSpan(chunkOff, int64(len(data)))
		for i := first; i <= last; i++ {
			bmSet(bm, i)
		}
	}
	return nil
}

// HashRange implements Store.
func (m *MemStore) HashRange(id FileID, idx int, off, length int64) (uint64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.files[id.Key()]
	if e == nil {
		return 0, false, nil
	}
	data, ok := e.chunks[idx]
	if !ok {
		return 0, false, nil
	}
	if off < 0 || length < 0 || off+length > int64(len(data)) {
		return 0, false, nil
	}
	if !e.rangePresent(idx, off, length) {
		return 0, false, nil
	}
	return hashChunk(data[off : off+length]), true, nil
}

// ChunkHash implements Store.
func (m *MemStore) ChunkHash(id FileID, idx int) (uint64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.files[id.Key()]
	if e == nil {
		return 0, false, nil
	}
	if _, ok := e.chunks[idx]; !ok {
		return 0, false, nil
	}
	return e.hashes[idx], true, nil
}

// Drop implements Store.
func (m *MemStore) Drop(id FileID, idx int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.files[id.Key()]; e != nil {
		delete(e.chunks, idx)
		delete(e.present, idx)
		delete(e.hashes, idx)
	}
	return nil
}

// Resize implements Store.
func (m *MemStore) Resize(id FileID, newSize int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(id, true)
	oldSize := e.curSize
	dropFrom := int(newSize / m.chunkSize)
	for idx := range e.chunks {
		if idx >= dropFrom {
			delete(e.chunks, idx)
			delete(e.present, idx)
			delete(e.hashes, idx)
		}
	}
	if oldSize > 0 {
		straddle := int((oldSize - 1) / m.chunkSize)
		delete(e.chunks, straddle)
		delete(e.present, straddle)
		delete(e.hashes, straddle)
	}
	e.curSize = newSize
	return nil
}

// Invalidate implements Store.
func (m *MemStore) Invalidate(id FileID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, id.Key())
	return nil
}

// Rename implements Store.
func (m *MemStore) Rename(oldID, newID FileID) error {
	if oldID.Key() == newID.Key() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.files[oldID.Key()]
	if e == nil {
		return nil
	}
	m.files[newID.Key()] = e
	delete(m.files, oldID.Key())
	return nil
}

// Hint implements Store.
func (m *MemStore) Hint(id FileID) (int64, int64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.files[id.Key()]
	if e == nil || !e.hintSet {
		return 0, 0, false, nil
	}
	return e.hintSize, e.hintMTime, true, nil
}

// SetHint implements Store.
func (m *MemStore) SetHint(id FileID, size, mtimeNs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entry(id, true)
	e.hintSet = true
	e.hintSize = size
	e.hintMTime = mtimeNs
	return nil
}

// Close implements Store.
func (m *MemStore) Close() error { return nil }
