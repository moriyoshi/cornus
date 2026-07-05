package caretaker

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/wire"
)

// fakeRelay plays the server's relayEgressMuxed for the client route: it accepts
// TagEgress streams, reads the session + route + destination lines, dials the
// destination itself (standing in for the client's egress backing), and splices.
func fakeRelay(t *testing.T, server *yamux.Session) {
	t.Helper()
	go func() {
		for {
			tag, stream, err := wire.AcceptTagged(server)
			if err != nil {
				return
			}
			if tag != wire.TagEgress {
				stream.Close()
				continue
			}
			go func() {
				defer stream.Close()
				if _, err := wire.ReadLine(stream); err != nil { // session
					return
				}
				if _, err := wire.ReadLine(stream); err != nil { // route
					return
				}
				dest, err := wire.ReadLine(stream)
				if err != nil {
					return
				}
				up, err := net.Dial("tcp", dest)
				if err != nil {
					return
				}
				defer up.Close()
				go io.Copy(up, stream)
				io.Copy(stream, up)
			}()
		}
	}()
}

func startEgressProxy(t *testing.T, rules []api.EgressRule) (proxyAddr string, echoAddr string) {
	t.Helper()
	// Echo destination the "client" (fakeRelay) can reach.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	t.Cleanup(func() { echo.Close() })

	client, server := yamuxPair(t)
	fakeRelay(t, server)

	pol, err := egresspolicy.Compile(api.EgressSpec{Rules: rules, Default: egresspolicy.RouteDeny})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go serveEgressProxy(ctx, ln, client, "sess-1", pol, 0)
	return ln.Addr().String(), echo.Addr().String()
}

func TestEgressProxyHTTPConnect(t *testing.T) {
	proxyAddr, echoAddr := startEgressProxy(t, []api.EgressRule{{Pattern: "*", Route: egresspolicy.RouteClient}})

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "CONNECT "+echoAddr+" HTTP/1.1\r\nHost: "+echoAddr+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}
	// Tunnel established: write through it, expect the echo back.
	if _, err := io.WriteString(c, "hello"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("echo = %q", buf)
	}
}

func TestEgressProxyHTTPConnectDenied(t *testing.T) {
	proxyAddr, echoAddr := startEgressProxy(t, []api.EgressRule{{Pattern: "*", Route: egresspolicy.RouteDeny}})
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT "+echoAddr+" HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("denied CONNECT status = %d, want 403", resp.StatusCode)
	}
}

func TestEgressProxySocks5Domain(t *testing.T) {
	proxyAddr, echoAddr := startEgressProxy(t, []api.EgressRule{{Pattern: "*", Route: egresspolicy.RouteClient}})
	host, portStr, _ := net.SplitHostPort(echoAddr)
	port, _ := strconvAtoi(portStr)

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Greeting: VER=5, 1 method (no-auth).
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(c, reply); err != nil || reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("socks greeting reply = %v (err=%v)", reply, err)
	}
	// CONNECT to a DOMAINNAME (127.0.0.1 spelled as a name would need DNS; use the
	// literal-as-domain form to exercise the socks5h name path — the relay resolves).
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	req = append(req, p[:]...)
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatalf("read socks reply: %v", err)
	}
	if rep[1] != 0x00 {
		t.Fatalf("socks connect status = %d, want 0 (success)", rep[1])
	}
	if _, err := io.WriteString(c, "socks!"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "socks!" {
		t.Fatalf("echo = %q", buf)
	}
}

// strconvAtoi is a tiny local helper to avoid importing strconv only for the test.
func strconvAtoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// startEgressProxyToHTTP is like startEgressProxy but the destination is a real
// HTTP server (so the plain-HTTP absolute-form forwarding path can be exercised),
// returning the proxy address and the target URL.
func startEgressProxyToHTTP(t *testing.T, rules []api.EgressRule) (proxyAddr, targetURL string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin", "hit")
		io.WriteString(w, "origin:"+r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	client, server := yamuxPair(t)
	fakeRelay(t, server)
	pol, err := egresspolicy.Compile(api.EgressSpec{Rules: rules, Default: egresspolicy.RouteDeny})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go serveEgressProxy(ctx, ln, client, "sess-1", pol, 0)
	return ln.Addr().String(), srv.URL
}

func TestEgressProxyHTTPForward(t *testing.T) {
	proxyAddr, targetURL := startEgressProxyToHTTP(t, []api.EgressRule{{Pattern: "*", Route: egresspolicy.RouteClient}})

	// A client configured with HTTP_PROXY=http://proxyAddr sends absolute-form
	// requests; the caretaker forwards them to the origin and returns the response.
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	hc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := hc.Get(targetURL + "/hello")
	if err != nil {
		t.Fatalf("GET through http proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Origin") != "hit" {
		t.Errorf("missing origin header; got %v", resp.Header)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "origin:/hello" {
		t.Fatalf("body = %q, want origin:/hello", body)
	}
}

func TestEgressProxyHTTPForwardDenied(t *testing.T) {
	proxyAddr, targetURL := startEgressProxyToHTTP(t, []api.EgressRule{{Pattern: "*", Route: egresspolicy.RouteDeny}})
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	hc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := hc.Get(targetURL + "/x")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("denied plain-HTTP status = %d, want 403", resp.StatusCode)
	}
}

func TestEgressReady(t *testing.T) {
	// No listener expected when ListenPort is 0.
	if err := egressReady(EgressRole{ListenPort: 0}); err != nil {
		t.Errorf("ListenPort 0 should be ready (no listener expected), got %v", err)
	}

	// A live loopback listener on the role's port -> ready.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if err := egressReady(EgressRole{ListenPort: port}); err != nil {
		t.Errorf("egressReady with a live listener should pass, got %v", err)
	}
	// And Ready() folds the egress role in.
	if err := Ready(Config{Egress: &EgressRole{ListenPort: port}}); err != nil {
		t.Errorf("Ready with a live egress listener should pass, got %v", err)
	}

	// Nothing listening on a closed port -> not ready (gates the app container).
	ln.Close()
	if err := egressReady(EgressRole{ListenPort: port}); err == nil {
		t.Error("egressReady should fail when nothing is listening on the port")
	}
	if err := Ready(Config{Egress: &EgressRole{ListenPort: port}}); err == nil {
		t.Error("Ready should fail when the egress proxy is not listening")
	}
}
