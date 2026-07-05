package wire

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sync/singleflight"

	"cornus/pkg/blockcache"
)

// The server-side block proxy: the cornus server terminates kernel-9p in a
// userspace p9.Server whose File methods speak the block protocol to the caller
// (a blockClient) and keep the server-side block cache coherent. Reads serve from
// the cache (miss-filled over the protocol with inline hash+seq); writes go
// through to the caller and update the cache. It is the writable, cache-coherent
// analog of ServeCachingProxy — for --local-mount SRC:DST:async mounts.

// ServeBlockProxy terminates the kernel-9p session on kernelConn in userspace and
// serves it against the caller over remoteStream using the block protocol, with
// cache as the server-side block cache and mount as the (deployment-scoped) cache
// scope. It blocks until the kernel session ends. The kernel conn is wrapped so a
// kernel-side unmount cancels the mount context, releasing any request parked on
// an unresponsive caller so the p9 server can drain (see cancelConn).
func ServeBlockProxy(kernelConn, remoteStream net.Conn, cache *blockcache.Cache, mount string, opts ...BlockOpt) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	o := resolveBlockOpts(opts)
	chunk := cache.ChunkSize()
	local := helloParams{version: blockProtoVersion, chunkSize: uint32(chunk), maxInflight: blockMaxInflight, features: o.features}
	client, err := newBlockClient(remoteStream, local)
	if err != nil {
		return
	}
	defer client.Close()

	a := &blockAttach{
		ctx:         ctx,
		client:      client,
		cache:       cache,
		store:       cache.Store(),
		mount:       mount,
		chunkSize:   chunk,
		features:    client.features,
		readahead:   o.readahead,
		prefetchSem: make(chan struct{}, blockPrefetchSlots),
		seqTab:      &blockSeqTable{last: map[string]map[int]uint64{}},
		sizes:       &sizeReg{m: map[string]int64{}},
	}
	kc := newCancelConn(kernelConn, cancel)
	_ = p9.NewServer(a).Handle(kc, kc)
}

type blockAttach struct {
	ctx       context.Context
	client    *blockClient
	cache     *blockcache.Cache
	store     blockcache.Store
	mount     string
	chunkSize int64
	features  uint32 // negotiated coherence features (FeatSubBlockHash / FeatDeferHash)
	readahead int64  // demand-fill speculative-prefetch distance cap in bytes (0 = pure demand)
	// prefetchSem bounds concurrent background prefetch goroutines across all files
	// of this mount, so a runaway sequential scan cannot spawn unbounded goroutines.
	prefetchSem chan struct{}
	// fetchSF collapses concurrent fetches of the same range — chiefly a demand read
	// catching up to an in-flight background prefetch of the same block — so they
	// issue one RBLOCK, not two. Exact for classic (whole-block key); best-effort in
	// demand-fill (keyed by the exact sub-range).
	fetchSF singleflight.Group
	nextH   atomic.Uint64
	seqTab  *blockSeqTable
	sizes   *sizeReg
}

// blockPrefetchSlots bounds concurrent background prefetches per mount.
const blockPrefetchSlots = 8

// The wire-side sub-block granularity (subBlockSize) and the cache's
// (blockcache.SubBlockSize) must be identical, or demand-fill alignment and the
// store's per-sub-block presence bitmaps disagree. This fails to compile if not.
const _ = uint(subBlockSize-blockcache.SubBlockSize) + uint(blockcache.SubBlockSize-subBlockSize)

func (a *blockAttach) newHandle() uint64 { return a.nextH.Add(1) }

func (a *blockAttach) do(op byte, payload []byte) (*msgR, error) {
	frames, err := a.client.do(a.ctx, op, payload)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return newMsgR(nil), nil
	}
	return newMsgR(frames[0].payload), nil
}

// doFrames is do without collapsing to the first frame — for a streamed response
// (the deferred-fsync reconciliation list, which may span several frames).
func (a *blockAttach) doFrames(op byte, payload []byte) ([]frame, error) {
	return a.client.do(a.ctx, op, payload)
}

// Attach implements p9.Attacher.
func (a *blockAttach) Attach() (p9.File, error) {
	h := a.newHandle()
	var w msgW
	w.u64(h)
	if _, err := a.do(opAttach, w.b); err != nil {
		return nil, err
	}
	return &blockProxyFile{a: a, h: h, rel: ""}, nil
}

type blockProxyFile struct {
	templatefs.NoopFile

	a   *blockAttach
	h   uint64
	rel string

	// Frozen at Open for a regular file; drives cache keying.
	cacheable bool
	id        blockcache.FileID

	// Speculative demand-fill readahead state (FeatSubBlockFill): a read whose
	// offset continues the previous one grows an adaptive distance (up to the
	// configured cap) and triggers a BACKGROUND prefetch of the next range, so a
	// sequential reader finds it already cached and never blocks on the round-trip;
	// a jump resets the distance to 0 (random reads stay at pure demand). Atomics
	// keep concurrent reads on one handle race-free (the heuristic is best-effort;
	// correctness is unaffected — seq-gating drops any stale fill).
	raNextOff    atomic.Int64 // expected next sequential read offset
	raWindow     atomic.Int64 // current adaptive prefetch distance in bytes
	prefetchHigh atomic.Int64 // highest offset a prefetch has been launched up to
}

var _ p9.File = (*blockProxyFile)(nil)

func (f *blockProxyFile) fileID() blockcache.FileID {
	return blockcache.FileID{Mount: f.a.mount, Path: f.rel, Writable: true}
}

func putNames(w *msgW, names []string) {
	w.u16(uint16(len(names)))
	for _, n := range names {
		w.str(n)
	}
}

// Walk implements p9.File.Walk.
func (f *blockProxyFile) Walk(names []string) ([]p9.QID, p9.File, error) {
	newH := f.a.newHandle()
	var w msgW
	w.u64(f.h)
	w.u64(newH)
	putNames(&w, names)
	r, err := f.a.do(opWalk, w.b)
	if err != nil {
		return nil, nil, err
	}
	qids := getQIDs(r)
	return qids, &blockProxyFile{a: f.a, h: newH, rel: joinRel(f.rel, names)}, nil
}

// WalkGetAttr implements p9.File.WalkGetAttr (delegated, so the hot Twalkgetattr
// stays 1 RTT).
func (f *blockProxyFile) WalkGetAttr(names []string) ([]p9.QID, p9.File, p9.AttrMask, p9.Attr, error) {
	newH := f.a.newHandle()
	var w msgW
	w.u64(f.h)
	w.u64(newH)
	putNames(&w, names)
	r, err := f.a.do(opWalkGetAttr, w.b)
	if err != nil {
		return nil, nil, p9.AttrMask{}, p9.Attr{}, err
	}
	qids := getQIDs(r)
	mask := getAttrMask(r)
	attr := getAttr(r)
	child := &blockProxyFile{a: f.a, h: newH, rel: joinRel(f.rel, names)}
	child.observe(mask, attr)
	return qids, child, mask, attr, nil
}

// observe records a regular file's size in the shared size registry.
func (f *blockProxyFile) observe(mask p9.AttrMask, attr p9.Attr) {
	if mask.Size && mask.Mode && attr.Mode.IsRegular() {
		f.a.sizes.set(f.fileID(), int64(attr.Size))
	}
}

// GetAttr implements p9.File.GetAttr.
func (f *blockProxyFile) GetAttr(req p9.AttrMask) (p9.QID, p9.AttrMask, p9.Attr, error) {
	var w msgW
	w.u64(f.h)
	putAttrMask(&w, req)
	r, err := f.a.do(opGetAttr, w.b)
	if err != nil {
		return p9.QID{}, p9.AttrMask{}, p9.Attr{}, err
	}
	qid := getQID(r)
	mask := getAttrMask(r)
	attr := getAttr(r)
	f.observe(mask, attr)
	return qid, mask, attr, nil
}

// Open implements p9.File.Open. For a regular file it freezes the writable FileID,
// records the size, and runs the coherence hint check (size+mtime vs the cache's
// last clean sync): on a mismatch — an external change since the cache was written
// (or a fresh session over a warm on-disk cache) — it invalidates the stale entry.
func (f *blockProxyFile) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	var w msgW
	w.u64(f.h)
	w.u32(uint32(mode))
	r, err := f.a.do(opOpen, w.b)
	if err != nil {
		return p9.QID{}, 0, err
	}
	qid := getQID(r)
	iounit := r.u32()

	_, mask, attr, gerr := f.GetAttr(p9.AttrMask{Size: true, MTime: true, Mode: true})
	if gerr == nil && mask.Mode && attr.Mode.IsRegular() {
		f.id = f.fileID()
		f.cacheable = true
		size := int64(attr.Size)
		mtime := int64(attr.MTimeSeconds)*1e9 + int64(attr.MTimeNanoSeconds)
		f.a.sizes.set(f.id, size)
		if hs, hm, ok, _ := f.a.store.Hint(f.id); ok && (hs != size || hm != mtime) {
			_ = f.a.store.Invalidate(f.id)
			f.a.seqTab.reset(f.id)
		}
		_ = f.a.store.SetHint(f.id, size, mtime)
	}
	return qid, iounit, nil
}

// ReadAt implements p9.File.ReadAt, serving from the block cache and miss-filling
// each block over the protocol (with seq-gated fills so a stale fetch never
// overwrites a newer write).
func (f *blockProxyFile) ReadAt(p []byte, off int64) (int, error) {
	if !f.cacheable {
		return f.rawReadAt(p, off)
	}
	size := f.a.sizes.get(f.id)
	if off < 0 {
		return 0, linux.EINVAL
	}
	if off >= size {
		return 0, io.EOF
	}
	end := off + int64(len(p))
	if end > size {
		end = size
	}
	demandFill := f.a.features&FeatSubBlockFill != 0

	// Speculative readahead: a sequential continuation grows the prefetch distance
	// (capped at the configured readahead), a jump resets it. The demand path stays
	// pure (fetch only the touched range); the prefetch of the NEXT range is kicked
	// off in the background after this read is served (see below). Works in classic
	// mode too (prefetch whole blocks) and demand-fill (prefetch sub-ranges).
	prefetchDist := int64(0)
	if f.a.readahead > 0 {
		if off == f.raNextOff.Load() {
			w := f.raWindow.Load() * 2
			if w < subBlockSize {
				w = subBlockSize
			}
			if w > f.a.readahead {
				w = f.a.readahead
			}
			f.raWindow.Store(w)
			prefetchDist = w
		} else {
			f.raWindow.Store(0)
			f.prefetchHigh.Store(end) // jumped: drop the old prefetch horizon
		}
		f.raNextOff.Store(end)
	}

	total := 0
	for pos := off; pos < end; {
		b := int(pos / f.a.chunkSize)
		blockStart := int64(b) * f.a.chunkSize
		blockValidEnd := blockStart + f.a.chunkSize
		if blockValidEnd > size {
			blockValidEnd = size
		}
		// data corresponds to the byte range starting at base; for a whole-block
		// fill base == blockStart, for a demand-fill it is the aligned sub-range.
		var data []byte
		var base int64
		var err error
		if demandFill {
			tHi := end
			if tHi > blockValidEnd {
				tHi = blockValidEnd
			}
			subLo := blockStart + ((pos-blockStart)/subBlockSize)*subBlockSize
			subHi := blockStart + ((tHi-blockStart+subBlockSize-1)/subBlockSize)*subBlockSize
			if subHi > blockValidEnd {
				subHi = blockValidEnd
			}
			data, err = f.subBlockBytes(b, subLo-blockStart, subHi-subLo)
			base = subLo
		} else {
			data, err = f.blockBytes(b, blockValidEnd-blockStart)
			base = blockStart
		}
		if err != nil {
			if total > 0 {
				return total, nil
			}
			return 0, err
		}
		inData := pos - base
		if inData >= int64(len(data)) {
			break
		}
		n := copy(p[total:end-off], data[inData:])
		total += n
		pos += int64(n)
		if n == 0 {
			break
		}
	}
	// Now that the read is served, speculatively prefetch the next range in the
	// background so the following sequential read finds it cached.
	if prefetchDist > 0 {
		from, dist := end, prefetchDist
		if !demandFill {
			// Classic prefetch is whole-block: the block containing `end` is already
			// cached by demand, so start at the NEXT block boundary and cover at
			// least one block.
			from = ((end-1)/f.a.chunkSize + 1) * f.a.chunkSize
			if dist < f.a.chunkSize {
				dist = f.a.chunkSize
			}
		}
		f.maybePrefetch(from, dist, size)
	}
	if total < len(p) {
		return total, io.EOF
	}
	return total, nil
}

// blockBytes returns block b's bytes (valid length chunkLen) from the cache, or
// miss-fills it from the caller — under single-flight so a demand read and a
// background prefetch of the same block collapse into one RBLOCK.
func (f *blockProxyFile) blockBytes(b int, chunkLen int64) ([]byte, error) {
	buf := make([]byte, chunkLen)
	if n, ok, _ := f.a.store.Get(f.id, b, buf); ok && int64(n) == chunkLen {
		return buf, nil
	}
	key := f.id.Key() + "|" + strconv.Itoa(b)
	v, err, _ := f.a.fetchSF.Do(key, func() (any, error) {
		data, hash, seq, err := f.readBlockFromCaller(b)
		if err != nil {
			return nil, err
		}
		if f.a.seqTab.admitFill(f.id, b, seq) {
			_ = f.a.store.PutHashed(f.id, b, data, hash)
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// subBlockBytes returns block b's bytes [subOff, subOff+subLen) from the cache
// (demand-fill), or miss-fills exactly that sub-range from the caller — the read
// analog of blockBytes that avoids dragging in the whole block for a small read.
// Readahead is handled separately by the background prefetcher (maybePrefetch), so
// the demand path stays minimal (no over-fetch on random reads).
func (f *blockProxyFile) subBlockBytes(b int, subOff, subLen int64) ([]byte, error) {
	buf := make([]byte, subLen)
	if n, ok, _ := f.a.store.GetSub(f.id, b, subOff, buf); ok && int64(n) == subLen {
		return buf, nil
	}
	data, _, err := f.fetchSub(b, subOff, subLen)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > subLen {
		data = data[:subLen]
	}
	return data, nil
}

// fetchSub fetches [subOff, subOff+subLen) of block b from the caller and, if the
// fill is not stale (seq-gated), stores it. Shared by the demand and prefetch
// paths, under single-flight (keyed by the exact sub-range) so identical
// concurrent fetches collapse.
func (f *blockProxyFile) fetchSub(b int, subOff, subLen int64) ([]byte, uint64, error) {
	key := f.id.Key() + "|" + strconv.Itoa(b) + "|" + strconv.FormatInt(subOff, 10) + "|" + strconv.FormatInt(subLen, 10)
	v, err, _ := f.a.fetchSF.Do(key, func() (any, error) {
		data, seq, err := f.readRangeFromCaller(b, subOff, subLen)
		if err != nil {
			return nil, err
		}
		if f.a.seqTab.admitFill(f.id, b, seq) {
			_ = f.a.store.PutSub(f.id, b, subOff, data)
		}
		return data, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return v.([]byte), 0, nil
}

// maybePrefetch launches a bounded background prefetch of [from, from+dist)
// (clamped to size), advancing the per-file prefetch horizon so a range is
// prefetched at most once. It never blocks the calling read: if all prefetch slots
// are busy it skips, and a later read retries.
func (f *blockProxyFile) maybePrefetch(from, dist, size int64) {
	to := from + dist
	if to > size {
		to = size
	}
	old := f.prefetchHigh.Load()
	start := from
	if old > start {
		start = old
	}
	if start >= to {
		return // already prefetched this far ahead
	}
	select {
	case f.a.prefetchSem <- struct{}{}:
	default:
		return // all prefetch slots busy; a later read retries
	}
	if !f.prefetchHigh.CompareAndSwap(old, to) {
		<-f.a.prefetchSem // another read raced us; back off
		return
	}
	go f.prefetchRange(start, to, size)
}

// prefetchRange fills [start, end) into the cache in the background (sub-block
// aligned, per block), honoring the mount's teardown context, then releases its
// prefetch slot.
func (f *blockProxyFile) prefetchRange(start, end, size int64) {
	defer func() { <-f.a.prefetchSem }()
	demandFill := f.a.features&FeatSubBlockFill != 0
	for pos := start; pos < end; {
		select {
		case <-f.a.ctx.Done():
			return
		default:
		}
		b := int(pos / f.a.chunkSize)
		blockStart := int64(b) * f.a.chunkSize
		blockValidEnd := blockStart + f.a.chunkSize
		if blockValidEnd > size {
			blockValidEnd = size
		}
		if pos >= blockValidEnd {
			break
		}
		if !demandFill {
			// Classic: warm the whole block (Get-checked + single-flighted).
			_, _ = f.blockBytes(b, blockValidEnd-blockStart)
			pos = blockValidEnd
			continue
		}
		subLo := blockStart + ((pos-blockStart)/subBlockSize)*subBlockSize
		hi := end
		if hi > blockValidEnd {
			hi = blockValidEnd
		}
		subHi := blockStart + ((hi-blockStart+subBlockSize-1)/subBlockSize)*subBlockSize
		if subHi > blockValidEnd {
			subHi = blockValidEnd
		}
		if subLo >= subHi {
			break
		}
		if _, ok, _ := f.a.store.GetSub(f.id, b, subLo-blockStart, make([]byte, subHi-subLo)); !ok {
			_, _, _ = f.fetchSub(b, subLo-blockStart, subHi-subLo)
		}
		pos = subHi
	}
}

// readRangeFromCaller issues a demand-fill ranged READ of one block's sub-range.
func (f *blockProxyFile) readRangeFromCaller(b int, subOff, subLen int64) ([]byte, uint64, error) {
	var w msgW
	w.u64(f.h)
	w.u64(uint64(b))
	w.u32(uint32(subOff))
	w.u32(uint32(subLen))
	r, err := f.a.do(opReadRange, w.b)
	if err != nil {
		return nil, 0, err
	}
	_ = r.u64() // blockIdx echo
	_ = r.u32() // subOff echo
	seq := r.u64()
	data := r.blob()
	return data, seq, nil
}

// readBlockFromCaller issues a single-block READ.
func (f *blockProxyFile) readBlockFromCaller(b int) ([]byte, uint64, uint64, error) {
	var w msgW
	w.u64(f.h)
	w.u64(uint64(b))
	w.u32(1)
	r, err := f.a.do(opRead, w.b)
	if err != nil {
		return nil, 0, 0, err
	}
	_ = r.u64() // blockIdx echo
	seq := r.u64()
	hash := r.u64()
	data := r.blob()
	return data, hash, seq, nil
}

// rawReadAt reads without caching (non-regular files / not opened cacheable).
func (f *blockProxyFile) rawReadAt(p []byte, off int64) (int, error) {
	// Fall back to a whole-range read starting at the containing block. For the
	// non-cacheable path (rare — directories don't ReadAt), serve via a READ of
	// the covering block(s). Kept simple: single covering block.
	b := int(off / f.a.chunkSize)
	data, _, _, err := f.readBlockFromCaller(b)
	if err != nil {
		return 0, err
	}
	inChunk := off - int64(b)*f.a.chunkSize
	if inChunk >= int64(len(data)) {
		return 0, io.EOF
	}
	n := copy(p, data[inChunk:])
	return n, nil
}

// WriteAt implements p9.File.WriteAt: write-through to the caller, then keep the
// server-side cache coherent by one of three negotiated schemes — a whole-block
// hash per touched block (classic), a sub-block hash per touched sub-block
// (FeatSubBlockHash), or a hash-free write-through reconciled in a batch at fsync
// (FeatDeferHash) — and keep the size + hint current.
func (f *blockProxyFile) WriteAt(p []byte, off int64) (int, error) {
	var w msgW
	w.u64(f.h)
	w.u64(uint64(off))
	w.blob(p)
	r, err := f.a.do(opWrite, w.b)
	if err != nil {
		return 0, err
	}
	n := int(r.u32())
	size := int64(r.u64())
	mtimeNs := int64(r.u64())
	seq := r.u64()
	if !f.cacheable {
		return n, nil
	}
	f.a.sizes.set(f.id, size)
	switch {
	case f.a.features&FeatDeferHash != 0:
		// Write through unhashed; the reply carries no inline hashes (fsync
		// reconciles). Consume the (zero) count for a clean cursor.
		_ = r.u16()
		f.applyWriteThrough(p, off, int64(n), size, seq)
	case f.a.features&FeatSubBlockHash != 0:
		f.applyWriteSubBlock(p, off, int64(n), size, seq, r)
	default:
		f.applyWriteClassic(p, off, int64(n), size, seq, r)
	}
	_ = f.a.store.SetHint(f.id, size, mtimeNs)
	return n, nil
}

// applyWriteClassic applies one authoritative whole-block hash per touched block
// (the default coherence path): RMW + self-verify inside the store.
func (f *blockProxyFile) applyWriteClassic(p []byte, off, n, size int64, seq uint64, r *msgR) {
	count := int(r.u16())
	for i := 0; i < count; i++ {
		b := int(r.u64())
		hash := r.u64()
		blockStart := int64(b) * f.a.chunkSize
		lo := max(off, blockStart)
		hi := min(off+n, blockStart+f.a.chunkSize)
		if lo >= hi {
			continue
		}
		chunkOff := lo - blockStart
		sub := p[lo-off : hi-off]
		chunkLen := f.a.chunkSize
		if blockStart+chunkLen > size {
			chunkLen = size - blockStart
		}
		if f.a.seqTab.admitWrite(f.id, b, seq) {
			_ = f.a.store.WriteChunk(f.id, b, chunkOff, sub, chunkLen, hash)
		}
	}
}

// applyWriteThrough splices the write into each touched cached block WITHOUT
// hashing (deferred and sub-block modes both write through first); coherence is
// reconciled afterward against the caller's authoritative hashes.
func (f *blockProxyFile) applyWriteThrough(p []byte, off, n, size int64, seq uint64) {
	if n <= 0 {
		return
	}
	first := off / f.a.chunkSize
	last := (off + n - 1) / f.a.chunkSize
	for b := first; b <= last; b++ {
		blockStart := b * f.a.chunkSize
		lo := max(off, blockStart)
		hi := min(off+n, blockStart+f.a.chunkSize)
		if lo >= hi {
			continue
		}
		chunkOff := lo - blockStart
		sub := p[lo-off : hi-off]
		chunkLen := f.a.chunkSize
		if blockStart+chunkLen > size {
			chunkLen = size - blockStart
		}
		if f.a.seqTab.admitWrite(f.id, int(b), seq) {
			_ = f.a.store.WriteThrough(f.id, int(b), chunkOff, sub, chunkLen)
		}
	}
}

// applyWriteSubBlock writes through, then verifies each touched sub-block against
// the caller's authoritative sub-hash, dropping a block whose cached base diverged
// (so the next read miss-fills it correctly).
func (f *blockProxyFile) applyWriteSubBlock(p []byte, off, n, size int64, seq uint64, r *msgR) {
	f.applyWriteThrough(p, off, n, size, seq)
	count := int(r.u16())
	for i := 0; i < count; i++ {
		b := int(r.u64())
		subOff := int64(r.u32())
		subLen := int64(r.u32())
		hash := r.u64()
		if h, present, _ := f.a.store.HashRange(f.id, b, subOff, subLen); present && h != hash {
			_ = f.a.store.Drop(f.id, b)
		}
	}
}

// SetAttr implements p9.File.SetAttr; a size change resizes the cache entry.
func (f *blockProxyFile) SetAttr(valid p9.SetAttrMask, attr p9.SetAttr) error {
	var w msgW
	w.u64(f.h)
	putSetAttrMask(&w, valid)
	putSetAttr(&w, attr)
	if _, err := f.a.do(opSetAttr, w.b); err != nil {
		return err
	}
	if valid.Size {
		id := f.id
		if !f.cacheable {
			id = f.fileID()
		}
		_ = f.a.store.Resize(id, int64(attr.Size))
		f.a.sizes.set(id, int64(attr.Size))
	}
	return nil
}

// FSync implements p9.File.FSync (forwards to the caller). Under FeatDeferHash the
// reply streams one authoritative hash per unit (sub-block or whole block) dirtied
// since the last fsync; reconcile the cache by dropping any block whose deferred
// write-through diverged from the now-durable file — the batched write-back
// coherence point (the unit granularity composes with FeatSubBlockHash).
func (f *blockProxyFile) FSync() error {
	var w msgW
	w.u64(f.h)
	if f.a.features&FeatDeferHash == 0 || !f.cacheable {
		_, err := f.a.do(opFSync, w.b)
		return err
	}
	frames, err := f.a.doFrames(opFSync, w.b)
	if err != nil {
		return err
	}
	for _, fr := range frames {
		r := newMsgR(fr.payload)
		count := int(r.u16())
		for i := 0; i < count; i++ {
			b := int(r.u64())
			subOff := int64(r.u32())
			subLen := int64(r.u32())
			hash := r.u64()
			if h, present, _ := f.a.store.HashRange(f.id, b, subOff, subLen); present && h != hash {
				_ = f.a.store.Drop(f.id, b)
			}
		}
	}
	return nil
}

// Create implements p9.File.Create; the new file is fresh, so its bucket is
// invalidated and it is returned cacheable+open.
func (f *blockProxyFile) Create(name string, flags p9.OpenFlags, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.File, p9.QID, uint32, error) {
	newH := f.a.newHandle()
	var w msgW
	w.u64(f.h)
	w.u64(newH)
	w.str(name)
	w.u32(uint32(flags))
	w.u32(uint32(perm))
	w.u32(uint32(uid))
	w.u32(uint32(gid))
	r, err := f.a.do(opCreate, w.b)
	if err != nil {
		return nil, p9.QID{}, 0, err
	}
	qid := getQID(r)
	iounit := r.u32()
	child := &blockProxyFile{a: f.a, h: newH, rel: joinRel(f.rel, []string{name})}
	child.id = child.fileID()
	child.cacheable = true
	_ = f.a.store.Invalidate(child.id)
	f.a.seqTab.reset(child.id)
	f.a.sizes.set(child.id, 0)
	return child, qid, iounit, nil
}

// Mkdir implements p9.File.Mkdir.
func (f *blockProxyFile) Mkdir(name string, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.QID, error) {
	var w msgW
	w.u64(f.h)
	w.str(name)
	w.u32(uint32(perm))
	w.u32(uint32(uid))
	w.u32(uint32(gid))
	r, err := f.a.do(opMkdir, w.b)
	if err != nil {
		return p9.QID{}, err
	}
	return getQID(r), nil
}

// Symlink implements p9.File.Symlink.
func (f *blockProxyFile) Symlink(oldName, newName string, uid p9.UID, gid p9.GID) (p9.QID, error) {
	var w msgW
	w.u64(f.h)
	w.str(oldName)
	w.str(newName)
	w.u32(uint32(uid))
	w.u32(uint32(gid))
	r, err := f.a.do(opSymlink, w.b)
	if err != nil {
		return p9.QID{}, err
	}
	return getQID(r), nil
}

// Link implements p9.File.Link.
func (f *blockProxyFile) Link(target p9.File, newName string) error {
	tgt, ok := target.(*blockProxyFile)
	if !ok {
		return linux.EINVAL
	}
	var w msgW
	w.u64(f.h)
	w.u64(tgt.h)
	w.str(newName)
	_, err := f.a.do(opLink, w.b)
	return err
}

// RenameAt implements p9.File.RenameAt; the cache bucket moves with it.
func (f *blockProxyFile) RenameAt(oldName string, newDir p9.File, newName string) error {
	nd, ok := newDir.(*blockProxyFile)
	if !ok {
		return linux.EINVAL
	}
	var w msgW
	w.u64(f.h)
	w.str(oldName)
	w.u64(nd.h)
	w.str(newName)
	if _, err := f.a.do(opRenameAt, w.b); err != nil {
		return err
	}
	oldID := blockcache.FileID{Mount: f.a.mount, Path: joinRel(f.rel, []string{oldName}), Writable: true}
	newID := blockcache.FileID{Mount: f.a.mount, Path: joinRel(nd.rel, []string{newName}), Writable: true}
	_ = f.a.store.Rename(oldID, newID)
	f.a.seqTab.reset(oldID)
	f.a.seqTab.reset(newID)
	return nil
}

// UnlinkAt implements p9.File.UnlinkAt; the removed name's bucket is invalidated.
func (f *blockProxyFile) UnlinkAt(name string, flags uint32) error {
	var w msgW
	w.u64(f.h)
	w.str(name)
	w.u32(flags)
	if _, err := f.a.do(opUnlinkAt, w.b); err != nil {
		return err
	}
	id := blockcache.FileID{Mount: f.a.mount, Path: joinRel(f.rel, []string{name}), Writable: true}
	_ = f.a.store.Invalidate(id)
	f.a.seqTab.reset(id)
	return nil
}

// Readlink implements p9.File.Readlink.
func (f *blockProxyFile) Readlink() (string, error) {
	var w msgW
	w.u64(f.h)
	r, err := f.a.do(opReadlink, w.b)
	if err != nil {
		return "", err
	}
	return r.str(), nil
}

// StatFS implements p9.File.StatFS.
func (f *blockProxyFile) StatFS() (p9.FSStat, error) {
	var w msgW
	w.u64(f.h)
	r, err := f.a.do(opStatFS, w.b)
	if err != nil {
		return p9.FSStat{}, err
	}
	return getFSStat(r), nil
}

// Readdir implements p9.File.Readdir.
func (f *blockProxyFile) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
	var w msgW
	w.u64(f.h)
	w.u64(offset)
	w.u32(count)
	r, err := f.a.do(opReaddir, w.b)
	if err != nil {
		return nil, err
	}
	return getDirents(r), nil
}

// Close implements p9.File.Close (clunks the one caller-side handle).
func (f *blockProxyFile) Close() error {
	var w msgW
	w.u64(f.h)
	_, err := f.a.do(opClunk, w.b)
	return err
}

// ---- session-scoped coherence state ----

// sizeReg tracks each writable file's current logical length, keyed by FileID.Key.
type sizeReg struct {
	mu sync.Mutex
	m  map[string]int64
}

func (s *sizeReg) get(id blockcache.FileID) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id.Key()]
}

func (s *sizeReg) set(id blockcache.FileID, v int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id.Key()] = v
}

// blockSeqTable gates cache writes/fills by the caller-assigned per-file write
// sequence so out-of-order responses converge to the latest write. Session-scoped.
type blockSeqTable struct {
	mu   sync.Mutex
	last map[string]map[int]uint64
}

// admitWrite admits a write to (id, block) iff its seq is strictly newer.
func (t *blockSeqTable) admitWrite(id blockcache.FileID, b int, seq uint64) bool {
	return t.admit(id, b, seq, true)
}

// admitFill admits a read fill iff its seq is at least as new (a fill at the same
// seq as the last write is consistent and idempotent).
func (t *blockSeqTable) admitFill(id blockcache.FileID, b int, seq uint64) bool {
	return t.admit(id, b, seq, false)
}

func (t *blockSeqTable) admit(id blockcache.FileID, b int, seq uint64, strict bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := id.Key()
	m := t.last[k]
	if m == nil {
		m = map[int]uint64{}
		t.last[k] = m
	}
	cur, seen := m[b]
	ok := (!seen) || (strict && seq > cur) || (!strict && seq >= cur)
	if ok && seq > cur {
		m[b] = seq
	}
	return ok
}

func (t *blockSeqTable) reset(id blockcache.FileID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.last, id.Key())
}
