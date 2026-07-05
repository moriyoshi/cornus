package portfwd

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
)

// fakeDialer hands out one end of a net.Pipe whose other end is echoed, and
// records the ports/protocols dialed. Byte echo doubles as framed-datagram echo:
// a frame echoed byte-for-byte is still a valid frame.
type fakeDialer struct {
	mu     sync.Mutex
	ports  []int
	protos []string
	err    error
	udpErr error            // fails only proto=="udp" dials (a TCP-only backend, kubernetes)
	closed int              // tunnels whose far end ended (echo goroutine returned)
	echo   func(c net.Conn) // defaults to io.Copy(c, c)
}

func (f *fakeDialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	f.mu.Lock()
	f.ports = append(f.ports, port)
	f.protos = append(f.protos, proto)
	err := f.err
	if proto == "udp" && f.udpErr != nil {
		err = f.udpErr
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	near, far := net.Pipe()
	echo := f.echo
	if echo == nil {
		echo = func(c net.Conn) { _, _ = io.Copy(c, c) }
	}
	go func() {
		defer far.Close()
		echo(far)
		f.mu.Lock()
		f.closed++
		f.mu.Unlock()
	}()
	return near, nil
}

func (f *fakeDialer) dialed() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.ports...)
}

func (f *fakeDialer) dialedProtos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.protos...)
}

func (f *fakeDialer) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// roundTrip dials addr, writes msg, and expects it echoed back.
func roundTrip(t *testing.T, addr, msg string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

func TestGroupEchoRoundTrip(t *testing.T) {
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web", []api.PortMapping{{Host: 0, Container: 80}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	fwds := g.Forwards()
	if len(fwds) != 1 {
		t.Fatalf("forwards = %d, want 1", len(fwds))
	}
	roundTrip(t, fwds[0].Local, "hello")
	if got := d.dialed(); len(got) != 1 || got[0] != 80 {
		t.Fatalf("dialed ports = %v, want [80]", got)
	}
}

func TestGroupConcurrentConnections(t *testing.T) {
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web", []api.PortMapping{{Host: 0, Container: 80}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	addr := g.Forwards()[0].Local
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			roundTrip(t, addr, fmt.Sprintf("conn-%d", i))
		}(i)
	}
	wg.Wait()
	if got := d.dialed(); len(got) != 5 {
		t.Fatalf("dialed %d tunnels, want 5", len(got))
	}
}

// udpRoundTrip sends msg from src to addr and expects it echoed back to src.
func udpRoundTrip(t *testing.T, src *net.UDPConn, addr *net.UDPAddr, msg string) {
	t.Helper()
	if _, err := src.WriteToUDP([]byte(msg), addr); err != nil {
		t.Fatalf("udp send: %v", err)
	}
	_ = src.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := src.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("udp read echo for %q: %v", msg, err)
	}
	if string(buf[:n]) != msg {
		t.Fatalf("udp echo = %q, want %q", buf[:n], msg)
	}
}

// TestGroupUDPPerSourceFlows drives a UDP mapping end to end: two distinct
// client sources round-trip datagrams through per-source tunnels (the echo far
// end echoes the frames), and each source's reply is routed back only to it. A
// probe tunnel plus one tunnel per source must have been dialed, all udp.
func TestGroupUDPPerSourceFlows(t *testing.T) {
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web", []api.PortMapping{{Host: 0, Container: 53, Protocol: "udp"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	fwds := g.Forwards()
	if len(fwds) != 1 || fwds[0].Mapping.Protocol != "udp" {
		t.Fatalf("forwards = %+v, want one udp mapping", fwds)
	}
	addr, err := net.ResolveUDPAddr("udp", fwds[0].Local)
	if err != nil {
		t.Fatalf("resolve %s: %v", fwds[0].Local, err)
	}

	src1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("src1: %v", err)
	}
	defer src1.Close()
	src2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("src2: %v", err)
	}
	defer src2.Close()

	udpRoundTrip(t, src1, addr, "HELLO-UDP")
	udpRoundTrip(t, src2, addr, "second-source")
	udpRoundTrip(t, src1, addr, "again") // reuses src1's flow

	protos := d.dialedProtos()
	if len(protos) != 3 { // probe + one flow per source
		t.Fatalf("dialed %d tunnels (%v), want 3 (probe + 2 flows)", len(protos), protos)
	}
	for _, p := range protos {
		if p != "udp" {
			t.Fatalf("dialed protos = %v, want all udp", protos)
		}
	}
}

// TestGroupUDPIdleExpiry proves a per-source flow's tunnel is reclaimed after
// the idle timeout and that traffic afterwards opens a fresh flow.
func TestGroupUDPIdleExpiry(t *testing.T) {
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web",
		[]api.PortMapping{{Host: 0, Container: 53, Protocol: "udp"}},
		WithUDPIdleTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	addr, err := net.ResolveUDPAddr("udp", g.Forwards()[0].Local)
	if err != nil {
		t.Fatal(err)
	}
	src, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	udpRoundTrip(t, src, addr, "before-idle")

	// The probe tunnel is closed at Start; the flow's tunnel must be closed by
	// the idle GC (closed count reaches 2).
	deadline := time.Now().Add(5 * time.Second)
	for d.closedCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("idle GC did not reclaim the flow (closed = %d)", d.closedCount())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Traffic after expiry opens a fresh flow (probe + flow + new flow = 3 dials).
	udpRoundTrip(t, src, addr, "after-idle")
	if got := d.dialedProtos(); len(got) != 3 {
		t.Fatalf("dialed %d tunnels (%v), want 3 after idle-expiry redial", len(got), got)
	}
}

// TestGroupUDPActiveFlowSurvivesIdleGC drives a single UDP source at the idle
// boundary so that, on some ticks, the idle GC marks the flow stale in the same
// window a fresh datagram refreshes it. The fix keeps such a flow alive (the GC
// rechecks last under the lock at reclaim time, and fetch+refresh is atomic), so
// every datagram is still delivered and echoed. Before the fix the GC could
// close an actively-used tunnel mid-write, dropping the reply and forcing a
// redial — the round-trip would time out. This test can only fail if that
// regression returns; the fixed code delivers every datagram deterministically.
func TestGroupUDPActiveFlowSurvivesIdleGC(t *testing.T) {
	const idle = 40 * time.Millisecond
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web",
		[]api.PortMapping{{Host: 0, Container: 53, Protocol: "udp"}},
		WithUDPIdleTimeout(idle))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	addr, err := net.ResolveUDPAddr("udp", g.Forwards()[0].Local)
	if err != nil {
		t.Fatal(err)
	}
	src, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	// Send at the idle boundary so ticks and refreshes race repeatedly. Every
	// datagram must round-trip (either over the surviving flow or a freshly
	// redialed one); a lost reply means an active flow was torn down mid-use.
	for i := 0; i < 40; i++ {
		udpRoundTrip(t, src, addr, fmt.Sprintf("keepalive-%d", i))
		time.Sleep(idle)
	}
}

// TestGroupUDPSkipsWhenBackendRejects models a kubernetes-target session: the
// probe dial fails (the server's ack rejects UDP), so the mapping is skipped
// with a warning while tcp mappings still forward.
func TestGroupUDPSkipsWhenBackendRejects(t *testing.T) {
	var msgs []string
	logf := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	d := &fakeDialer{udpErr: fmt.Errorf("udp port-forward: kubernetes pods/portforward is TCP-only")}
	g, err := Start(context.Background(), d, "web",
		[]api.PortMapping{{Host: 0, Container: 53, Protocol: "udp"}, {Host: 0, Container: 80}},
		WithLogf(logf))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	fwds := g.Forwards()
	if len(fwds) != 1 || fwds[0].Mapping.Container != 80 {
		t.Fatalf("forwards = %+v, want just the tcp mapping", fwds)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "skipping") || !strings.Contains(msgs[0], "TCP-only") {
		t.Fatalf("warnings = %v, want one udp skip carrying the reject reason", msgs)
	}
	roundTrip(t, fwds[0].Local, "tcp-still-works")
}

// TestGroupSkipsUnknownProtocol asserts a mapping with an unsupported protocol
// is warn-and-skipped without dialing.
func TestGroupSkipsUnknownProtocol(t *testing.T) {
	var msgs []string
	logf := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web",
		[]api.PortMapping{{Host: 0, Container: 132, Protocol: "sctp"}},
		WithLogf(logf))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()
	if len(g.Forwards()) != 0 {
		t.Fatalf("forwards = %+v, want none", g.Forwards())
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "unsupported protocol") {
		t.Fatalf("warnings = %v, want one unsupported-protocol skip", msgs)
	}
	if len(d.dialed()) != 0 {
		t.Fatalf("dialed = %v, want no dials", d.dialed())
	}
}

func TestGroupSkipsBusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	var msgs []string
	logf := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	d := &fakeDialer{}
	g, err := Start(context.Background(), d, "web",
		[]api.PortMapping{{Host: busyPort, Container: 8080}, {Host: 0, Container: 80}},
		WithLogf(logf))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	fwds := g.Forwards()
	if len(fwds) != 1 || fwds[0].Mapping.Container != 80 {
		t.Fatalf("forwards = %+v, want just the free mapping", fwds)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "skipping") {
		t.Fatalf("warnings = %v, want one bind skip", msgs)
	}
	roundTrip(t, fwds[0].Local, "still-works")
}

func TestGroupStrictBindFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	d := &fakeDialer{}
	// The free mapping binds first, then the busy one must fail and clean up.
	_, err = Start(context.Background(), d, "web",
		[]api.PortMapping{{Host: 0, Container: 80}, {Host: busyPort, Container: 8080}},
		WithStrictBind())
	if err == nil {
		t.Fatal("Start with a busy port under WithStrictBind: want error")
	}
}

func TestGroupCloseSeversInFlight(t *testing.T) {
	// An echo that never writes: the pipe only ends when a side is closed.
	d := &fakeDialer{echo: func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }}
	g, err := Start(context.Background(), d, "web", []api.PortMapping{{Host: 0, Container: 80}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	c, err := net.Dial("tcp", g.Forwards()[0].Local)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "held"); err != nil {
		t.Fatalf("write: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		g.Close()
		g.Close() // idempotent
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung behind an in-flight connection")
	}
}

func TestGroupParentContextCancelCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &fakeDialer{}
	g, err := Start(ctx, d, "web", []api.PortMapping{{Host: 0, Container: 80}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := g.Forwards()[0].Local
	roundTrip(t, addr, "before-cancel")

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			break // listener gone
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting after parent ctx cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.Close()
}

func TestGroupTunnelDialFailureNonFatal(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		msgs = append(msgs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	d := &fakeDialer{err: fmt.Errorf("backend not ready")}
	g, err := Start(context.Background(), d, "web", []api.PortMapping{{Host: 0, Container: 80}}, WithLogf(logf))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Close()

	addr := g.Forwards()[0].Local
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// The local conn must be closed by the failed tunnel attempt.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after failed tunnel = %v, want EOF", err)
	}
	c.Close()

	// The listener must still accept: failures are per-connection.
	d.mu.Lock()
	d.err = nil
	d.mu.Unlock()
	roundTrip(t, addr, "recovered")

	mu.Lock()
	defer mu.Unlock()
	if len(msgs) == 0 || !strings.Contains(msgs[0], "failed") {
		t.Fatalf("warnings = %v, want a tunnel failure log", msgs)
	}
}
