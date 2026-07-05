package wire

import (
	"net"
	"sync/atomic"
	"testing"

	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/p9"
)

// Accounting the mount benchmarks report alongside wall time, so a change can be
// attributed rather than guessed at. Three quantities matter here and none of them
// is visible in ns/op:
//
//   - SYSCALLS. Every Read/Write on a socket is one. A protocol that moves the same
//     bytes in more calls pays for it on every one, and the cost does not show up on
//     a local socket pair the way it does on a loaded host.
//   - FILE OPS. Reads and writes against the authoritative export, i.e. real
//     pread/pwrite. This is what says whether an optimization moved work off the
//     disk or only shuffled it inside the process — the 1 MiB coherence read-back
//     per write was invisible in ns/op on a warm page cache and obvious here.
//   - COPIES. See blitcount.go; opt in with -tags blitprof.
//
// Counters are wrappers rather than sampling, so they are exact, and they are all
// in _test.go files: nothing here is compiled into cornus.

// syscallConn counts Read/Write calls and bytes on one connection. Wrap the
// LOWEST conn of a hop — under yamux, not the stream — for the count to mean
// syscalls rather than buffer operations.
//
// The obvious objection is that wrapping hides the concrete type, so io.Copy can
// no longer take a ReaderFrom/WriterTo fast path — which would slow the raw 9P
// splice and bias the comparison toward the block protocol. Measured rather than
// argued: raw 9P over local sockets, three runs each, wrapped vs unwrapped, came
// out at 949 vs 952 MB/s (write) and 2223 vs 2173 MB/s (read). No fast path is
// being lost, because *net.UnixConn's ReadFrom is the packet-oriented
// ReadFrom(b []byte) (int, Addr, error) and cannot also satisfy io.ReaderFrom.
// Re-check this if the harness ever moves to TCP, where *net.TCPConn DOES
// implement io.ReaderFrom and splices.
type syscallConn struct {
	net.Conn
	reads, writes  atomic.Int64
	rbytes, wbytes atomic.Int64
}

func (c *syscallConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.reads.Add(1)
	c.rbytes.Add(int64(n))
	return n, err
}

func (c *syscallConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.writes.Add(1)
	c.wbytes.Add(int64(n))
	return n, err
}

// hopStats is one hop's totals.
type hopStats struct{ reads, writes, rbytes, wbytes int64 }

func (c *syscallConn) stats() hopStats {
	return hopStats{c.reads.Load(), c.writes.Load(), c.rbytes.Load(), c.wbytes.Load()}
}

func (a hopStats) sub(b hopStats) hopStats {
	return hopStats{a.reads - b.reads, a.writes - b.writes, a.rbytes - b.rbytes, a.wbytes - b.wbytes}
}

func (a hopStats) add(b hopStats) hopStats {
	return hopStats{a.reads + b.reads, a.writes + b.writes, a.rbytes + b.rbytes, a.wbytes + b.wbytes}
}

// ---- file-op accounting at the authoritative export ----

// countingAttacher wraps an attacher so every ReadAt/WriteAt against the export is
// counted. It counts the ops the block protocol drives, which are the ones that
// become pread/pwrite on the caller's filesystem.
type countingAttacher struct {
	inner p9.Attacher
	c     *fileOpCounter
}

type fileOpCounter struct {
	reads, writes  atomic.Int64
	rbytes, wbytes atomic.Int64
}

func (c *fileOpCounter) stats() hopStats {
	return hopStats{c.reads.Load(), c.writes.Load(), c.rbytes.Load(), c.wbytes.Load()}
}

func (a *countingAttacher) Attach() (p9.File, error) {
	f, err := a.inner.Attach()
	if err != nil {
		return nil, err
	}
	return &countingFile{File: f, c: a.c}, nil
}

// countingFile forwards every p9.File method to the wrapped file, counting the
// data ops. It embeds p9.File rather than templatefs.NoopFile for forwarding, and
// re-wraps the files that Walk/WalkGetAttr/Create hand back so a descendant is
// counted too — without that, every counted op would be missed, since the ops that
// matter all happen on a file reached by walking.
type countingFile struct {
	templatefs.NoopFile
	p9.File
	c *fileOpCounter
}

func (f *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(p, off)
	f.c.reads.Add(1)
	f.c.rbytes.Add(int64(n))
	return n, err
}

func (f *countingFile) WriteAt(p []byte, off int64) (int, error) {
	n, err := f.File.WriteAt(p, off)
	f.c.writes.Add(1)
	f.c.wbytes.Add(int64(n))
	return n, err
}

func (f *countingFile) Walk(names []string) ([]p9.QID, p9.File, error) {
	qids, c, err := f.File.Walk(names)
	if err != nil {
		return nil, nil, err
	}
	return qids, &countingFile{File: c, c: f.c}, nil
}

func (f *countingFile) WalkGetAttr(names []string) ([]p9.QID, p9.File, p9.AttrMask, p9.Attr, error) {
	qids, c, mask, attr, err := f.File.WalkGetAttr(names)
	if err != nil {
		return nil, nil, p9.AttrMask{}, p9.Attr{}, err
	}
	return qids, &countingFile{File: c, c: f.c}, mask, attr, nil
}

func (f *countingFile) Create(name string, flags p9.OpenFlags, perm p9.FileMode, uid p9.UID, gid p9.GID) (p9.File, p9.QID, uint32, error) {
	c, qid, iounit, err := f.File.Create(name, flags, perm, uid, gid)
	if err != nil {
		return nil, p9.QID{}, 0, err
	}
	return &countingFile{File: c, c: f.c}, qid, iounit, nil
}

// TestBlitAccountingCountsWhatItClaims validates the copy instrument and, in the
// same assertions, pins the property it exists to protect: the block protocol's
// data path lands each payload EXACTLY ONCE per direction and copies it no further.
//
// An instrument nobody checks is worse than none, because optimization work is
// steered by it. Two failures this would catch: a counter that stopped counting
// (wire-read collapses toward zero and every future change looks free), and a
// reintroduced copy — a request payload staged into a msgW, or a read reply copied
// out of a frame buffer instead of landing in the caller's — which shows up as
// msg-append or user-copy going non-trivial.
//
// Requires -tags blitprof; without it the counters are compiled out.
func TestBlitAccountingCountsWhatItClaims(t *testing.T) {
	if !BlitProfiling {
		t.Skip("build with -tags blitprof to check the copy accounting")
	}
	const total = 4 << 20
	dir := t.TempDir()
	cl, _ := countingHarness(t, dir, nil)
	root := attachRoot(t, cl)
	f, _, _, err := root.Create("f", p9.ReadWrite, 0o644, p9.UID(0), p9.GID(0))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)

	ResetBlits()
	for off := int64(0); off < total; off += int64(len(buf)) {
		if _, err := f.WriteAt(buf, off); err != nil {
			t.Fatal(err)
		}
	}
	for off := int64(0); off < total; off += int64(len(buf)) {
		if _, err := f.ReadAt(buf, off); err != nil {
			t.Fatal(err)
		}
	}
	got := BlitStats()

	// One landing per direction: the write's payload lands at the caller, the
	// read's lands in the buffer the p9 server will hand back. Headers and metadata
	// add a little on top, hence the slack — but nothing close to another pass.
	const slack = 256 << 10
	if wire := got[blitWireRead]; wire < 2*total || wire > 2*total+slack {
		t.Errorf("wire-read = %d bytes for %d written + %d read; want ~%d (one landing each way)",
			wire, total, total, 2*total)
	}
	// Everything else must stay in the noise: a payload-sized number in any of
	// these is a copy that came back.
	if got[blitUserCopy] != 0 {
		t.Errorf("user-copy = %d bytes, want 0: reads should land in the caller's buffer, not be copied into it", got[blitUserCopy])
	}
	if got[blitMsgAppend] > slack {
		t.Errorf("msg-append = %d bytes: a payload is being staged into a message again", got[blitMsgAppend])
	}
	if got[blitFrameStage] > slack {
		t.Errorf("frame-stage = %d bytes: bulk is being copied into the frame buffer instead of written from the caller's slice", got[blitFrameStage])
	}
}

// TestCountingFileForwards guards the wrapper itself: an embedded p9.File plus an
// embedded NoopFile is a shape where a missed override silently becomes a no-op
// (NoopFile answers everything), so a wrapper that stopped forwarding would make
// the benchmarks report zero file ops and look like a win.
func TestCountingFileForwards(t *testing.T) {
	dir := t.TempDir()
	inner, err := writableConfinedAttacher(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &fileOpCounter{}
	root, err := (&countingAttacher{inner: inner, c: c}).Attach()
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err := root.Create("f", p9.ReadWrite, 0o644, p9.UID(0), p9.GID(0))
	if err != nil {
		t.Fatalf("create through the wrapper: %v", err)
	}
	if _, err := f.WriteAt([]byte("hello"), 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 5)
	if n, err := f.ReadAt(buf, 0); err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("read = %q, %v; want \"hello\"", buf[:n], err)
	}
	got := c.stats()
	if got.reads != 1 || got.writes != 1 || got.rbytes != 5 || got.wbytes != 5 {
		t.Fatalf("counted %+v, want 1 read / 1 write / 5 bytes each", got)
	}
	// And a walked descendant must still be counted, not just the created one.
	_, w, err := root.Walk([]string{"f"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Open(p9.ReadOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if c.stats().reads != 2 {
		t.Fatalf("a walked file's reads are not counted: %+v", c.stats())
	}
}
