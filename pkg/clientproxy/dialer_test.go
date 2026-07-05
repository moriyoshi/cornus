package clientproxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// startEcho starts a TCP echo server and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// startHTTPConnectProxy starts a minimal HTTP CONNECT proxy that records each
// CONNECT target and tunnels to it.
func startHTTPConnectProxy(t *testing.T) (addr string, targets *[]string, mu *sync.Mutex) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var seen []string
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
				seen = append(seen, req.Host)
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
	return ln.Addr().String(), &seen, &m
}

// startSOCKS5Proxy starts a minimal no-auth SOCKS5 proxy that records the ATYP of
// each request (1=IPv4, 3=domainname) and tunnels to the target.
func startSOCKS5Proxy(t *testing.T) (addr string, atyps *[]byte, mu *sync.Mutex) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var seen []byte
	var m sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				ver, _ := br.ReadByte()
				n, _ := br.ReadByte()
				io.CopyN(io.Discard, br, int64(n))
				if ver != 0x05 {
					return
				}
				c.Write([]byte{0x05, 0x00}) // no auth
				hdr := make([]byte, 4)
				io.ReadFull(br, hdr)
				m.Lock()
				seen = append(seen, hdr[3])
				m.Unlock()
				var host string
				switch hdr[3] {
				case 0x01:
					b := make([]byte, 4)
					io.ReadFull(br, b)
					host = net.IP(b).String()
				case 0x03:
					l, _ := br.ReadByte()
					b := make([]byte, int(l))
					io.ReadFull(br, b)
					host = string(b)
				default:
					return
				}
				var pb [2]byte
				io.ReadFull(br, pb[:])
				dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))
				up, err := net.Dial("tcp", dest)
				if err != nil {
					c.Write([]byte{0x05, 0x01, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{0x05, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}) // success
				go io.Copy(up, br)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String(), &seen, &m
}

func roundTrip(t *testing.T, up net.Conn) {
	t.Helper()
	defer up.Close()
	if _, err := io.WriteString(up, "ping"); err != nil {
		t.Fatal(err)
	}
	up.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(up, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q", buf)
	}
}

func TestDialViaDirect(t *testing.T) {
	echo := startEcho(t)
	up, err := dialVia(context.Background(), &ProxyConfig{}, "tcp", echo)
	if err != nil {
		t.Fatalf("direct dial: %v", err)
	}
	roundTrip(t, up)
}

func TestDialViaNoProxyBypass(t *testing.T) {
	echo := startEcho(t)
	proxy, targets, mu := startHTTPConnectProxy(t)
	// A proxy is set, but NO_PROXY covers 127.0.0.1 -> the proxy must be bypassed.
	cfg := &ProxyConfig{HTTP: proxy, NoProxy: "127.0.0.1"}
	up, err := dialVia(context.Background(), cfg, "tcp", echo)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	roundTrip(t, up)
	mu.Lock()
	defer mu.Unlock()
	if len(*targets) != 0 {
		t.Fatalf("NO_PROXY host must bypass the proxy, but proxy saw %v", *targets)
	}
}

func TestDialViaHTTPConnect(t *testing.T) {
	echo := startEcho(t)
	proxy, targets, mu := startHTTPConnectProxy(t)
	cfg := &ProxyConfig{HTTP: proxy}
	up, err := dialVia(context.Background(), cfg, "tcp", echo)
	if err != nil {
		t.Fatalf("dial via http proxy: %v", err)
	}
	roundTrip(t, up)
	mu.Lock()
	defer mu.Unlock()
	if len(*targets) != 1 || (*targets)[0] != echo {
		t.Fatalf("proxy CONNECT targets = %v, want [%s]", *targets, echo)
	}
}

func TestDialViaSOCKS5RemoteAndLocalDNS(t *testing.T) {
	echo := startEcho(t)
	_, port, _ := net.SplitHostPort(echo)

	// socks5h:// forwards the NAME to the proxy (domainname ATYP=3).
	proxy, atyps, mu := startSOCKS5Proxy(t)
	up, err := dialVia(context.Background(), &ProxyConfig{All: "socks5h://" + proxy}, "tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("socks5h dial: %v", err)
	}
	roundTrip(t, up)
	mu.Lock()
	got := append([]byte(nil), *atyps...)
	mu.Unlock()
	if len(got) != 1 || got[0] != 0x03 {
		t.Fatalf("socks5h ATYP = %v, want [3] (domainname / remote DNS)", got)
	}

	// socks5:// resolves locally, so the proxy sees an IPv4 (ATYP=1).
	proxy2, atyps2, mu2 := startSOCKS5Proxy(t)
	up2, err := dialVia(context.Background(), &ProxyConfig{All: "socks5://" + proxy2}, "tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("socks5 dial: %v", err)
	}
	roundTrip(t, up2)
	mu2.Lock()
	got2 := append([]byte(nil), *atyps2...)
	mu2.Unlock()
	// socks5:// resolves locally, so the proxy only ever sees IP literals, never a
	// name (ATYP=3). On a dual-stack host localhost may resolve to ::1 first; the
	// dialer then falls back to 127.0.0.1, so the proxy can see a leading IPv6
	// attempt (ATYP=4) before the successful IPv4 one (ATYP=1).
	if len(got2) == 0 || got2[len(got2)-1] != 0x01 {
		t.Fatalf("socks5 ATYP = %v, want to end with 1 (IPv4 / local DNS)", got2)
	}
	for _, a := range got2 {
		if a == 0x03 {
			t.Fatalf("socks5 forwarded a domainname (ATYP=3): %v; local DNS must resolve first", got2)
		}
	}
}

// TestDialViaSOCKS5LocalDNSDualStackFallback reproduces the CI failure where
// localhost resolves to ::1 first: socks5:// local resolution must not bet on the
// resolver's first address, but fall back through the proxy to a reachable one.
func TestDialViaSOCKS5LocalDNSDualStackFallback(t *testing.T) {
	echo := startEcho(t) // IPv4-only (127.0.0.1)
	_, port, _ := net.SplitHostPort(echo)

	// Force the CI ordering: IPv6 loopback (no listener) ahead of the IPv4 echo.
	orig := lookupIP
	t.Cleanup(func() { lookupIP = orig })
	lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.IPv6loopback}, {IP: net.IPv4(127, 0, 0, 1)}}, nil
	}

	proxy, atyps, mu := startSOCKS5Proxy(t)
	up, err := dialVia(context.Background(), &ProxyConfig{All: "socks5://" + proxy}, "tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("socks5 dial: %v", err)
	}
	roundTrip(t, up)
	mu.Lock()
	got := append([]byte(nil), *atyps...)
	mu.Unlock()
	// The proxy should have seen the IPv6 attempt (ATYP=4) first, then the IPv4 one
	// (ATYP=1) that actually connected — never a domainname (ATYP=3).
	if len(got) != 2 || got[0] != 0x04 || got[1] != 0x01 {
		t.Fatalf("socks5 ATYP sequence = %v, want [4 1] (IPv6 attempt then IPv4 fallback)", got)
	}
}

func TestNoProxyBypass(t *testing.T) {
	if !noProxyBypass("10.0.0.0/8,*.internal", "10.1.2.3") {
		t.Error("10.1.2.3 should be bypassed by 10.0.0.0/8")
	}
	if !noProxyBypass("example.com", "example.com") {
		t.Error("example.com should be bypassed")
	}
	if noProxyBypass("example.com", "other.org") {
		t.Error("other.org should NOT be bypassed")
	}
	if noProxyBypass("", "anything") {
		t.Error("empty NO_PROXY bypasses nothing")
	}
}
