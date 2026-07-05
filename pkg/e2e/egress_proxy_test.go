package e2e

import (
	"context"
	"io"
	"net"
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
