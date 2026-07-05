package e2e

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"cornus/pkg/clientproxy"
)

// TestEgressProxyRecordsAndTunnels drives the harness recording proxy with the same
// client dialer the real client uses (clientproxy, socks5h), and confirms it both
// records the requested destination (by name, remote-DNS) and tunnels bytes to it.
func TestEgressProxyRecordsAndTunnels(t *testing.T) {
	// A TCP echo standing in for the destination the client reaches THROUGH the proxy.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &egressProxy{ln: ln}
	go p.serve()
	defer ln.Close()

	// Dial the echo THROUGH the proxy using the production client dialer (socks5h).
	dial := clientproxy.DialerFor(&clientproxy.ProxyConfig{All: "socks5h://" + ln.Addr().String()})
	// Use a name so the proxy records a domainname (socks5h / remote DNS); it resolves
	// "localhost" itself. The echo's port makes the recorded target unambiguous.
	_, port, _ := net.SplitHostPort(echo.Addr().String())
	target := net.JoinHostPort("localhost", port)

	conn, err := dial(context.Background(), "tcp", target)
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo through proxy: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}

	hits := p.targets()
	if len(hits) != 1 || hits[0] != target {
		t.Fatalf("proxy recorded %v, want [%s]", hits, target)
	}
}

// TestEgressProxyIgnoresMalformedConnect pins the rule that the recording double
// never invents a destination: a CONNECT it cannot fully parse produces NO hit.
//
// The last case is the one that was actually reachable. A domain length byte of 0
// is well-formed as far as every read is concerned — the empty address read
// succeeds, the port read succeeds — and the double recorded ":<port>", an
// address the client never asked for. The truncation cases are characterization,
// not regression: they were already rejected, because the checked port read
// backstops the address reads (a request too short for its address is too short
// for its port). They are here so a future change to the read order or to the
// framing cannot quietly make them reachable.
func TestEgressProxyIgnoresMalformedConnect(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  []byte
	}{
		// ver, nmethods, method | ver, cmd=CONNECT, rsv, atyp, <address...>, port
		{"ipv4 address cut short", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01, 127, 0}},
		{"ipv6 address cut short", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x04, 0xfe, 0x80}},
		{"domain shorter than its length byte", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x03, 0x09, 'l', 'o', 'c'}},
		{"port cut short", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x1f}},
		{"zero-length domain with a valid port", []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x03, 0x00, 0x1f, 0x90}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			p := &egressProxy{ln: ln}
			go p.serve()
			defer ln.Close()

			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Write(tt.req); err != nil {
				t.Fatal(err)
			}
			// Half-close so the proxy's ReadFull sees EOF rather than blocking.
			if cw, ok := c.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			// The handler returns and closes; wait for that rather than sleeping.
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _ = io.Copy(io.Discard, c)
			c.Close()

			if hits := p.targets(); len(hits) != 0 {
				t.Fatalf("malformed CONNECT recorded %v, want no hits", hits)
			}
		})
	}
}

// TestEgressProxySplicePumpsBothDirections pins the splice coordination. The
// naive `go io.Copy(up, br); io.Copy(c, up)` returns as soon as the upstream
// direction ends, and handle's deferred Close then kills the client->upstream
// copy mid-flight. This drives a destination that stays silent until the client
// has sent everything, so a splice that abandons the client->upstream direction
// early loses bytes.
func TestEgressProxySplicePumpsBothDirections(t *testing.T) {
	const payload = 512 << 10 // large enough not to fit in one socket buffer

	dest, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	got := make(chan int, 1)
	go func() {
		c, err := dest.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Read to EOF FIRST, then reply. Nothing flows back while the client is
		// still writing, so the upstream direction cannot end early on its own.
		n, _ := io.Copy(io.Discard, c)
		got <- int(n)
		_, _ = c.Write([]byte("done"))
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &egressProxy{ln: ln}
	go p.serve()
	defer ln.Close()

	// The handshake is spoken by hand rather than through clientproxy: this test
	// needs a half-close on the CLIENT side, and only a raw *net.TCPConn is
	// guaranteed to offer CloseWrite. Going through the production dialer here
	// would test whatever its wrapper happens to expose, not the splice.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	destIP, destPort, _ := net.SplitHostPort(dest.Addr().String())
	dp, _ := strconv.Atoi(destPort)
	req := []byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01}
	req = append(req, net.ParseIP(destIP).To4()...)
	req = append(req, byte(dp>>8), byte(dp))
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	// Method selection (2 bytes) then the CONNECT reply (10 for an IPv4 BND.ADDR).
	if _, err := io.ReadFull(conn, make([]byte, 12)); err != nil {
		t.Fatalf("socks5 handshake: %v", err)
	}

	if _, err := conn.Write(make([]byte, payload)); err != nil {
		t.Fatalf("write through proxy: %v", err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-close: %v", err)
	}

	select {
	case n := <-got:
		if n != payload {
			t.Fatalf("destination received %d bytes, want %d: the client->upstream copy was cut off", n, payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("destination never saw EOF: the client->upstream half-close was not propagated")
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read reply through proxy: %v", err)
	}
	if string(reply) != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}
}
