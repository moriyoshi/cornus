package wire

import (
	"io"
	"net"
	pathpkg "path"
	"sync"
	"sync/atomic"

	"github.com/hugelgupf/p9/linux"
	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// blockServer is the CALLER endpoint of the block protocol (the authoritative
// file owner). It decodes request frames and services them against the confined
// writable export (writableConfinedAttacher — the SAME guard/containment the 9P
// export uses), and computes each block's xxh3 content hash + a per-file write
// sequence inline on READ/WRITE/STATBLOCK so the server's cache stays coherent.
//
// Phase 1 processes requests SERIALLY (one at a time). The proxy still pipelines
// (its in-flight requests queue here); local-disk ops are fast, so this is
// correct and simple. Per-request concurrency is a follow-up (it would need the
// per-file seq/handle tables to be locked; kept trivial here by serial dispatch).
type blockServer struct {
	conn      net.Conn
	chunkSize int64
	maxFrame  int
	attacher  p9.Attacher

	handles  map[uint64]*bsHandle // proxy-chosen handle id -> caller file
	seq      map[string]uint64    // per-file (rel path) write sequence
	features uint32               // negotiated coherence features (FeatSubBlockHash / FeatDeferHash)

	// dirty tracks, per file (rel path), the set of unit-aligned absolute offsets
	// written since the last fsync, for FeatDeferHash: at fsync each is hashed once
	// and reconciled, instead of hashing on every write. The unit is subBlockSize
	// (FeatSubBlockHash) or the whole chunkSize. markDirty (concurrent writers) and
	// the fsync drain guard it with mu.
	dirty map[string]map[int64]struct{}

	// Concurrency: READ requests (opRead/opReadRange/opStatBlock) AND opWrite are
	// dispatched to bounded goroutines so a burst (the proxy's speculative prefetch,
	// or a kernel writeback flush of many dirty pages) is served in parallel instead
	// of one-at-a-time. Coherence does not depend on serializing writes here — it is
	// enforced at the proxy (per-block seq-gating + hash-verify + drop-on-mismatch),
	// which already tolerates concurrent writes — so parallelizing the caller-side
	// WriteAt + hash is safe. The remaining mutating ops (fsync, setattr, and all
	// metadata) stay on the serial loop goroutine; fsync/setattr first drain in-flight
	// writes via writeWG so they never race a WriteAt. mu guards the handle + seq +
	// dirty maps (shared across goroutines); writeMu serializes reply frames on the
	// wire; readSem/writeSem bound concurrent readers/writers; opScratch pools
	// per-handler chunk buffers (the serial fsync path keeps using the single
	// `scratch`).
	mu        sync.Mutex
	writeMu   sync.Mutex
	readSem   chan struct{}
	writeSem  chan struct{}
	writeWG   sync.WaitGroup
	opScratch sync.Pool
	scratch   []byte
}

// blockCallerReadSlots bounds concurrent read handlers at the caller. A var (not
// const) so a test can force serial (1) vs concurrent for an A/B.
var blockCallerReadSlots = 16

// blockCallerReadHook, if set, runs at the start of every read handler — a test
// seam to simulate slow authoritative-storage read cost. Atomic so a test can set
// or clear it while caller goroutines are running; nil (production) => a single
// cheap atomic load + nil check.
var blockCallerReadHook atomic.Pointer[func()]

// blockCallerWriteSlots bounds concurrent write handlers at the caller (separate
// from reads so a write burst and a read burst do not compete for slots). A var so
// a test can force serial (1) vs concurrent for an A/B.
var blockCallerWriteSlots = 16

// blockCallerWriteHook, if set, runs at the start of every write handler — a test
// seam to simulate slow authoritative-storage write cost. Same atomic contract as
// blockCallerReadHook; nil (production) => a cheap atomic load + nil check.
var blockCallerWriteHook atomic.Pointer[func()]

// unitSize is the coherence hashing granularity for the negotiated features:
// a sub-block under FeatSubBlockHash, else the whole chunk.
func (s *blockServer) unitSize() int64 {
	if s.features&FeatSubBlockHash != 0 {
		return subBlockSize
	}
	return s.chunkSize
}

type bsHandle struct {
	file p9.File
	rel  string // path from the export root (for the per-file seq key)
}

// ServeBlockServer runs the caller endpoint over conn for the export rooted at
// dir, using chunkSize as the block size. It returns when the connection ends.
func ServeBlockServer(conn net.Conn, dir string, chunkSize int64, opts ...BlockOpt) {
	if chunkSize <= 0 {
		chunkSize = defaultBlockChunk
	}
	o := resolveBlockOpts(opts)
	local := helloParams{version: blockProtoVersion, chunkSize: uint32(chunkSize), maxInflight: blockMaxInflight, features: o.features}
	peer, err := blockServerHandshake(conn, local)
	if err != nil {
		return
	}
	att, err := writableConfinedAttacher(dir)
	if err != nil {
		return
	}
	s := &blockServer{
		conn:      conn,
		chunkSize: chunkSize,
		maxFrame:  int(chunkSize) + blockFrameSlack,
		attacher:  att,
		handles:   map[uint64]*bsHandle{},
		seq:       map[string]uint64{},
		features:  local.features & peer.features,
		dirty:     map[string]map[int64]struct{}{},
		readSem:   make(chan struct{}, blockCallerReadSlots),
		writeSem:  make(chan struct{}, blockCallerWriteSlots),
		scratch:   make([]byte, chunkSize),
	}
	s.opScratch.New = func() any { b := make([]byte, chunkSize); return &b }
	s.loop()
}

// defaultBlockChunk mirrors blockcache.DefaultChunkSize without importing it here.
const defaultBlockChunk = 1 << 20

func (s *blockServer) loop() {
	for {
		f, err := readFrame(s.conn, s.maxFrame)
		if err != nil {
			return
		}
		// Reads and writes run concurrently (each bounded by its own semaphore);
		// coherence is enforced at the proxy, not by serializing writes here. fsync and
		// setattr first drain in-flight writes (writeWG) so they never race a WriteAt;
		// all other metadata ops stay serial on this goroutine.
		switch f.op {
		case opRead, opReadRange, opStatBlock:
			s.readSem <- struct{}{}
			go func(f frame) {
				defer func() { <-s.readSem }()
				s.dispatch(f)
			}(f)
		case opWrite:
			s.writeWG.Add(1)
			s.writeSem <- struct{}{}
			go func(f frame) {
				defer func() { <-s.writeSem; s.writeWG.Done() }()
				s.dispatch(f)
			}(f)
		case opFSync, opSetAttr:
			s.writeWG.Wait() // barrier: see all prior writes, race none in flight
			s.dispatch(f)
		default:
			s.dispatch(f)
		}
	}
}

func (s *blockServer) reply(reqID uint64, payload []byte) {
	s.writeMu.Lock()
	_ = writeFrame(s.conn, frame{op: opBlockResp, flags: flagFinal, reqID: reqID, payload: payload})
	s.writeMu.Unlock()
}

func (s *blockServer) replyFlags(reqID uint64, flags byte, payload []byte) {
	s.writeMu.Lock()
	_ = writeFrame(s.conn, frame{op: opBlockResp, flags: flags, reqID: reqID, payload: payload})
	s.writeMu.Unlock()
}

func (s *blockServer) replyErr(reqID uint64, err error) {
	var w msgW
	w.u32(errnoOf(err))
	s.writeMu.Lock()
	_ = writeFrame(s.conn, frame{op: opBlockResp, flags: flagFinal | flagErr, reqID: reqID, payload: w.b})
	s.writeMu.Unlock()
}

// get/putHandle/delHandle guard the handle map (shared between the serial loop and
// the concurrent read goroutines).
func (s *blockServer) get(h uint64) (*bsHandle, bool) {
	s.mu.Lock()
	hn, ok := s.handles[h]
	s.mu.Unlock()
	return hn, ok
}

func (s *blockServer) putHandle(h uint64, hn *bsHandle) {
	s.mu.Lock()
	s.handles[h] = hn
	s.mu.Unlock()
}

func (s *blockServer) delHandle(h uint64) (*bsHandle, bool) {
	s.mu.Lock()
	hn, ok := s.handles[h]
	if ok {
		delete(s.handles, h)
	}
	s.mu.Unlock()
	return hn, ok
}

// seqOf/bumpSeq guard the per-file write sequence (read by concurrent readers,
// bumped by the serial write path).
func (s *blockServer) seqOf(rel string) uint64 {
	s.mu.Lock()
	v := s.seq[rel]
	s.mu.Unlock()
	return v
}

func (s *blockServer) bumpSeq(rel string) uint64 {
	s.mu.Lock()
	s.seq[rel]++
	v := s.seq[rel]
	s.mu.Unlock()
	return v
}

func (s *blockServer) dispatch(f frame) {
	r := newMsgR(f.payload)
	switch f.op {
	case opAttach:
		h := r.u64()
		root, err := s.attacher.Attach()
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.putHandle(h, &bsHandle{file: root, rel: ""})
		s.reply(f.reqID, nil)

	case opWalk:
		src := r.u64()
		newH := r.u64()
		names := readNames(r)
		hn, ok := s.get(src)
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		qids, child, err := hn.file.Walk(names)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.putHandle(newH, &bsHandle{file: child, rel: joinRel(hn.rel, names)})
		var w msgW
		putQIDs(&w, qids)
		s.reply(f.reqID, w.b)

	case opWalkGetAttr:
		src := r.u64()
		newH := r.u64()
		names := readNames(r)
		hn, ok := s.get(src)
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		qids, child, mask, attr, err := hn.file.WalkGetAttr(names)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.putHandle(newH, &bsHandle{file: child, rel: joinRel(hn.rel, names)})
		var w msgW
		putQIDs(&w, qids)
		putAttrMask(&w, mask)
		putAttr(&w, attr)
		s.reply(f.reqID, w.b)

	case opGetAttr:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		mask := getAttrMask(r)
		qid, vmask, attr, err := hn.file.GetAttr(mask)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		putQID(&w, qid)
		putAttrMask(&w, vmask)
		putAttr(&w, attr)
		s.reply(f.reqID, w.b)

	case opSetAttr:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		valid := getSetAttrMask(r)
		attr := getSetAttr(r)
		if err := hn.file.SetAttr(valid, attr); err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.reply(f.reqID, nil)

	case opOpen:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		mode := p9.OpenFlags(r.u32())
		qid, iounit, err := hn.file.Open(mode)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		putQID(&w, qid)
		w.u32(iounit)
		s.reply(f.reqID, w.b)

	case opRead:
		s.handleRead(f.reqID, r)

	case opReadRange:
		s.handleReadRange(f.reqID, r)

	case opWrite:
		s.handleWrite(f.reqID, r)

	case opStatBlock:
		s.handleStatBlock(f.reqID, r)

	case opFSync:
		s.handleFSync(f.reqID, r)

	case opCreate:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		newH := r.u64()
		name := r.str()
		flags := p9.OpenFlags(r.u32())
		perm := p9.FileMode(r.u32())
		uid := p9.UID(r.u32())
		gid := p9.GID(r.u32())
		child, qid, iounit, err := hn.file.Create(name, flags, perm, uid, gid)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.putHandle(newH, &bsHandle{file: child, rel: joinRel(hn.rel, []string{name})})
		var w msgW
		putQID(&w, qid)
		w.u32(iounit)
		s.reply(f.reqID, w.b)

	case opMkdir:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		name := r.str()
		perm := p9.FileMode(r.u32())
		uid := p9.UID(r.u32())
		gid := p9.GID(r.u32())
		qid, err := hn.file.Mkdir(name, perm, uid, gid)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		putQID(&w, qid)
		s.reply(f.reqID, w.b)

	case opSymlink:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		oldName := r.str()
		newName := r.str()
		uid := p9.UID(r.u32())
		gid := p9.GID(r.u32())
		qid, err := hn.file.Symlink(oldName, newName, uid, gid)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		putQID(&w, qid)
		s.reply(f.reqID, w.b)

	case opLink:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		tgt, ok2 := s.get(r.u64())
		newName := r.str()
		if !ok2 {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		if err := hn.file.Link(tgt.file, newName); err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.reply(f.reqID, nil)

	case opRenameAt:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		oldName := r.str()
		newDir, ok2 := s.get(r.u64())
		newName := r.str()
		if !ok2 {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		if err := hn.file.RenameAt(oldName, newDir.file, newName); err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.reply(f.reqID, nil)

	case opUnlinkAt:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		name := r.str()
		flags := r.u32()
		if err := hn.file.UnlinkAt(name, flags); err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		s.reply(f.reqID, nil)

	case opReadlink:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		target, err := hn.file.Readlink()
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		w.str(target)
		s.reply(f.reqID, w.b)

	case opStatFS:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		st, err := hn.file.StatFS()
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		putFSStat(&w, st)
		s.reply(f.reqID, w.b)

	case opReaddir:
		hn, ok := s.get(r.u64())
		if !ok {
			s.replyErr(f.reqID, linux.EBADF)
			return
		}
		offset := r.u64()
		count := r.u32()
		ents, err := hn.file.Readdir(offset, count)
		if err != nil {
			s.replyErr(f.reqID, err)
			return
		}
		var w msgW
		putDirents(&w, ents)
		s.reply(f.reqID, w.b)

	case opClunk:
		h := r.u64()
		if hn, ok := s.delHandle(h); ok {
			_ = hn.file.Close()
		}
		s.reply(f.reqID, nil)

	default:
		s.replyErr(f.reqID, linux.ENOSYS)
	}
}

// fileSize returns the current logical size of a file via GetAttr.
func fileSize(f p9.File) (int64, error) {
	_, _, attr, err := f.GetAttr(p9.AttrMask{Size: true})
	if err != nil {
		return 0, err
	}
	return int64(attr.Size), nil
}

func fileSizeMTime(f p9.File) (int64, int64, error) {
	_, _, attr, err := f.GetAttr(p9.AttrMask{Size: true, MTime: true})
	if err != nil {
		return 0, 0, err
	}
	return int64(attr.Size), int64(attr.MTimeSeconds)*1e9 + int64(attr.MTimeNanoSeconds), nil
}

// readBlock reads block blockIdx of f (whose current size is size) into the
// caller-provided scratch buffer (>= the block's valid length), returning the
// block's valid bytes (a sub-slice of scratch — valid only until scratch is
// reused), its xxh3 hash, and whether the block exists (offset < size). The scratch
// is a pooled per-reader buffer (concurrent read handlers) or the serial write
// path's own, so it never allocates a fresh 1 MiB slice per read.
func (s *blockServer) readBlock(f p9.File, blockIdx int64, size int64, scratch []byte) ([]byte, uint64, bool, error) {
	off := blockIdx * s.chunkSize
	if off >= size {
		return nil, 0, false, nil
	}
	blockLen := s.chunkSize
	if off+blockLen > size {
		blockLen = size - off
	}
	buf := scratch[:blockLen]
	n, err := f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, 0, false, err
	}
	buf = buf[:n]
	return buf, blockcache.HashChunk(buf), true, nil
}

// opScratchBuf borrows a pooled chunk-sized buffer for a concurrent handler; the
// returned put func returns it to the pool.
func (s *blockServer) opScratchBuf() (scratch []byte, put func()) {
	bp := s.opScratch.Get().(*[]byte)
	return *bp, func() { s.opScratch.Put(bp) }
}

// readScratchBuf is opScratchBuf plus the read test-hook (simulated read cost).
func (s *blockServer) readScratchBuf() (scratch []byte, put func()) {
	if h := blockCallerReadHook.Load(); h != nil {
		(*h)()
	}
	return s.opScratchBuf()
}

func (s *blockServer) handleRead(reqID uint64, r *msgR) {
	hn, ok := s.get(r.u64())
	if !ok {
		s.replyErr(reqID, linux.EBADF)
		return
	}
	blockIdx := int64(r.u64())
	nBlocks := int(r.u32())
	if nBlocks < 1 {
		nBlocks = 1
	}
	size, err := fileSize(hn.file)
	if err != nil {
		s.replyErr(reqID, err)
		return
	}
	seq := s.seqOf(hn.rel)
	scratch, put := s.readScratchBuf()
	defer put()
	for i := 0; i < nBlocks; i++ {
		b := blockIdx + int64(i)
		data, hash, present, err := s.readBlock(hn.file, b, size, scratch)
		if err != nil {
			s.replyErr(reqID, err)
			return
		}
		final := i == nBlocks-1
		flags := byte(0)
		off := b * s.chunkSize
		atEOF := !present || off+int64(len(data)) >= size
		if atEOF {
			final = true
			flags |= flagEOF
		}
		if final {
			flags |= flagFinal
		}
		var w msgW
		w.u64(uint64(b))
		w.u64(seq)
		w.u64(hash)
		w.blob(data)
		s.replyFlags(reqID, flags, w.b)
		if final {
			return
		}
	}
}

func (s *blockServer) handleWrite(reqID uint64, r *msgR) {
	if h := blockCallerWriteHook.Load(); h != nil {
		(*h)()
	}
	hn, ok := s.get(r.u64())
	if !ok {
		s.replyErr(reqID, linux.EBADF)
		return
	}
	off := int64(r.u64())
	data := r.blob()
	if r.error() != nil {
		s.replyErr(reqID, linux.EINVAL)
		return
	}
	n, err := hn.file.WriteAt(data, off)
	if err != nil {
		s.replyErr(reqID, err)
		return
	}
	seq := s.bumpSeq(hn.rel)
	size, mtimeNs, err := fileSizeMTime(hn.file)
	if err != nil {
		s.replyErr(reqID, err)
		return
	}
	var w msgW
	w.u32(uint32(n))
	w.u64(uint64(size))
	w.u64(uint64(mtimeNs))
	w.u64(seq)

	switch {
	case s.features&FeatDeferHash != 0:
		// Deferred coherence: no hashing on the write. Record the dirty units so
		// fsync can hash each once and reconcile the proxy cache in a batch.
		s.markDirty(hn.rel, off, int64(n), size)
		w.u16(0)
	case s.features&FeatSubBlockHash != 0:
		// Sub-block coherence: hash only the touched sub-blocks (subBlockSize), so a
		// small page write costs a sub-block read+hash, not a whole 1 MiB one.
		scratch, put := s.opScratchBuf()
		units, herr := s.writeUnits(hn.file, data, off, int64(n), size, scratch)
		put()
		if herr != nil {
			s.replyErr(reqID, herr)
			return
		}
		putUnitHashes(&w, units)
	default:
		// Classic: one xxh3 hash per touched whole block, inline in the reply.
		var entries []unitHash
		if n > 0 {
			scratch, put := s.opScratchBuf()
			first := off / s.chunkSize
			last := (off + int64(n) - 1) / s.chunkSize
			for b := first; b <= last; b++ {
				blockStart := b * s.chunkSize
				blockValidEnd := blockStart + s.chunkSize
				if blockValidEnd > size {
					blockValidEnd = size
				}
				if blockStart >= blockValidEnd {
					continue
				}
				hash, herr := s.unitHashCovering(hn.file, blockStart, blockValidEnd-blockStart, data, off, int64(n), scratch)
				if herr != nil {
					put()
					s.replyErr(reqID, herr)
					return
				}
				entries = append(entries, unitHash{blockIdx: b, hash: hash})
			}
			put()
		}
		w.u16(uint16(len(entries)))
		for _, e := range entries {
			w.u64(uint64(e.blockIdx))
			w.u64(e.hash)
		}
	}
	s.reply(reqID, w.b)
}

// unitHash is one authoritative coherence hash: the whole block idx (classic /
// deferred, subOff=0) or a subBlockSize sub-block at subOff within it.
type unitHash struct {
	blockIdx int64
	subOff   int64
	subLen   int64
	hash     uint64
}

// putUnitHashes writes a sub-block unit list: u16 count then (blockIdx, subOff,
// subLen, hash) per unit — the FeatSubBlockHash write-reply / defer-fsync format.
func putUnitHashes(w *msgW, units []unitHash) {
	w.u16(uint16(len(units)))
	for _, u := range units {
		w.u64(uint64(u.blockIdx))
		w.u32(uint32(u.subOff))
		w.u32(uint32(u.subLen))
		w.u64(u.hash)
	}
}

// unitHashCovering returns the xxh3 hash of the unit [absOff, absOff+length). When
// the just-applied write (data, written at off, n bytes) FULLY covers the unit —
// the common case for a sequential / full-block write — it hashes the write buffer
// directly, avoiding a read-back of bytes we just wrote (which on DiskStore is a
// whole extra disk read per write). A partial-edge unit is read from the file.
func (s *blockServer) unitHashCovering(f p9.File, absOff, length int64, data []byte, off, n int64, scratch []byte) (uint64, error) {
	if off <= absOff && absOff+length <= off+n {
		lo := absOff - off
		return blockcache.HashChunk(data[lo : lo+length]), nil
	}
	return s.hashUnit(f, absOff, length, scratch)
}

// hashUnit reads [absOff, absOff+length) of f into the caller-provided scratch
// buffer and returns its xxh3 hash. length must be <= chunkSize (scratch's
// capacity). scratch is a pooled per-writer buffer (concurrent write handlers) or,
// on the serial fsync-drain path, the blockServer's own `scratch`.
func (s *blockServer) hashUnit(f p9.File, absOff, length int64, scratch []byte) (uint64, error) {
	buf := scratch[:length]
	n, err := f.ReadAt(buf, absOff)
	if err != nil && err != io.EOF {
		return 0, err
	}
	return blockcache.HashChunk(buf[:n]), nil
}

// forEachWriteUnit calls fn for each coherence unit overlapping [off, off+n)
// (clamped to size): a subBlockSize sub-block (FeatSubBlockHash) or the whole
// chunk. Units are aligned RELATIVE to each block start, so a unit never crosses a
// chunk boundary; the last unit of a block (or the file) may be short. fn receives
// the block index, the sub-offset within the block, and the unit's valid length.
func (s *blockServer) forEachWriteUnit(off, n, size int64, fn func(blockIdx, subOff, length int64)) {
	if n <= 0 {
		return
	}
	end := off + n
	if end > size {
		end = size
	}
	u := s.unitSize()
	first := off / s.chunkSize
	last := (off + n - 1) / s.chunkSize
	for b := first; b <= last; b++ {
		blockStart := b * s.chunkSize
		if blockStart >= size {
			break
		}
		blockValidEnd := blockStart + s.chunkSize
		if blockValidEnd > size {
			blockValidEnd = size
		}
		lo := max(off, blockStart)
		hi := min(end, blockValidEnd)
		if lo >= hi {
			continue
		}
		for so := ((lo - blockStart) / u) * u; blockStart+so < hi; so += u {
			subEnd := blockStart + so + u
			if subEnd > blockValidEnd {
				subEnd = blockValidEnd
			}
			length := subEnd - (blockStart + so)
			if length <= 0 {
				continue
			}
			fn(b, so, length)
		}
	}
}

// writeUnits hashes each coherence unit touched by [off, off+n) and returns their
// (blockIdx, subOff, subLen, hash) — the inline FeatSubBlockHash write reply. Units
// the write fully covers are hashed straight from the write buffer (no read-back).
func (s *blockServer) writeUnits(f p9.File, data []byte, off, n, size int64, scratch []byte) ([]unitHash, error) {
	var out []unitHash
	var ferr error
	s.forEachWriteUnit(off, n, size, func(blockIdx, subOff, length int64) {
		if ferr != nil {
			return
		}
		h, err := s.unitHashCovering(f, blockIdx*s.chunkSize+subOff, length, data, off, n, scratch)
		if err != nil {
			ferr = err
			return
		}
		out = append(out, unitHash{blockIdx: blockIdx, subOff: subOff, subLen: length, hash: h})
	})
	return out, ferr
}

// markDirty records the absolute unit-aligned offsets touched by [off, off+n)
// (clamped to size) for reconciliation at the next fsync (FeatDeferHash). The unit
// granularity follows unitSize(), so defer composes with FeatSubBlockHash (dirty
// sub-blocks) or tracks whole blocks on its own.
func (s *blockServer) markDirty(rel string, off, n, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.dirty[rel]
	if m == nil {
		m = map[int64]struct{}{}
		s.dirty[rel] = m
	}
	s.forEachWriteUnit(off, n, size, func(blockIdx, subOff, length int64) {
		m[blockIdx*s.chunkSize+subOff] = struct{}{}
	})
}

// drainDirtyUnits removes rel's dirty set and hashes each recorded unit once
// against the now-durable file, returning the (blockIdx, subOff, subLen, hash)
// list to reconcile. Units past EOF (a later shrink) are skipped.
func (s *blockServer) drainDirtyUnits(f p9.File, rel string, scratch []byte) ([]unitHash, error) {
	s.mu.Lock()
	offs := s.dirty[rel]
	delete(s.dirty, rel)
	s.mu.Unlock()
	if len(offs) == 0 {
		return nil, nil
	}
	size, err := fileSize(f)
	if err != nil {
		return nil, err
	}
	u := s.unitSize()
	out := make([]unitHash, 0, len(offs))
	for uo := range offs {
		if uo >= size {
			continue
		}
		blockIdx := uo / s.chunkSize
		blockStart := blockIdx * s.chunkSize
		blockValidEnd := blockStart + s.chunkSize
		if blockValidEnd > size {
			blockValidEnd = size
		}
		length := u
		if uo+length > blockValidEnd {
			length = blockValidEnd - uo
		}
		if length <= 0 {
			continue
		}
		h, herr := s.hashUnit(f, uo, length, scratch)
		if herr != nil {
			return nil, herr
		}
		out = append(out, unitHash{blockIdx: blockIdx, subOff: uo - blockStart, subLen: length, hash: h})
	}
	return out, nil
}

// handleFSync fsyncs the file, then (FeatDeferHash) hashes each unit dirtied since
// the last fsync ONCE and streams the (blockIdx, subOff, subLen, hash) list back —
// across multiple frames if it exceeds one — so the proxy reconciles its cache in
// a batch. This is the batched-write-back coherence path; the unit granularity
// (sub-block or whole block) composes with FeatSubBlockHash.
func (s *blockServer) handleFSync(reqID uint64, r *msgR) {
	hn, ok := s.get(r.u64())
	if !ok {
		s.replyErr(reqID, linux.EBADF)
		return
	}
	if err := hn.file.FSync(); err != nil {
		s.replyErr(reqID, err)
		return
	}
	if s.features&FeatDeferHash == 0 {
		s.reply(reqID, nil)
		return
	}
	// Serial-after-barrier (loop goroutine, in-flight writes drained), so the single
	// `scratch` is exclusive here and needs no pooled buffer.
	units, err := s.drainDirtyUnits(hn.file, hn.rel, s.scratch)
	if err != nil {
		s.replyErr(reqID, err)
		return
	}
	if len(units) == 0 {
		var w msgW
		w.u16(0)
		s.reply(reqID, w.b)
		return
	}
	// Each unit is 24 bytes; keep a reply frame within the negotiated frame cap.
	maxPer := (s.maxFrame - 64) / 24
	if maxPer < 1 {
		maxPer = 1
	}
	for i := 0; i < len(units); i += maxPer {
		end := i + maxPer
		if end > len(units) {
			end = len(units)
		}
		var w msgW
		putUnitHashes(&w, units[i:end])
		flags := byte(0)
		if end == len(units) {
			flags = flagFinal
		}
		s.replyFlags(reqID, flags, w.b)
	}
}

// handleReadRange serves a demand-fill read (FeatSubBlockFill): a sub-block-aligned
// range [subOff, subOff+subLen) within one block, reading only that range from the
// authoritative file. The reply echoes (blockIdx, subOff), the per-file write seq
// (so the proxy can seq-gate the fill), and the bytes.
func (s *blockServer) handleReadRange(reqID uint64, r *msgR) {
	hn, ok := s.get(r.u64())
	if !ok {
		s.replyErr(reqID, linux.EBADF)
		return
	}
	blockIdx := int64(r.u64())
	subOff := int64(r.u32())
	subLen := int64(r.u32())
	if r.error() != nil || subLen <= 0 || subLen > s.chunkSize {
		s.replyErr(reqID, linux.EINVAL)
		return
	}
	scratch, put := s.readScratchBuf()
	defer put()
	buf := scratch[:subLen]
	n, err := hn.file.ReadAt(buf, blockIdx*s.chunkSize+subOff)
	if err != nil && err != io.EOF {
		s.replyErr(reqID, err)
		return
	}
	var w msgW
	w.u64(uint64(blockIdx))
	w.u32(uint32(subOff))
	w.u64(s.seqOf(hn.rel))
	w.blob(buf[:n])
	s.reply(reqID, w.b)
}

func (s *blockServer) handleStatBlock(reqID uint64, r *msgR) {
	hn, ok := s.get(r.u64())
	if !ok {
		s.replyErr(reqID, linux.EBADF)
		return
	}
	blockIdx := int64(r.u64())
	nBlocks := int(r.u32())
	if nBlocks < 1 {
		nBlocks = 1
	}
	size, err := fileSize(hn.file)
	if err != nil {
		s.replyErr(reqID, err)
		return
	}
	seq := s.seqOf(hn.rel)
	scratch, put := s.readScratchBuf()
	defer put()
	var w msgW
	w.u16(uint16(nBlocks))
	for i := 0; i < nBlocks; i++ {
		b := blockIdx + int64(i)
		_, hash, present, herr := s.readBlock(hn.file, b, size, scratch)
		if herr != nil {
			s.replyErr(reqID, herr)
			return
		}
		w.u64(uint64(b))
		w.u64(seq)
		w.u64(hash)
		w.boolean(present)
	}
	s.reply(reqID, w.b)
}

func readNames(r *msgR) []string {
	n := int(r.u16())
	if n == 0 {
		return nil
	}
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		names = append(names, r.str())
	}
	return names
}

func joinRel(base string, names []string) string {
	rel := base
	for _, n := range names {
		rel = pathpkg.Join(rel, n)
	}
	return rel
}
