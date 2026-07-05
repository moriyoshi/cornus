package wire

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/wire/qosab"
)

// The A/B for "what does the block protocol cost versus the raw 9P splice, when
// it buys nothing back in caching?" — the question e2e/benchmarks/bench-mount-modes.star
// answers on a real kernel mount, and which this answers in-process so a fix can
// be iterated on without the privileged runner.
//
// Both modes are wired with the SAME two hops the production path has, so the
// only difference is the protocol:
//
//	raw 9P: p9 client -[unix]- pipe() splice   -[yamux/link]- p9 server over the export
//	block:  p9 client -[unix]- ServeBlockProxy -[yamux/link]- ServeBlockServer over the export
//
// The p9 client stands in for the kernel's v9fs client and reproduces the one
// property of it that matters most: ReadAt/WriteAt are chunked at payloadSize
// (msize minus header), which is NOT a multiple of the 1 MiB block, so every
// request after the first straddles a block boundary.
//
// Two axes, because they fail differently. On "local" (a plain socket pair, no
// link) round trips are nearly free and what shows is CPU and allocation. On an
// emulated link, a difference in round-trip COUNT dominates and copies barely
// register. A change that helps one and hurts the other is not a fix.
//
//	go test ./pkg/wire/ -run XXX -bench BenchmarkMount -benchtime 5x

// benchMsize matches the msize the production mount uses (deploywire.Mount9P
// passes msize=1048576).
const benchMsize = 1 << 20

// mountProfiles is the link matrix for the server<->caller hop. "local" is the
// zero-value profile: no bandwidth limit and no delay, so it measures the
// protocols' CPU/allocation cost with round trips nearly free.
var mountProfiles = []struct {
	link  qosab.LinkProfile
	bytes int64 // sequential transfer size (WAN would take minutes at 16 MiB)
}{
	{qosab.LinkProfile{Name: "local"}, 16 << 20},
	{qosab.LinkProfile{Name: "LAN", BytesPerSec: 1e9 / 8, Latency: 200 * time.Microsecond}, 16 << 20},
	{qosab.LinkProfile{Name: "WAN", BytesPerSec: 100e6 / 8, Latency: 20 * time.Millisecond}, 4 << 20},
}

// sockPair returns a connected pair of unix-socket conns. Real sockets, not
// net.Pipe: net.Pipe is unbuffered and synchronous, which would turn every extra
// Write into a scheduler round trip and measure the harness instead of the code.
func sockPair(tb testing.TB) (net.Conn, net.Conn) {
	tb.Helper()
	dir := tb.TempDir()
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		tb.Fatal(err)
	}
	defer l.Close()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := l.Accept()
		ch <- res{c, err}
	}()
	c1, err := net.Dial("unix", sock)
	if err != nil {
		tb.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		tb.Fatal(r.err)
	}
	tb.Cleanup(func() { c1.Close(); r.c.Close() })
	return c1, r.c
}

// remoteHop returns the server-side and caller-side ends of the server<->caller
// hop. For the zero profile that is a plain socket pair; otherwise it is one
// yamux stream over an emulated link, which is what production runs (the block /
// 9P backing rides a tagged yamux stream inside the session's WebSocket).
//
// counted, when non-nil, receives a wrapper around the LOWEST conn of the hop —
// under yamux, not the stream — so its Read/Write tally means syscalls.
func remoteHop(tb testing.TB, p qosab.LinkProfile, counted **syscallConn) (net.Conn, net.Conn) {
	tb.Helper()
	if p.BytesPerSec == 0 && p.Latency == 0 {
		a, b := sockPair(tb)
		if counted != nil {
			sc := &syscallConn{Conn: a}
			*counted = sc
			return sc, b
		}
		return a, b
	}
	ca, cb := qosab.NewLink(p)
	if counted != nil {
		sc := &syscallConn{Conn: ca}
		*counted = sc
		ca = sc
	}
	srv, err := yamux.Client(ca, yamuxConfig())
	if err != nil {
		tb.Fatal(err)
	}
	cli, err := yamux.Server(cb, yamuxConfig())
	if err != nil {
		tb.Fatal(err)
	}
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := cli.Accept()
		ch <- res{c, err}
	}()
	s, err := srv.Open()
	if err != nil {
		tb.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		tb.Fatal(r.err)
	}
	tb.Cleanup(func() { srv.Close(); cli.Close(); ca.Close(); cb.Close() })
	return s, r.c
}

// mountMeters holds the accounting for one wired-up mount: the two hops' syscall
// counters and the export's file-op counter.
type mountMeters struct {
	kernelHop *syscallConn // kernel <-> server
	remoteHop *syscallConn // server <-> caller, below any mux
	files     *fileOpCounter
}

// benchClient builds a p9 client over one of the two mount modes, with every hop
// and the export metered.
func benchClient(tb testing.TB, dir string, block bool, p qosab.LinkProfile) (*p9.Client, *mountMeters) {
	tb.Helper()
	m := &mountMeters{files: &fileOpCounter{}}
	kc1, kc2raw := sockPair(tb) // kernel <-> server (always local)
	m.kernelHop = &syscallConn{Conn: kc2raw}
	var kc2 net.Conn = m.kernelHop
	rs1, rs2 := remoteHop(tb, p, &m.remoteHop)

	inner, err := writableConfinedAttacher(dir)
	if err != nil {
		tb.Fatal(err)
	}
	att := &countingAttacher{inner: inner, c: m.files}
	if block {
		go ServeBlockProxy(kc2, rs1, nil, "m") // nil cache == NO-CACHE mode
		go serveBlockServerFS(rs2, att, defaultBlockChunk)
	} else {
		go pipe(kc2, rs1)
		go func() { _ = p9.NewServer(att).Handle(rs2, rs2) }()
	}
	cl, err := p9.NewClient(kc1, p9.WithMessageSize(benchMsize))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { cl.Close() })
	return cl, m
}

// snapshot captures every counter at once.
func (m *mountMeters) snapshot() mountProfile {
	var p mountProfile
	if m.kernelHop != nil {
		p.kernel = m.kernelHop.stats()
	}
	if m.remoteHop != nil {
		p.remote = m.remoteHop.stats()
	}
	p.files = m.files.stats()
	p.blits = BlitStats()
	return p
}

type mountProfile struct {
	kernel, remote, files hopStats
	blits                 [numBlitKinds]int64
}

func (a mountProfile) sub(b mountProfile) mountProfile {
	out := mountProfile{kernel: a.kernel.sub(b.kernel), remote: a.remote.sub(b.remote), files: a.files.sub(b.files)}
	for i := range out.blits {
		out.blits[i] = a.blits[i] - b.blits[i]
	}
	return out
}

func (a mountProfile) add(b mountProfile) mountProfile {
	out := mountProfile{kernel: a.kernel.add(b.kernel), remote: a.remote.add(b.remote), files: a.files.add(b.files)}
	for i := range out.blits {
		out.blits[i] = a.blits[i] + b.blits[i]
	}
	return out
}

// report emits the accounting as benchmark metrics, per iteration.
//
// The names are terse because they sit in a benchmark line: `sys` is socket
// Read+Write calls (syscalls) across both hops, `wireB` the bytes the
// server<->caller hop carried, `fsys` the ReadAt/WriteAt count against the
// authoritative export (pread/pwrite), and `fileB` its bytes.
//
// Copy accounting (-tags blitprof) is reported ONLY for the block mode, and that
// is deliberate. The counters live at the block protocol's own copy sites; the raw
// 9P splice copies inside io.Copy and inside the p9 library, neither of which is
// instrumented. Emitting a 0 on those rows would read as "measured, copies
// nothing" when it means "not measured" — so the column is simply absent there.
// Do not compare a blit number against a missing one.
func (a mountProfile) report(b *testing.B, block bool) {
	per := func(v int64) float64 { return float64(v) / float64(b.N) }
	sys := a.kernel.reads + a.kernel.writes + a.remote.reads + a.remote.writes
	b.ReportMetric(per(sys), "sys/op")
	b.ReportMetric(per(a.remote.rbytes+a.remote.wbytes), "wireB/op")
	b.ReportMetric(per(a.files.reads+a.files.writes), "fsys/op")
	b.ReportMetric(per(a.files.rbytes+a.files.wbytes), "fileB/op")
	if BlitProfiling && block {
		for i, v := range a.blits {
			b.ReportMetric(per(v), "blit-"+blitNames[i]+"B/op")
		}
	}
}

func benchRoot(tb testing.TB, cl *p9.Client) p9.File {
	tb.Helper()
	root, err := cl.Attach("")
	if err != nil {
		tb.Fatal(err)
	}
	return root
}

// benchSeqWrite writes total bytes in 1 MiB WriteAt calls — the dd bs=1M shape.
func benchSeqWrite(b *testing.B, block bool, p qosab.LinkProfile, total int64) {
	dir := b.TempDir()
	buf := make([]byte, 1<<20)
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	var acc mountProfile
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cl, meters := benchClient(b, dir, block, p)
		root := benchRoot(b, cl)
		// ReadWrite, not WriteOnly: v9fs opens a read-write writeback fid, and the
		// classic coherence path reads the block back through this same fid.
		f, _, _, err := root.Create("seq", p9.ReadWrite, 0o644, p9.UID(os.Getuid()), p9.GID(os.Getgid()))
		if err != nil {
			b.Fatal(err)
		}
		before := meters.snapshot()
		b.StartTimer()
		for off := int64(0); off < total; off += int64(len(buf)) {
			if _, err := f.WriteAt(buf, off); err != nil {
				b.Fatal(err)
			}
		}
		if err := f.FSync(); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		acc = acc.add(meters.snapshot().sub(before))
		f.Close()
		os.Remove(filepath.Join(dir, "seq"))
		b.StartTimer()
	}
	acc.report(b, block)
}

// benchSeqRead reads a pre-existing file back in 1 MiB ReadAt calls.
func benchSeqRead(b *testing.B, block bool, p qosab.LinkProfile, total int64) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seq"), make([]byte, total), 0o644); err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	var acc mountProfile
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cl, meters := benchClient(b, dir, block, p)
		root := benchRoot(b, cl)
		_, f, err := root.Walk([]string{"seq"})
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err := f.Open(p9.ReadOnly); err != nil {
			b.Fatal(err)
		}
		before := meters.snapshot()
		b.StartTimer()
		for off := int64(0); off < total; off += int64(len(buf)) {
			if _, err := f.ReadAt(buf, off); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		acc = acc.add(meters.snapshot().sub(before))
		f.Close()
		b.StartTimer()
	}
	acc.report(b, block)
}

// benchSmallSync is the write-intensive-DB / dd conv=fsync shape: per iteration a
// fresh walk+open, one small write, an fsync and a clunk. Per-op round trips
// dominate, so a protocol that spends an EXTRA round trip per open shows up here
// and nowhere else.
func benchSmallSync(b *testing.B, block bool, p qosab.LinkProfile) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "w"), make([]byte, 4096), 0o644); err != nil {
		b.Fatal(err)
	}
	page := make([]byte, 4096)
	cl, meters := benchClient(b, dir, block, p)
	root := benchRoot(b, cl)
	b.ReportAllocs()
	b.ResetTimer()
	before := meters.snapshot()
	for i := 0; i < b.N; i++ {
		_, f, err := root.Walk([]string{"w"})
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err := f.Open(p9.ReadWrite); err != nil {
			b.Fatal(err)
		}
		if _, err := f.WriteAt(page, 0); err != nil {
			b.Fatal(err)
		}
		if err := f.FSync(); err != nil {
			b.Fatal(err)
		}
		f.Close()
	}
	b.StopTimer()
	meters.snapshot().sub(before).report(b, block)
}

// BenchmarkMount is the whole matrix: profile x workload x mode. Read the pairs
// (raw9p, block) within one profile/workload group; across profiles the absolute
// numbers are not comparable.
func BenchmarkMount(b *testing.B) {
	for _, prof := range mountProfiles {
		for _, w := range []struct {
			name string
			run  func(b *testing.B, block bool)
		}{
			{"seq-write", func(b *testing.B, block bool) { benchSeqWrite(b, block, prof.link, prof.bytes) }},
			{"seq-read", func(b *testing.B, block bool) { benchSeqRead(b, block, prof.link, prof.bytes) }},
			{"small-sync", func(b *testing.B, block bool) { benchSmallSync(b, block, prof.link) }},
		} {
			for _, mode := range []struct {
				name  string
				block bool
			}{{"raw9p", false}, {"block", true}} {
				b.Run(fmt.Sprintf("%s/%s/%s", prof.link.Name, w.name, mode.name), func(b *testing.B) {
					w.run(b, mode.block)
				})
			}
		}
	}
}
