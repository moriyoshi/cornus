// Package blockcache implements a server-side, per-file, fixed-chunk block cache
// for the on-demand remote file reads cornus performs over 9P (see pkg/wire).
//
// A file read once — a chunk at a time — is served locally on subsequent reads,
// across reads, mounts, and (with the on-disk Store) server restarts. The cache
// is content-validated by a per-file identity (mount, path, size, mtime): any
// change to the origin file yields a new FileID, so stale chunks are never
// served and simply age out via Prune.
//
// The Store backend is pluggable; the default is the on-disk sparse-file store
// (NewDiskStore). MemStore is an in-memory backend for tests.
package blockcache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"

	"golang.org/x/sync/singleflight"
)

// FileID identifies a cache entry. It has two identity policies, selected by
// Writable, sharing one Key()/on-disk format:
//
//   - Read-only (Writable=false): a CONTENT-VERSION identity. Size and MTimeNs are
//     populated and part of the key, so any change to the origin file yields a new
//     key and old chunks are never reused (a changed file is simply a new entry).
//     This is the immutable-context / build-context model.
//   - Writable (Writable=true): a STABLE-BUCKET identity for block-protocol
//     (writable-async) mounts. Size and MTimeNs are left ZERO (not in the key), so
//     the bucket (Mount, Path) is stable for the life of the path and a file's
//     blocks are shared across its versions. Validity is judged PER BLOCK by the
//     stored xxh3 hash (plus a session write sequence and an open-time size+mtime
//     hint), not by the whole-file key. Mount must be deployment-scoped so distinct
//     workloads never share a bucket.
type FileID struct {
	Mount    string // logical mount scope (deployment-scoped for writable buckets)
	Path     string // slash-separated path from the export root
	Size     int64  // read-only: file size at open time; writable: 0 (not identity)
	MTimeNs  int64  // read-only: mtime ns at open time; writable: 0 (not identity)
	Writable bool   // selects the stable-bucket identity policy
}

// Key returns a stable hex digest identifying this entry. It is the on-disk file
// name and the singleflight namespace. All fields are hashed unconditionally;
// writable entries simply carry Size=MTimeNs=0, which makes the key stable across
// versions of the same path.
func (id FileID) Key() string {
	// Writable buckets ignore size/mtime (block-level validity), so normalize them
	// to 0 here — the bucket is stable across versions regardless of what a caller
	// leaves in those fields.
	size, mtime := id.Size, id.MTimeNs
	if id.Writable {
		size, mtime = 0, 0
	}
	h := sha256.New()
	// A NUL separator that cannot appear in any field disambiguates the parts.
	io.WriteString(h, id.Mount)
	h.Write([]byte{0})
	io.WriteString(h, id.Path)
	h.Write([]byte{0})
	io.WriteString(h, strconv.FormatInt(size, 10))
	h.Write([]byte{0})
	io.WriteString(h, strconv.FormatInt(mtime, 10))
	h.Write([]byte{0})
	if id.Writable {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Store is the pluggable cache backend. Implementations must be safe for
// concurrent use. Get fills buf with the cached chunk and reports whether it was
// present; a present chunk always fills exactly its valid length (chunkSize, or
// less for the final chunk of a file). Put stores a chunk's bytes. Cache errors
// are advisory: the Cache treats a Store failure as a miss and still serves the
// read from the origin.
type Store interface {
	// Get fills buf with chunk index idx of the file identified by id. It
	// returns the number of bytes written, whether the chunk was present, and
	// any error. buf's length is the chunk's valid byte length. A chunk is
	// present only if ALL its sub-blocks are present (see GetSub).
	Get(id FileID, idx int, buf []byte) (n int, ok bool, err error)
	// GetSub fills buf with bytes [subOff, subOff+len(buf)) of chunk idx, and
	// reports present=true only if every sub-block covering that range is
	// present. It is the demand-fill (FeatSubBlockFill) read-serve path: a chunk
	// may be partially cached (some sub-blocks filled by earlier ranged reads or
	// writes), and only the requested sub-range needs to be present.
	GetSub(id FileID, idx int, subOff int64, buf []byte) (n int, ok bool, err error)
	// PutSub stores data at intra-chunk offset subOff of chunk idx, marking the
	// sub-blocks it fully covers present. It is the demand-fill read-fill path
	// (data comes from a ranged RBLOCK). subOff and len(data) are expected to be
	// sub-block aligned (the final sub-block of a file may be short).
	PutSub(id FileID, idx int, subOff int64, data []byte) error
	// Put stores data as chunk index idx of the file identified by id, computing
	// and recording its content hash. len(data) is the chunk's valid byte length.
	// This is the read-only miss-fill path.
	Put(id FileID, idx int, data []byte) error
	// Close releases any resources held by the store.
	Close() error

	// --- Writable / block-protocol extensions ---
	// All operate atomically under the entry's per-key lock. They are only used
	// for writable (block-protocol) entries; a writable entry's backing length is
	// tracked internally (via Resize / grow-on-write), independent of id.Size.

	// PutHashed fills chunk idx with data whose content hash is already known
	// (from the caller's RBLOCK), avoiding recomputation.
	PutHashed(id FileID, idx int, data []byte, hash uint64) error
	// WriteChunk applies data at intra-chunk offset chunkOff of chunk idx (valid
	// length chunkLen), recording callerHash as the chunk's authoritative hash:
	//   - full-chunk cover        -> overwrite, mark present, store callerHash
	//   - partial write, present  -> read-modify-write; if the resulting local
	//                                hash != callerHash the base diverged, so DROP
	//                                the chunk (never store wrong bytes)
	//   - partial write, absent   -> no-op (next read miss-fills the whole chunk)
	// It grows the backing/bitmap as needed for a writable entry.
	WriteChunk(id FileID, idx int, chunkOff int64, data []byte, chunkLen int64, callerHash uint64) error
	// ChunkHash returns the stored content hash for a present chunk.
	ChunkHash(id FileID, idx int) (hash uint64, present bool, err error)
	// WriteThrough applies data at intra-chunk offset chunkOff of chunk idx (valid
	// length chunkLen) WITHOUT computing or verifying a content hash: it RMWs a
	// present chunk (or stores a full-cover write) in place and marks it present.
	// It is the deferred / coherence-decoupled write path used by the block
	// proxy's featDeferHash / featSubBlockHash modes: the authoritative hash is
	// reconciled later — per touched sub-block, or in a batch at fsync — via
	// HashRange, not on every write. Absent + partial => no-op (next read
	// miss-fills the whole chunk), matching WriteChunk.
	WriteThrough(id FileID, idx int, chunkOff int64, data []byte, chunkLen int64) error
	// HashRange returns the xxh3 hash of bytes [off, off+length) within present
	// chunk idx (present=false if the chunk is absent or the range exceeds its
	// stored length). It hashes in place — no chunk-sized copy — so a sub-block
	// coherence check costs only the sub-block. Used to reconcile a WriteThrough'd
	// chunk against the caller's authoritative hash.
	HashRange(id FileID, idx int, off, length int64) (hash uint64, present bool, err error)
	// Drop clears presence + hash for chunk idx (a coherence invalidation).
	Drop(id FileID, idx int) error
	// Resize sets a writable entry's logical length to newSize: it drops chunks
	// wholly beyond newSize and the chunk straddling the OLD size (its valid
	// length changed), and resizes the backing.
	Resize(id FileID, newSize int64) error
	// Invalidate drops the entire entry (all chunks + metadata).
	Invalidate(id FileID) error
	// Rename moves a regular-file bucket from oldID to newID, keeping its warm
	// blocks (the safe-write rename-replace pattern).
	Rename(oldID, newID FileID) error
	// Hint returns the size+mtime recorded at the last clean sync of a writable
	// entry (the open-time fast-path validation hint); ok is false if none.
	Hint(id FileID) (size, mtimeNs int64, ok bool, err error)
	// SetHint records the caller's current size+mtime as this entry's clean hint.
	SetHint(id FileID, size, mtimeNs int64) error
}

// Fetcher reads from the origin at absolute file offset off into buf, like
// io.ReaderAt. It is only called on a cache miss. The Cache always calls it with
// chunk-aligned offsets and lengths.
type Fetcher func(off int64, buf []byte) (int, error)

// Cache serves ReadAt from a Store, filling misses from a Fetcher under
// per-(file, chunk) singleflight so concurrent readers of the same chunk trigger
// a single origin fetch.
type Cache struct {
	store     Store
	chunkSize int64
	sf        singleflight.Group
}

// DefaultChunkSize matches the kernel 9p mount msize (1 MiB) so one kernel Tread
// maps to one chunk maps to (on a miss) one origin fetch.
const DefaultChunkSize int64 = 1 << 20

// SubBlockSize is the demand-fill (FeatSubBlockFill) sub-block granularity: the
// unit at which a chunk's presence is tracked and a ranged read miss is filled. It
// matches the block protocol's subBlockSize and a common DB page size, and divides
// DefaultChunkSize evenly.
const SubBlockSize int64 = 4096

// subSpan returns the inclusive sub-block index range [first, last] covering the
// byte range [off, off+length).
func subSpan(off, length int64) (first, last int) {
	if length <= 0 {
		return int(off / SubBlockSize), int(off / SubBlockSize)
	}
	return int(off / SubBlockSize), int((off + length - 1) / SubBlockSize)
}

func bmWords(nSub int) int { return (nSub + 63) / 64 }

func bmSet(bm []uint64, i int) {
	if w := i / 64; w < len(bm) {
		bm[w] |= 1 << uint(i%64)
	}
}

func bmGet(bm []uint64, i int) bool {
	w := i / 64
	return i >= 0 && w < len(bm) && bm[w]&(1<<uint(i%64)) != 0
}

// bmRangeAllSet reports whether every bit in [first, last] is set.
func bmRangeAllSet(bm []uint64, first, last int) bool {
	for i := first; i <= last; i++ {
		if !bmGet(bm, i) {
			return false
		}
	}
	return true
}

// New returns a Cache over store using the given chunk size (falling back to
// DefaultChunkSize when chunkSize <= 0).
func New(store Store, chunkSize int64) *Cache {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	return &Cache{store: store, chunkSize: chunkSize}
}

// ChunkSize returns the cache's fixed chunk size in bytes.
func (c *Cache) ChunkSize() int64 { return c.chunkSize }

// Store returns the underlying Store. The writable block proxy uses it directly
// for the write/coherence ops (WriteChunk/PutHashed/Resize/…), which have
// different fill semantics (inline hash + session seq gating) than the read-only
// miss-fill ReadAt path.
func (c *Cache) Store() Store { return c.store }

// Close releases the underlying store.
func (c *Cache) Close() error {
	if c.store == nil {
		return nil
	}
	return c.store.Close()
}

// ReadAt fills p with bytes at offset off from the file identified by id (whose
// total length is size), reading from the cache and filling any missing chunks
// via fetch. It may span multiple chunks in one call. It returns io.EOF (with a
// possibly-nonzero count) when the read reaches end of file, matching the
// io.ReaderAt / p9 ReadAt contract.
func (c *Cache) ReadAt(id FileID, size int64, fetch Fetcher, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, errors.New("blockcache: negative offset")
	}
	if off >= size {
		return 0, io.EOF
	}
	end := off + int64(len(p))
	if end > size {
		end = size
	}
	total := 0
	for pos := off; pos < end; {
		idx := int(pos / c.chunkSize)
		chunkStart := int64(idx) * c.chunkSize
		chunkLen := c.chunkSize
		if chunkStart+chunkLen > size {
			chunkLen = size - chunkStart
		}
		data, err := c.chunk(id, idx, chunkLen, fetch)
		if err != nil {
			if total > 0 {
				return total, nil
			}
			return 0, err
		}
		inChunk := pos - chunkStart
		if inChunk >= int64(len(data)) {
			// Short chunk (origin returned fewer bytes than size promised):
			// treat as EOF rather than spin.
			break
		}
		n := copy(p[total:end-off], data[inChunk:])
		total += n
		pos += int64(n)
		if n == 0 {
			break
		}
	}
	if total < len(p) {
		// We clamped at size, so a short fill means end of file was reached.
		return total, io.EOF
	}
	return total, nil
}

// chunk returns the bytes of chunk idx (of valid length chunkLen), consulting the
// store first and fetching from the origin on a miss. Concurrent callers for the
// same (id, idx) collapse to a single fetch via singleflight.
func (c *Cache) chunk(id FileID, idx int, chunkLen int64, fetch Fetcher) ([]byte, error) {
	if buf, ok := c.storeGet(id, idx, chunkLen); ok {
		return buf, nil
	}
	key := id.Key() + "/" + strconv.Itoa(idx)
	v, err, _ := c.sf.Do(key, func() (any, error) {
		// Another goroutine may have filled the chunk while we waited.
		if buf, ok := c.storeGet(id, idx, chunkLen); ok {
			return buf, nil
		}
		buf := make([]byte, chunkLen)
		n, ferr := fillFull(fetch, int64(idx)*c.chunkSize, buf)
		if ferr != nil && !(ferr == io.EOF && int64(n) == chunkLen) {
			return nil, ferr
		}
		buf = buf[:n]
		// Best-effort store; a cache write failure must not fail the read.
		_ = c.store.Put(id, idx, buf)
		return buf, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// storeGet returns the cached chunk when present and completely filled.
func (c *Cache) storeGet(id FileID, idx int, chunkLen int64) ([]byte, bool) {
	buf := make([]byte, chunkLen)
	n, ok, err := c.store.Get(id, idx, buf)
	if err != nil || !ok || int64(n) != chunkLen {
		return nil, false
	}
	return buf, true
}

// fillFull reads into buf starting at absolute file offset base, looping until
// buf is full or the origin signals end of file. It returns the number of bytes
// filled and io.EOF if the origin ended before buf was full.
func fillFull(fetch Fetcher, base int64, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := fetch(base+int64(total), buf[total:])
		total += n
		if err != nil {
			if err == io.EOF {
				return total, io.EOF
			}
			return total, err
		}
		if n == 0 {
			return total, io.EOF
		}
	}
	return total, nil
}
