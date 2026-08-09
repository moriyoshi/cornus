package wire

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/wire/qosab"
)

// The production transport for the mount benchmarks: yamux inside a real
// WebSocket, which is what `pkg/wire/session.go` builds and what every remote
// mount actually runs on.
//
// This exists because the rest of the matrix runs yamux over bare sockets, and
// that hides the layer with the largest per-byte cost. coder/websocket masks
// CLIENT writes in chunks bounded by its own 4 KiB bufio, flushing once per
// chunk (write.go writeFramePayload), so the side that dials pays roughly
// ceil(bytes/4096) write syscalls no matter how the bytes are framed. In
// production the side that dials is the DEPLOY CALLER (pkg/deploywire/serve.go
// dials and then serves the export), so that cost lands on read replies — the
// bulk direction for a container reading its mount.
//
// Getting the two ends the right way round is therefore the whole point. The one
// pre-existing in-process WebSocket harness, TestBlockRTTOverWebsocketYamux in
// blocklatency_test.go, serves the export from the WS SERVER side, whose writes
// are unmasked — which is why this cost has never shown up in-process before.

// connListener is a net.Listener that hands out conns given to it, so an
// http.Server can be driven over a socket pair or an emulated link instead of
// loopback TCP.
//
// TCP is avoided deliberately. mountcount_test.go records a measurement that the
// syscall counters do not bias the raw 9P side BECAUSE *net.UnixConn cannot
// satisfy io.ReaderFrom; *net.TCPConn can, and splices, so moving this harness to
// TCP would silently invalidate that argument. Feeding the listener directly also
// lets the WebSocket compose with the qosab link profiles.
type connListener struct {
	mu     sync.Mutex
	conns  []net.Conn
	closed chan struct{}
	once   sync.Once
}

func newConnListener(conns ...net.Conn) *connListener {
	return &connListener{conns: conns, closed: make(chan struct{})}
}

func (l *connListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.conns) > 0 {
		c := l.conns[0]
		l.conns = l.conns[1:]
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *connListener) Addr() net.Addr { return benchAddr{} }

type benchAddr struct{}

func (benchAddr) Network() string { return "bench" }
func (benchAddr) String() string  { return "cornus-bench" }

// wsHop returns the cornus-server-side and deploy-caller-side ends of one tagged
// backing stream, carried by yamux inside a real WebSocket — the production
// stack. near/far receive syscall counters on the conns UNDER the WebSocket, so
// their tallies include the WS framing and masking work, which is the point.
func wsHop(tb testing.TB, p qosab.LinkProfile, tag byte, near, far **syscallConn) (net.Conn, net.Conn) {
	tb.Helper()

	// The socket beneath everything: a real unix pair, or an emulated link.
	var srvRaw, cliRaw net.Conn
	if p.BytesPerSec == 0 && p.Latency == 0 {
		srvRaw, cliRaw = sockPair(tb)
	} else {
		srvRaw, cliRaw = qosab.NewLink(p)
		tb.Cleanup(func() { srvRaw.Close(); cliRaw.Close() })
	}
	count := func(c net.Conn, into **syscallConn) net.Conn {
		if into == nil {
			return c
		}
		sc := &syscallConn{Conn: c}
		*into = sc
		return sc
	}
	srvConn, cliConn := count(srvRaw, near), count(cliRaw, far)

	// Server side: accept() is the production upgrade + yamux.Server, and the
	// server OPENS the backing stream exactly as OpenBlockBacking does.
	type res struct {
		c   net.Conn
		err error
	}
	srvCh := make(chan res, 1)
	l := newConnListener(srvConn)
	httpSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := accept(w, r)
		if err != nil {
			srvCh <- res{nil, err}
			return
		}
		stream, err := openTagged(sess, tag)
		srvCh <- res{stream, err}
		if err != nil {
			sess.Close()
			return
		}
		// Hold the handler open: returning would let net/http tear the hijacked
		// connection down underneath the session.
		<-l.closed
		sess.Close()
	})}
	go func() { _ = httpSrv.Serve(l) }()

	// Caller side: the production client dial, with the socket injected through
	// the same ClientTransport seam pkg/deploywire uses for SSH tunnels.
	var dialOnce sync.Once
	ct := ClientTransport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		var c net.Conn
		dialOnce.Do(func() { c = cliConn })
		if c == nil {
			return nil, errors.New("wsHop: the transport dialed more than once")
		}
		return c, nil
	}}
	sess, err := DialControlHeaderCT(context.Background(), "ws://cornus-bench/backing", nil, nil, ct)
	if err != nil {
		tb.Fatal(err)
	}
	gotTag, callerStream, err := acceptTagged(sess)
	if err != nil {
		tb.Fatal(err)
	}
	if gotTag != tag {
		tb.Fatalf("wsHop: accepted tag %q, want %q", gotTag, tag)
	}
	r := <-srvCh
	if r.err != nil {
		tb.Fatal(r.err)
	}
	tb.Cleanup(func() {
		l.Close()
		sess.Close()
		_ = httpSrv.Close()
	})
	return r.c, callerStream
}

// mountTransport selects how the server<->caller hop is carried for one profile:
// a bare socket / emulated link, or the production WebSocket + yamux stack.
type mountTransport struct {
	ws   bool
	link qosab.LinkProfile
}

// TestWSHopPutsTheMaskingCostOnTheCallersWrites pins the harness topology, which
// is the one thing about it that can be wrong while everything still passes.
//
// Under a WebSocket the DIALING side masks its writes and flushes them in 4 KiB
// chunks, so whichever end holds the export pays roughly one syscall per 4 KiB it
// sends. Production puts the export on the dialing side, so that cost lands on
// READ replies. If wsHop were wired the other way round — as the older
// blocklatency_test.go harness is — reads would look cheap, writes would look
// expensive, and every ws measurement would describe a stack nobody runs.
//
// The assertion is the RATIO between the two directions at equal bytes, because
// the absolute figures move with buffer sizes and Go versions while the asymmetry
// is structural.
func TestWSHopPutsTheMaskingCostOnTheCallersWrites(t *testing.T) {
	const total = 4 << 20
	dir := t.TempDir()
	tp := mountTransport{ws: true, link: qosab.LinkProfile{Name: "local"}}

	cl, meters := benchClient(t, dir, true, tp)
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err := root.Create("f", p9.ReadWrite, 0o644, p9.UID(os.Getuid()), p9.GID(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)

	before := meters.snapshot()
	for off := int64(0); off < total; off += int64(len(buf)) {
		if _, err := f.WriteAt(buf, off); err != nil {
			t.Fatal(err)
		}
	}
	wrote := meters.snapshot().sub(before)

	before = meters.snapshot()
	for off := int64(0); off < total; off += int64(len(buf)) {
		if _, err := f.ReadAt(buf, off); err != nil {
			t.Fatal(err)
		}
	}
	read := meters.snapshot().sub(before)

	// Correctness control: without it, a harness where every op failed would move
	// no bytes, both counts would be ~0, and any ratio assertion would be vacuous.
	if got := read.files.rbytes; got != total {
		t.Fatalf("the export served %d read bytes, want %d — the workload did not run", got, total)
	}
	if got := wrote.files.wbytes; got != total {
		t.Fatalf("the export took %d write bytes, want %d — the workload did not run", got, total)
	}

	callerWritesOnRead := read.remoteFar.writes
	callerWritesOnWrite := wrote.remoteFar.writes
	t.Logf("caller-side socket writes for %d bytes: reads %d, writes %d", total, callerWritesOnRead, callerWritesOnWrite)
	if callerWritesOnRead < 4*callerWritesOnWrite {
		t.Fatalf("caller sent %d socket writes serving reads vs %d serving writes; expected reads to dominate. "+
			"The export is probably on the WebSocket SERVER side, where writes are unmasked — inverted from production",
			callerWritesOnRead, callerWritesOnWrite)
	}
}
