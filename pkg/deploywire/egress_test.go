package deploywire

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/clientproxy"
	"cornus/pkg/egresspolicy"
)

// echoListener starts a TCP echo server and returns its address plus a stop func.
func echoListener(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func mustPolicy(t *testing.T, spec api.EgressSpec) egresspolicy.Policy {
	t.Helper()
	p, err := egresspolicy.Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServeEgressClientRouteRoundTrip(t *testing.T) {
	addr, stop := echoListener(t)
	defer stop()

	policy := mustPolicy(t, api.EgressSpec{Rules: []api.EgressRule{{Pattern: "*", Route: "client"}}})

	// a is the relay/server side we drive; b is the backing stream serveEgress reads.
	a, b := net.Pipe()
	dialed := make(chan string, 1)
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed <- address
		return net.Dial("tcp", addr)
	}
	go serveEgress(context.Background(), addr, b, policy, dial)

	select {
	case got := <-dialed:
		if got != addr {
			t.Fatalf("dialed %q, want %q", got, addr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial never happened for a client-routed destination")
	}

	if _, err := io.WriteString(a, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(a, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
	a.Close()
}

func TestServeEgressGuardDropsNonClientRoute(t *testing.T) {
	addr, stop := echoListener(t)
	defer stop()

	for _, route := range []string{"cluster", "deny", "gateway"} {
		policy := mustPolicy(t, api.EgressSpec{Rules: []api.EgressRule{{Pattern: "*", Route: route}}})
		a, b := net.Pipe()
		dialed := make(chan string, 1)
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed <- address
			return net.Dial("tcp", addr)
		}
		go serveEgress(context.Background(), addr, b, policy, dial)

		// The client guard must refuse to dial anything the policy does not route to
		// the client; the backing is closed instead.
		select {
		case <-dialed:
			t.Fatalf("route %q was dialed locally; the client guard must drop it", route)
		case <-time.After(150 * time.Millisecond):
		}
		// The stream should be closed (read returns an error promptly).
		a.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := a.Read(make([]byte, 1)); err == nil {
			t.Fatalf("route %q: expected the backing to be closed", route)
		}
		a.Close()
	}
}

// connectProxy starts an in-process HTTP CONNECT proxy that records each CONNECT
// target and tunnels to it, standing in for the CALLER's own corporate/SASE proxy.
func connectProxy(t *testing.T) (addr string, seen *[]string, mu *sync.Mutex) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var targets []string
	var m sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					c.Close()
					return
				}
				m.Lock()
				targets = append(targets, req.Host)
				m.Unlock()
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					c.Close()
					return
				}
				io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go io.Copy(up, br)
				io.Copy(c, up)
				c.Close()
				up.Close()
			}()
		}
	}()
	return ln.Addr().String(), &targets, &m
}

// TestServeEgressDialsThroughClientProxy is the G1 proof at integration scope: the
// client-side egress backing must reach the destination through the CALLER's OWN
// resolved proxy (here an HTTP CONNECT proxy), not by a bare direct dial — so a
// container's egress physically leaves via the client's sanctioned egress path. It
// drives the real client dialer (clientproxy.DialerFor) through the egress backing
// handler and asserts the proxy actually carried the connection.
func TestServeEgressDialsThroughClientProxy(t *testing.T) {
	echo, stop := echoListener(t)
	defer stop()
	proxyAddr, seen, mu := connectProxy(t)

	// The caller's resolved config points ALL egress at its own HTTP proxy.
	dial := clientproxy.DialerFor(&clientproxy.ProxyConfig{HTTP: proxyAddr})
	policy := mustPolicy(t, api.EgressSpec{Rules: []api.EgressRule{{Pattern: "*", Route: "client"}}})
	handler := egressHandler(context.Background(), policy, dial)

	a, b := net.Pipe()
	go handler(echo, b)

	if _, err := io.WriteString(a, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	a.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(a, buf); err != nil {
		t.Fatalf("read echo through client proxy: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
	a.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(*seen) != 1 || (*seen)[0] != echo {
		t.Fatalf("client proxy CONNECT targets = %v, want [%s] — the client did NOT dial through its own proxy", *seen, echo)
	}
}

func TestEgressHandlerForModes(t *testing.T) {
	// env / nil => no egress backing served.
	if h := egressHandlerFor(context.Background(), nil, nil); h != nil {
		t.Fatal("nil egress spec should yield no handler")
	}
	if h := egressHandlerFor(context.Background(), &api.EgressSpec{Mode: "env"}, nil); h != nil {
		t.Fatal("env mode should yield no handler")
	}
	// proxy / transparent => a handler.
	if h := egressHandlerFor(context.Background(), &api.EgressSpec{Mode: "proxy"}, nil); h == nil {
		t.Fatal("proxy mode should yield a handler")
	}
	if h := egressHandlerFor(context.Background(), &api.EgressSpec{Mode: "transparent"}, nil); h == nil {
		t.Fatal("transparent mode should yield a handler")
	}
}
