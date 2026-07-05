package wire

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hugelgupf/p9/linux"
)

// The block protocol is cornus's bespoke, block-granular file I/O protocol on the
// SERVER<->CALLER hop for writable, cache-coherent mounts (the '\'b'\' backing). It
// replaces tunneling 9P over the muxed stream: the server keeps a userspace
// p9.Server toward the kernel mount, but speaks THIS protocol to the caller. Its
// data path is block-indexed (a block == the cache chunk size) and carries the
// per-block xxh3 hash + a write sequence inline, so read-cache coherence needs no
// separate side channel. This file is the transport: framing, HELLO negotiation,
// errno mapping, and the full-duplex client mux. Message payload layouts live in
// blockmsg.go; the two endpoints are blockproxy.go (server) and blockserver.go
// (caller).

const (
	blockProtoVersion = 1
	// blockFrameHeader is the fixed header after the u32 length prefix:
	// op(1) + flags(1) + reserved(2) + reqID(8).
	blockFrameHeader = 12
	// blockMaxInflight bounds concurrent in-flight requests per mount (and thus
	// worst-case buffered data ~= maxInflight * chunkSize).
	blockMaxInflight = 64
	// blockFrameSlack is the header/metadata allowance a frame may carry beyond a
	// full data chunk (block index, seq, hash, counts…).
	blockFrameSlack = 4096
)

// Ops. Metadata ops mirror the p9.File surface the kernel front-end drives; the
// data ops (READ/WRITE/STATBLOCK) are the block-native, hash-inline core.
const (
	opHello byte = iota + 1
	opAttach
	opWalk
	opWalkGetAttr
	opGetAttr
	opSetAttr
	opOpen
	opRead
	opWrite
	opStatBlock
	opFSync
	opCreate
	opMkdir
	opSymlink
	opLink
	opRenameAt
	opUnlinkAt
	opReadlink
	opStatFS
	opReaddir
	opClunk
	// opReadRange is a demand-fill read (FeatSubBlockFill): fetch a sub-block-aligned
	// range [subOff, subOff+subLen) within one block, not the whole block.
	opReadRange
	// opBlockResp is the op stamped on every response frame; responses are matched
	// to requests by reqID, so the op is informational (the client ignores it).
	opBlockResp
)

// Frame flags.
const (
	flagFinal byte = 1 << 0 // last response frame for this reqID
	flagErr   byte = 1 << 1 // payload is a u32 linux errno
	flagEOF   byte = 1 << 2 // read reached end of file (errno-free success)
)

// Coherence-mode feature bits, exchanged in the HELLO `features` field and used
// only when BOTH endpoints advertise them (the negotiated set is local & peer, so
// a peer that predates a bit simply keeps the default full-block, per-write path).
// They change how a write keeps the server-side cache coherent — trading the
// 1 MiB read-back + hash every small write pays today (see blockserver.readBlock)
// for a cheaper scheme — without touching the read/transfer granularity.
const (
	// FeatSubBlockHash hashes only the touched sub-blocks (subBlockSize granularity)
	// of a write, not the whole enclosing chunk: a 4 KiB DB page write costs a
	// 4 KiB read+hash on the caller and a HashRange sub-block check on the proxy,
	// instead of 1 MiB of both. The 1 MiB transfer/readahead block is unchanged.
	FeatSubBlockHash uint32 = 1 << 0
	// FeatDeferHash defers coherence hashing from every WriteAt to FSync: writes
	// go through unhashed (WriteThrough) and the caller hashes each dirty unit ONCE
	// at fsync, reconciling the proxy cache in a batch. Composes with
	// FeatSubBlockHash (the dirty unit is then a sub-block, else a whole block).
	FeatDeferHash uint32 = 1 << 1
	// FeatSubBlockFill is demand-fill reads: a read miss fetches only the touched
	// sub-block range (aligned to subBlockSize) instead of the whole 1 MiB block,
	// and the cache tracks presence per sub-block. This cuts read amplification for
	// sparse/random access (a point query no longer drags in a whole block) while
	// the block stays the addressing unit; sequential access still fetches large
	// because the kernel issues large reads. It requires sub-block-granular
	// coherence, so it implies FeatSubBlockHash (see resolveBlockOpts).
	FeatSubBlockFill uint32 = 1 << 2

	// blockSupportedFeatures is what this build advertises; the negotiated set is
	// intersected with the requested features (WithBlockFeatures) and the peer's.
	blockSupportedFeatures = FeatSubBlockHash | FeatDeferHash | FeatSubBlockFill
)

// subBlockSize is the coherence granularity for FeatSubBlockHash. It matches a
// common DB page size and divides the default 1 MiB chunk evenly, so every
// sub-block lies wholly within one transfer block.
const subBlockSize int64 = 4096

// blockOpts configures ServeBlockProxy/ServeBlockServer; the zero value is the
// classic full-block, per-write coherence path (features negotiated to 0).
type blockOpts struct {
	features uint32
	// readahead caps the demand-fill ADAPTIVE prefetch window in bytes
	// (FeatSubBlockFill): a sequential read grows its fetch toward this cap so it
	// hits the cache next time, while a random read stays at pure demand (fetch
	// only the touched range). 0 = no readahead (always pure demand). Proxy-local;
	// not negotiated.
	readahead int64
}

// BlockOpt is a functional option for ServeBlockProxy / ServeBlockServer.
type BlockOpt func(*blockOpts)

// WithBlockFeatures requests the given coherence feature bits (FeatSubBlockHash /
// FeatDeferHash / FeatSubBlockFill). They take effect only if the peer also
// advertises them.
func WithBlockFeatures(f uint32) BlockOpt { return func(o *blockOpts) { o.features = f } }

// WithReadahead caps the demand-fill adaptive prefetch window (bytes): sequential
// reads grow toward it, random reads ignore it. Only matters under FeatSubBlockFill
// and only on the proxy (fetch) side.
func WithReadahead(w int64) BlockOpt { return func(o *blockOpts) { o.readahead = w } }

// BlockEnvOpts returns block-protocol options derived from the environment, for
// production call sites that want operator control without threading config:
//
//	CORNUS_BLOCK_COHERENCE  comma/space-separated: "subhash", "defer", "subfill"
//	                        (subfill implies subhash). Empty => classic path.
//	CORNUS_BLOCK_READAHEAD  demand-fill prefetch window, e.g. "64k", "262144" (0=off).
//
// BOTH endpoints must set the same coherence bits for a feature to negotiate on:
// the cornus server (ServeBlockProxy) and the deploy caller (ServeBlockServer).
// Readahead is proxy-side only (a no-op on the caller).
func BlockEnvOpts() []BlockOpt {
	var opts []BlockOpt
	if f := parseCoherenceEnv(os.Getenv("CORNUS_BLOCK_COHERENCE")); f != 0 {
		opts = append(opts, WithBlockFeatures(f))
	}
	if w := parseByteSizeEnv(os.Getenv("CORNUS_BLOCK_READAHEAD")); w > 0 {
		opts = append(opts, WithReadahead(w))
	}
	return opts
}

func parseCoherenceEnv(s string) uint32 {
	var f uint32
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		switch strings.ToLower(tok) {
		case "subhash", "subblock", "subblockhash":
			f |= FeatSubBlockHash
		case "defer", "deferhash":
			f |= FeatDeferHash
		case "subfill", "fill", "demandfill":
			f |= FeatSubBlockFill
		}
	}
	return f
}

func parseByteSizeEnv(s string) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n * mult
}

func resolveBlockOpts(opts []BlockOpt) blockOpts {
	var o blockOpts
	for _, opt := range opts {
		opt(&o)
	}
	o.features &= blockSupportedFeatures
	// Demand-fill needs sub-block-granular coherence (the cache holds partial
	// chunks, so a whole-chunk write self-verify is impossible); enforce the
	// dependency rather than silently mis-cache.
	if o.features&FeatSubBlockFill != 0 {
		o.features |= FeatSubBlockHash
	}
	return o
}

var errBlockClosed = errors.New("blockproto: connection closed")

// frame is one wire frame. Requests flow server->caller; a response echoes reqID
// and may span multiple frames (streamed), the last carrying flagFinal.
type frame struct {
	op      byte
	flags   byte
	reqID   uint64
	payload []byte
}

// writeFrame writes f. The caller MUST hold the connection's send lock so the
// header and payload are not interleaved with another frame.
func writeFrame(w io.Writer, f frame) error {
	var hdr [4 + blockFrameHeader]byte
	total := blockFrameHeader + len(f.payload)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(total))
	hdr[4] = f.op
	hdr[5] = f.flags
	// hdr[6:8] reserved = 0
	binary.BigEndian.PutUint64(hdr[8:16], f.reqID)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(f.payload) > 0 {
		if _, err := w.Write(f.payload); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads one frame, rejecting a payload larger than maxPayload.
func readFrame(r io.Reader, maxPayload int) (frame, error) {
	var hdr [4 + blockFrameHeader]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	total := int(binary.BigEndian.Uint32(hdr[0:4]))
	if total < blockFrameHeader {
		return frame{}, errors.New("blockproto: short frame")
	}
	plen := total - blockFrameHeader
	if plen > maxPayload {
		return frame{}, errors.New("blockproto: frame too large")
	}
	f := frame{op: hdr[4], flags: hdr[5], reqID: binary.BigEndian.Uint64(hdr[8:16])}
	if plen > 0 {
		f.payload = make([]byte, plen)
		if _, err := io.ReadFull(r, f.payload); err != nil {
			return frame{}, err
		}
	}
	return f, nil
}

// ---- errno mapping ----

// errnoOf maps a Go error to a linux errno for an ERR frame (0 => no error).
func errnoOf(err error) uint32 {
	if err == nil {
		return 0
	}
	return uint32(linux.ExtractErrno(err))
}

// errnoError turns an ERR frame's errno back into an error the p9 server will
// re-extract to the same errno for the kernel.
func errnoError(e uint32) error {
	if e == 0 {
		return errors.New("blockproto: remote error")
	}
	return linux.Errno(e)
}

// ---- payload cursor helpers ----

type msgW struct{ b []byte }

func (w *msgW) u8(v byte)    { w.b = append(w.b, v) }
func (w *msgW) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *msgW) u32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *msgW) u64(v uint64) { w.b = binary.BigEndian.AppendUint64(w.b, v) }
func (w *msgW) i64(v int64)  { w.u64(uint64(v)) }
func (w *msgW) boolean(v bool) {
	if v {
		w.u8(1)
	} else {
		w.u8(0)
	}
}
func (w *msgW) blob(p []byte) { w.u32(uint32(len(p))); w.b = append(w.b, p...) }
func (w *msgW) str(s string)  { w.u32(uint32(len(s))); w.b = append(w.b, s...) }

type msgR struct {
	b   []byte
	off int
	err error
}

func newMsgR(b []byte) *msgR { return &msgR{b: b} }

func (r *msgR) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.off+n > len(r.b) {
		r.err = io.ErrUnexpectedEOF
		return false
	}
	return true
}

func (r *msgR) u8() byte {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *msgR) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *msgR) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *msgR) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	v := binary.BigEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

func (r *msgR) i64() int64    { return int64(r.u64()) }
func (r *msgR) boolean() bool { return r.u8() != 0 }

func (r *msgR) blob() []byte {
	n := int(r.u32())
	if !r.need(n) {
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

func (r *msgR) str() string {
	n := int(r.u32())
	if !r.need(n) {
		return ""
	}
	v := string(r.b[r.off : r.off+n])
	r.off += n
	return v
}

func (r *msgR) error() error { return r.err }

// ---- HELLO negotiation ----

type helloParams struct {
	version     uint16
	chunkSize   uint32
	maxInflight uint16
	features    uint32
}

func (h helloParams) marshal() []byte {
	var w msgW
	w.u16(h.version)
	w.u32(h.chunkSize)
	w.u16(h.maxInflight)
	w.u32(h.features)
	return w.b
}

func unmarshalHello(p []byte) (helloParams, error) {
	r := newMsgR(p)
	h := helloParams{version: r.u16(), chunkSize: r.u32(), maxInflight: r.u16(), features: r.u32()}
	return h, r.error()
}

// blockClientHandshake (proxy side) writes our HELLO first, then reads the peer's;
// blockServerHandshake (caller side) reads first, then writes. Splitting the order
// by role means the exchange never deadlocks even on a synchronous transport (a
// symmetric write-then-read would). Both require an equal version and chunk size.
func blockClientHandshake(conn net.Conn, local helloParams) (helloParams, error) {
	if err := sendHello(conn, local); err != nil {
		return helloParams{}, err
	}
	return recvHello(conn, local)
}

func blockServerHandshake(conn net.Conn, local helloParams) (helloParams, error) {
	peer, err := recvHello(conn, local)
	if err != nil {
		return helloParams{}, err
	}
	return peer, sendHello(conn, local)
}

func sendHello(conn net.Conn, local helloParams) error {
	return writeFrame(conn, frame{op: opHello, flags: flagFinal, payload: local.marshal()})
}

func recvHello(conn net.Conn, local helloParams) (helloParams, error) {
	f, err := readFrame(conn, int(local.chunkSize)+blockFrameSlack)
	if err != nil {
		return helloParams{}, err
	}
	if f.op != opHello {
		return helloParams{}, errors.New("blockproto: expected HELLO")
	}
	peer, err := unmarshalHello(f.payload)
	if err != nil {
		return helloParams{}, err
	}
	if peer.version != local.version {
		return helloParams{}, errors.New("blockproto: version mismatch")
	}
	if peer.chunkSize != local.chunkSize {
		return helloParams{}, errors.New("blockproto: chunk size mismatch")
	}
	return peer, nil
}

// ---- client mux (used by the server-side block proxy) ----

type blockCall struct {
	frames []frame
	done   chan struct{}
	err    error
}

// blockClient is the full-duplex request mux the block proxy drives toward the
// caller: one send path (serialized), one reader goroutine routing response
// frames to per-reqID waiters, so many requests are in flight with out-of-order
// completion. A response is collected until its flagFinal frame, then delivered —
// which does not head-of-line-block other reqIDs (each is routed independently).
type blockClient struct {
	conn     net.Conn
	maxFrame int
	inflight chan struct{}
	features uint32 // negotiated coherence features (local & peer)

	sendMu sync.Mutex

	mu      sync.Mutex
	pending map[uint64]*blockCall
	nextID  atomic.Uint64
	readErr error
	closed  chan struct{}
	once    sync.Once
}

// newBlockClient performs the HELLO handshake and starts the reader loop.
func newBlockClient(conn net.Conn, local helloParams) (*blockClient, error) {
	peer, err := blockClientHandshake(conn, local)
	if err != nil {
		return nil, err
	}
	inflight := int(local.maxInflight)
	if inflight <= 0 {
		inflight = blockMaxInflight
	}
	bc := &blockClient{
		conn:     conn,
		maxFrame: int(local.chunkSize) + blockFrameSlack,
		inflight: make(chan struct{}, inflight),
		features: local.features & peer.features,
		pending:  map[uint64]*blockCall{},
		closed:   make(chan struct{}),
	}
	go bc.readLoop()
	return bc, nil
}

// do sends a request and returns its collected response frames (or the errno the
// caller reported). It blocks on the in-flight semaphore for backpressure and on
// ctx for cancellation: when ctx is cancelled (e.g. the mount is tearing down),
// do abandons the request — it removes its waiter so a late response frame is
// discarded, and returns ctx.Err(). The caller (block server) always completes an
// applied mutation regardless; only the reply is dropped. This is what lets a
// parked do release so the p9 server's Handle can drain in-flight handlers on
// teardown even if the caller endpoint is unresponsive.
func (bc *blockClient) do(ctx context.Context, op byte, payload []byte) ([]frame, error) {
	select {
	case bc.inflight <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-bc.closed:
		return nil, bc.err()
	}
	defer func() { <-bc.inflight }()

	id := bc.nextID.Add(1)
	call := &blockCall{done: make(chan struct{})}
	bc.mu.Lock()
	if bc.readErr != nil {
		bc.mu.Unlock()
		return nil, bc.readErr
	}
	bc.pending[id] = call
	bc.mu.Unlock()

	bc.sendMu.Lock()
	err := writeFrame(bc.conn, frame{op: op, reqID: id, payload: payload})
	bc.sendMu.Unlock()
	if err != nil {
		bc.mu.Lock()
		delete(bc.pending, id)
		bc.mu.Unlock()
		bc.fail(err)
		return nil, err
	}

	select {
	case <-call.done:
		return call.frames, call.err
	case <-ctx.Done():
		// Abandon the waiter; the readLoop drops any late/streaming frames for a
		// reqID no longer in pending.
		bc.mu.Lock()
		delete(bc.pending, id)
		bc.mu.Unlock()
		return nil, ctx.Err()
	case <-bc.closed:
		return nil, bc.err()
	}
}

func (bc *blockClient) readLoop() {
	for {
		f, err := readFrame(bc.conn, bc.maxFrame)
		if err != nil {
			bc.fail(err)
			return
		}
		bc.mu.Lock()
		call := bc.pending[f.reqID]
		if call == nil {
			// Unknown/cancelled reqID: drop the frame.
			bc.mu.Unlock()
			continue
		}
		switch {
		case f.flags&flagErr != 0:
			e := uint32(0)
			if len(f.payload) >= 4 {
				e = binary.BigEndian.Uint32(f.payload)
			}
			call.err = errnoError(e)
			delete(bc.pending, f.reqID)
			bc.mu.Unlock()
			close(call.done)
		case f.flags&flagFinal != 0:
			call.frames = append(call.frames, f)
			delete(bc.pending, f.reqID)
			bc.mu.Unlock()
			close(call.done)
		default:
			call.frames = append(call.frames, f)
			bc.mu.Unlock()
		}
	}
}

func (bc *blockClient) fail(err error) {
	bc.once.Do(func() {
		bc.mu.Lock()
		bc.readErr = err
		pending := bc.pending
		bc.pending = map[uint64]*blockCall{}
		bc.mu.Unlock()
		for _, call := range pending {
			call.err = err
			close(call.done)
		}
		close(bc.closed)
	})
}

func (bc *blockClient) err() error {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if bc.readErr != nil {
		return bc.readErr
	}
	return errBlockClosed
}

// Close tears down the client; in-flight calls fail with a closed error.
func (bc *blockClient) Close() error {
	err := bc.conn.Close()
	bc.fail(errBlockClosed)
	return err
}

// cancelConn wraps a net.Conn and invokes onGone exactly once when a Read returns
// an error (the peer went away / the conn broke) or when Close is called. The
// block proxy wraps the KERNEL-facing conn with it, tripping the mount context so
// that when the kernel unmounts, any blockClient.do parked on an unresponsive
// caller is released — which is what lets the p9 server's Handle drain its
// in-flight handlers (it blocks on pendingWg.Wait() in stop()) instead of hanging.
type cancelConn struct {
	net.Conn
	once sync.Once
	fn   func()
}

func newCancelConn(c net.Conn, onGone func()) *cancelConn {
	return &cancelConn{Conn: c, fn: onGone}
}

func (c *cancelConn) trip() { c.once.Do(c.fn) }

func (c *cancelConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.trip()
	}
	return n, err
}

func (c *cancelConn) Close() error {
	c.trip()
	return c.Conn.Close()
}
