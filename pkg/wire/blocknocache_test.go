package wire

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
)

// Coverage for NO-CACHE mode (ServeBlockProxy with a nil cache) and for the
// round-trip count of the block protocol's metadata path. Both were previously
// unexercised: no test constructed a nil-cache proxy at all, and nothing observed
// how many caller round trips one kernel operation costs — which is precisely
// where this protocol was losing to the raw 9P splice.

// opCounterConn decodes the frame stream written through it and counts frames by
// op, so a test can assert how many round trips an operation costs. It decodes
// the length prefix rather than sniffing bytes, so a payload byte can never be
// miscounted as an op.
type opCounterConn struct {
	net.Conn
	mu      sync.Mutex
	counts  map[byte]int
	hdr     []byte
	pending int
}

func newOpCounter(c net.Conn) *opCounterConn {
	return &opCounterConn{Conn: c, counts: map[byte]int{}}
}

func (c *opCounterConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	for p := b; len(p) > 0; {
		if c.pending > 0 {
			n := min(c.pending, len(p))
			c.pending -= n
			p = p[n:]
			continue
		}
		c.hdr = append(c.hdr, p[0])
		p = p[1:]
		if len(c.hdr) == 5 {
			c.counts[c.hdr[4]]++
			c.pending = int(binary.BigEndian.Uint32(c.hdr[0:4])) - 1
			c.hdr = c.hdr[:0]
		}
	}
	c.mu.Unlock()
	return c.Conn.Write(b)
}

func (c *opCounterConn) reset() {
	c.mu.Lock()
	c.counts = map[byte]int{}
	c.mu.Unlock()
}

func (c *opCounterConn) get(op byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[op]
}

func (c *opCounterConn) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.counts {
		n += v
	}
	return n
}

// countingHarness is blockHarness with the request stream counted. cache may be
// nil, which selects NO-CACHE mode.
func countingHarness(t *testing.T, dir string, cache *blockcache.Cache) (*p9.Client, *opCounterConn) {
	t.Helper()
	kc1, kc2 := net.Pipe()
	rs1, rs2 := net.Pipe()
	chunk := int64(defaultBlockChunk)
	if cache != nil {
		chunk = cache.ChunkSize()
	}
	counter := newOpCounter(rs1)
	go ServeBlockProxy(kc2, counter, cache, "m")
	go ServeBlockServer(rs2, dir, chunk)
	cl, err := p9.NewClient(kc1, p9.WithMessageSize(1<<20))
	if err != nil {
		t.Fatalf("p9 client: %v", err)
	}
	t.Cleanup(func() {
		cl.Close()
		kc1.Close()
		kc2.Close()
		rs1.Close()
		rs2.Close()
	})
	return cl, counter
}

func attachRoot(t *testing.T, cl *p9.Client) p9.File {
	t.Helper()
	root, err := cl.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	return root
}

// TestNoCacheNegotiatesFeatNoCache pins who decides the mode. FeatNoCache must be
// derived from whether the proxy holds a cache, and must NOT be requestable
// through the options / environment surface: a proxy that advertised it while
// holding a cache would stop that cache ever being validated.
func TestNoCacheNegotiatesFeatNoCache(t *testing.T) {
	if got := resolveBlockOpts([]BlockOpt{WithBlockFeatures(FeatNoCache)}).features; got&FeatNoCache != 0 {
		t.Fatalf("WithBlockFeatures(FeatNoCache) resolved to %b: the mode must not be requestable", got)
	}
	if got := parseCoherenceEnv("nocache,no-cache,featnocache"); got&FeatNoCache != 0 {
		t.Fatalf("parseCoherenceEnv turned on FeatNoCache (%b): the mode must not be settable by an operator", got)
	}

	// The caller advertises the capability unconditionally, so the intersection is
	// decided by the proxy alone.
	for _, tc := range []struct {
		name  string
		cache *blockcache.Cache
		want  bool
	}{
		{"nil cache => no-cache mode", nil, true},
		{"with cache => coherence stays on", blockcache.New(blockcache.NewMemStore(1<<26), 1<<20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kc1, kc2 := net.Pipe()
			rs1, rs2 := net.Pipe()
			defer func() { kc1.Close(); kc2.Close(); rs1.Close(); rs2.Close() }()
			go ServeBlockProxy(kc2, rs1, tc.cache, "m")

			// Stand in for the caller: complete the handshake and read the negotiated set.
			local := helloParams{version: blockProtoVersion, chunkSize: defaultBlockChunk, maxInflight: blockMaxInflight, features: FeatNoCache}
			if tc.cache != nil {
				local.chunkSize = uint32(tc.cache.ChunkSize())
			}
			peer, err := blockServerHandshake(rs2, local)
			if err != nil {
				t.Fatalf("handshake: %v", err)
			}
			if got := local.features&peer.features&FeatNoCache != 0; got != tc.want {
				t.Fatalf("negotiated FeatNoCache = %v, want %v (proxy advertised %b)", got, tc.want, peer.features)
			}
		})
	}
}

// TestNoCacheWriteDoesNotReadBack is the behavioural proof that no-cache mode
// skips coherence hashing: with hashing on, a write that does not cover the whole
// valid block makes the caller READ the block back to hash it, and a file the
// workload can only write cannot be read back at all.
//
// The discriminator is a file that is write-only by PERMISSION (0200), so the
// caller's read-only-clone fallback cannot rescue it either. Under FeatNoCache
// nothing reads, so the write succeeds; if the feature stopped negotiating, the
// second write would fail EBADF/EACCES.
func TestNoCacheWriteDoesNotReadBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test uses to make the file unreadable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "wo")
	if err := os.WriteFile(path, nil, 0o200); err != nil {
		t.Fatal(err)
	}
	cl, _ := countingHarness(t, dir, nil)
	root := attachRoot(t, cl)
	_, f, err := root.Walk([]string{"wo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Open(p9.WriteOnly); err != nil {
		t.Fatalf("open write-only: %v", err)
	}
	page := bytes.Repeat([]byte{'a'}, 4096)
	if _, err := f.WriteAt(page, 0); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// The second write extends the file, so it covers only part of block 0's valid
	// range — the case that needs a read-back when coherence hashing is on.
	if _, err := f.WriteAt(page, 4096); err != nil {
		t.Fatalf("second write (the one that would need a read-back): %v", err)
	}
	if err := f.FSync(); err != nil {
		t.Fatalf("fsync: %v", err)
	}
	f.Close()

	got, err := os.ReadFile(path)
	if err != nil {
		// Unreadable by permission is expected; check the length instead.
		st, serr := os.Stat(path)
		if serr != nil {
			t.Fatal(serr)
		}
		if st.Size() != 8192 {
			t.Fatalf("file size = %d, want 8192", st.Size())
		}
		return
	}
	if len(got) != 8192 {
		t.Fatalf("file len = %d, want 8192", len(got))
	}
}

// TestWriteOnlyPartialWriteWorksWhenCached is the same shape with the cache ON,
// where the read-back is genuinely needed. The caller opens a read-only clone of
// its own handle to satisfy it; without that, an ordinary append to a file the
// workload opened O_WRONLY (dd, a shell `>` redirect) failed EBADF on the SECOND
// write — the first covers the whole valid block and needs no read-back, which is
// why one write was not enough to see it.
func TestWriteOnlyPartialWriteWorksWhenCached(t *testing.T) {
	dir := t.TempDir()
	cache := blockcache.New(blockcache.NewMemStore(1<<26), 1<<20)
	cl, _ := countingHarness(t, dir, cache)
	root := attachRoot(t, cl)
	f, _, _, err := root.Create("wo", p9.WriteOnly, 0o644, p9.UID(os.Getuid()), p9.GID(os.Getgid()))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	page := bytes.Repeat([]byte{'z'}, 4096)
	if _, err := f.WriteAt(page, 0); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := f.WriteAt(page, 4096); err != nil {
		t.Fatalf("second write: %v", err)
	}
	f.Close()

	got, err := os.ReadFile(filepath.Join(dir, "wo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8192 || !bytes.Equal(got[4096:], page) {
		t.Fatalf("file = %d bytes, tail equal = %v; want 8192 with the second page intact",
			len(got), bytes.Equal(got[4096:], page))
	}
}

// TestNoCacheWriteCausesNoFileReads pins the write path's FILE-I/O amplification
// at the authoritative export, which is the direct form of the cost the coherence
// read-back used to impose: hashing a straddling write read a whole 1 MiB block
// back off disk to produce a number no-cache mode discards.
//
// It asserts on bytes touched rather than on wall time because the read-back was
// invisible in wall time on a warm page cache — the reason it survived so long.
// Exactly zero file reads is the honest bound here: nothing on this path has any
// reason to read a file the workload is writing.
func TestNoCacheWriteCausesNoFileReads(t *testing.T) {
	dir := t.TempDir()
	counter := &fileOpCounter{}
	inner, err := writableConfinedAttacher(dir)
	if err != nil {
		t.Fatal(err)
	}
	kc1, kc2 := net.Pipe()
	rs1, rs2 := net.Pipe()
	go ServeBlockProxy(kc2, rs1, nil, "m")
	go serveBlockServerFS(rs2, &countingAttacher{inner: inner, c: counter}, defaultBlockChunk)
	cl, err := p9.NewClient(kc1, p9.WithMessageSize(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close(); kc1.Close(); kc2.Close(); rs1.Close(); rs2.Close() })

	root := attachRoot(t, cl)
	f, _, _, err := root.Create("seq", p9.ReadWrite, 0o644, p9.UID(os.Getuid()), p9.GID(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	// 4 MiB in 1 MiB calls. The p9 client chunks at payloadSize (msize minus
	// header), which is NOT a multiple of the 1 MiB block, so every call after the
	// first straddles a block boundary — the case that forced a read-back.
	const total = 4 << 20
	buf := make([]byte, 1<<20)
	for off := int64(0); off < total; off += int64(len(buf)) {
		if _, err := f.WriteAt(buf, off); err != nil {
			t.Fatalf("write at %d: %v", off, err)
		}
	}
	if err := f.FSync(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got := counter.stats()
	if got.rbytes != 0 || got.reads != 0 {
		t.Fatalf("writing %d bytes read %d bytes back off the file in %d reads; want none — "+
			"the coherence read-back is back on a path with no cache to keep coherent",
			total, got.rbytes, got.reads)
	}
	if got.wbytes != total {
		t.Fatalf("wrote %d bytes to the file, want %d", got.wbytes, total)
	}
}

// TestNoCacheRoundTripBudget pins the round-trip COST of the metadata path,
// because that is what the protocol was losing on and nothing else observes it.
// Every count here was higher before:
//
//	walk  1 was 3 — WalkGetAttr returned ENOSYS to the proxy and p9's server then
//	              fell back to Walk + GetAttr, each its own round trip.
//	open  1 was 2 — the proxy issued a GetAttr whose result no-cache mode discards.
//
// A regression shows up as a bigger number, which is exactly the failure mode a
// latency-blind test would miss.
func TestNoCacheRoundTripBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	cl, counter := countingHarness(t, dir, nil)
	root := attachRoot(t, cl)

	counter.reset()
	_, f, err := root.Walk([]string{"f"})
	if err != nil {
		t.Fatal(err)
	}
	if got := counter.total(); got != 1 {
		t.Fatalf("walk cost %d round trips, want 1 (walkgetattr %d, walk %d, getattr %d)",
			got, counter.get(opWalkGetAttr), counter.get(opWalk), counter.get(opGetAttr))
	}

	counter.reset()
	if _, _, err := f.Open(p9.ReadWrite); err != nil {
		t.Fatal(err)
	}
	if got := counter.total(); got != 1 {
		t.Fatalf("open cost %d round trips, want 1 (open %d, getattr %d)",
			got, counter.get(opOpen), counter.get(opGetAttr))
	}

	counter.reset()
	if _, err := f.WriteAt(make([]byte, 4096), 0); err != nil {
		t.Fatal(err)
	}
	if got := counter.total(); got != 1 {
		t.Fatalf("write cost %d round trips, want 1", got)
	}
	f.Close()
}

// TestNoCacheReadDoesNotSplitAtBlockBoundary covers the other round-trip leak: a
// read crossing a block boundary used to be clamped to the end of the covering
// block, so the client got a short read and re-issued for the remainder — an
// extra round trip per block for any sequential reader, since the kernel reads at
// msize granularity, which is not a multiple of the block.
func TestNoCacheReadDoesNotSplitAtBlockBoundary(t *testing.T) {
	dir := t.TempDir()
	const size = 3 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cl, counter := countingHarness(t, dir, nil)
	root := attachRoot(t, cl)
	_, f, err := root.Walk([]string{"f"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Open(p9.ReadOnly); err != nil {
		t.Fatal(err)
	}

	// A read straddling the 1 MiB boundary: 8 KiB starting 4 KiB before it.
	const off = defaultBlockChunk - 4096
	buf := make([]byte, 8192)
	counter.reset()
	n, err := f.ReadAt(buf, off)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != len(buf) {
		t.Fatalf("read %d bytes, want %d", n, len(buf))
	}
	if !bytes.Equal(buf, data[off:off+int64(len(buf))]) {
		t.Fatal("straddling read returned the wrong bytes")
	}
	if got := counter.get(opReadRange); got != 1 {
		t.Fatalf("a read across a block boundary cost %d READRANGE round trips, want 1", got)
	}
}

// TestNoCacheReadWriteRoundTrip exercises the direct-delivery read path (the
// reply's bytes are read off the connection straight into the p9 server's buffer)
// across the shapes where an off-by-one in the metadata/bulk split would show:
// sizes either side of the inline-staging threshold, an offset inside a block, a
// short read at EOF, and a read entirely past EOF.
func TestNoCacheReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cl, _ := countingHarness(t, dir, nil)
	root := attachRoot(t, cl)
	f, _, _, err := root.Create("f", p9.ReadWrite, 0o644, p9.UID(os.Getuid()), p9.GID(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}

	const size = (2 << 20) + 12345
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i*7 + 1)
	}
	for off := 0; off < size; {
		n, err := f.WriteAt(want[off:min(off+(1<<20), size)], int64(off))
		if err != nil {
			t.Fatalf("write at %d: %v", off, err)
		}
		off += n
	}

	for _, tc := range []struct {
		name     string
		off, len int64
	}{
		{"tiny (staged inline)", 0, 7},
		{"at the inline threshold", 1000, smallBulkInline},
		{"just over it", 1000, smallBulkInline + 1},
		{"inside a block", 1<<20 + 5000, 64 << 10},
		{"across a block boundary", 1<<20 - 100, 4096},
		{"short at EOF", size - 100, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.len)
			n, err := f.ReadAt(buf, tc.off)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			exp := want[tc.off:min(tc.off+tc.len, int64(size))]
			if int64(n) != int64(len(exp)) {
				t.Fatalf("read %d bytes, want %d", n, len(exp))
			}
			if !bytes.Equal(buf[:n], exp) {
				t.Fatalf("read at %d returned the wrong bytes", tc.off)
			}
		})
	}

	// Entirely past EOF: the p9 client turns a zero-length reply into io.EOF.
	if n, err := f.ReadAt(make([]byte, 4096), size+1<<20); n != 0 || err == nil {
		t.Fatalf("read past EOF = (%d, %v), want (0, EOF)", n, err)
	}
	f.Close()

	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file on disk differs (%d bytes, want %d)", len(got), len(want))
	}
}

// TestWriteFramePartsMatchesWholeFrame pins the wire format across the
// metadata/bulk split: the bytes must be exactly what a peer that predates the
// split expects.
//
// The expectation is spelled out here rather than taken from writeFrame, because
// writeFrame now DELEGATES to writeFrameParts — comparing them would compare the
// function against itself and pass however the layout was mangled. (It did: an
// injected stray byte kept both sides equal.)
func TestWriteFramePartsMatchesWholeFrame(t *testing.T) {
	for _, n := range []int{0, 1, smallBulkInline - 1, smallBulkInline, smallBulkInline + 1, 1 << 16} {
		meta := []byte{1, 2, 3, 4, 5}
		bulk := bytes.Repeat([]byte{'x'}, n)

		var want bytes.Buffer
		want.Write(binary.BigEndian.AppendUint32(nil, uint32(blockFrameHeader+len(meta)+n)))
		want.WriteByte(opWrite)
		want.WriteByte(flagFinal)
		want.Write([]byte{0, 0}) // reserved
		want.Write(binary.BigEndian.AppendUint64(nil, 42))
		want.Write(meta)
		want.Write(bulk)

		var got bytes.Buffer
		if err := writeFrameParts(&got, frame{op: opWrite, flags: flagFinal, reqID: 42, payload: meta}, bulk); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("bulk=%d: framing differs from the wire layout (got %d bytes, want %d)",
				n, got.Len(), want.Len())
		}
		// And it must decode back to the concatenation.
		f, err := readFrame(bytes.NewReader(got.Bytes()), 1<<20)
		if err != nil {
			t.Fatalf("bulk=%d: readFrame: %v", n, err)
		}
		if f.op != opWrite || f.flags != flagFinal || f.reqID != 42 ||
			!bytes.Equal(f.payload, append(append([]byte{}, meta...), bulk...)) {
			t.Fatalf("bulk=%d: decoded frame does not round-trip", n)
		}
	}
}
