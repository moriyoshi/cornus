package clientproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpproxy"
	xproxy "golang.org/x/net/proxy"
)

// Dialer returns a dialer that routes each connection through the CALLER's own
// resolved proxy — a corporate HTTP or SOCKS proxy, or a SASE gateway — when one
// applies, honoring NO_PROXY, and dials directly otherwise. It resolves the proxy
// configuration once. This is what makes client-side egress reach destinations that
// are only reachable via the caller's sanctioned proxy (not merely direct from the
// caller's host): the container's traffic arrives at the client and then leaves
// through the client's own proxy, exactly as the client's own HTTP traffic would.
func Dialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	cfg, err := Resolve()
	if err != nil || cfg == nil {
		cfg = &ProxyConfig{}
	}
	return DialerFor(cfg)
}

// DialerFor is Dialer with an explicit proxy configuration instead of the resolved
// OS one — the seam that lets a caller (and tests) drive the client dialer through a
// known proxy. A nil cfg dials directly.
func DialerFor(cfg *ProxyConfig) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if cfg == nil {
		cfg = &ProxyConfig{}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialVia(ctx, cfg, network, addr)
	}
}

func directDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// dialVia routes addr through the caller's proxy per the resolved config.
func dialVia(ctx context.Context, cfg *ProxyConfig, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if noProxyBypass(cfg.NoProxy, host) {
		return directDial(ctx, network, addr)
	}
	// A SOCKS proxy (ALL_PROXY) handles arbitrary TCP and, with socks5h, resolves
	// the name at the proxy — the right default for a caller behind a SASE/corp
	// egress. Prefer it; otherwise fall back to an HTTP proxy via CONNECT.
	if cfg.All != "" {
		return socksDial(ctx, cfg.All, addr)
	}
	if p := firstNonEmpty(cfg.HTTPS, cfg.HTTP); p != "" {
		return httpConnectDial(ctx, p, addr)
	}
	return directDial(ctx, network, addr)
}

// noProxyBypass reports whether host is exempted by NO_PROXY. It reuses httpproxy's
// matcher: with a proxy forced, ProxyFunc returns nil ONLY when NoProxy matches.
func noProxyBypass(noProxy, host string) bool {
	if strings.TrimSpace(noProxy) == "" {
		return false
	}
	hc := httpproxy.Config{HTTPSProxy: "http://placeholder:1", NoProxy: noProxy}
	p, err := hc.ProxyFunc()(&url.URL{Scheme: "https", Host: host})
	return err == nil && p == nil
}

// socksDial dials addr through a SOCKS5 proxy. A "socks5://" scheme resolves the
// destination name locally (then dials the IP); "socks5h://" (and the default)
// forwards the name to the proxy for remote resolution — honoring the caller's
// deliberate scheme choice.
func socksDial(ctx context.Context, proxyURL, addr string) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse socks proxy %q: %w", proxyURL, err)
	}
	var auth *xproxy.Auth
	if u.User != nil {
		pw, _ := u.User.Password()
		auth = &xproxy.Auth{User: u.User.Username(), Password: pw}
	}
	d, err := xproxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{})
	if err != nil {
		return nil, err
	}
	// "socks5h" (and the default) forwards the name for remote resolution; only
	// "socks5" resolves locally and forwards an IP literal.
	if !strings.EqualFold(u.Scheme, "socks5") {
		return proxyDial(ctx, d, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return proxyDial(ctx, d, addr)
	}
	ips, err := lookupIP(ctx, host)
	if err != nil || len(ips) == 0 {
		return proxyDial(ctx, d, addr)
	}
	// A host may resolve to several addresses (e.g. both ::1 and 127.0.0.1) while
	// only one family is actually reachable. Because the SOCKS proxy connects on our
	// behalf, try each resolved address through it in turn — like a direct dialer —
	// rather than betting on the resolver's first result.
	var firstErr error
	for _, ip := range ips {
		conn, err := proxyDial(ctx, d, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// lookupIP resolves a host to its IP addresses. It is a package variable so tests
// can drive the socks5:// local-resolution fallback deterministically, independent
// of the host's own /etc/hosts (e.g. whether localhost is dual-stack).
var lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// proxyDial dials addr through the SOCKS dialer, preferring its context-aware form.
func proxyDial(ctx context.Context, d xproxy.Dialer, addr string) (net.Conn, error) {
	if cd, ok := d.(xproxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", addr)
	}
	return d.Dial("tcp", addr)
}

// httpConnectDial dials addr through an HTTP proxy using the CONNECT method (which
// tunnels any TCP, not just HTTP).
func httpConnectDial(ctx context.Context, proxyURL, addr string) (net.Conn, error) {
	u, err := url.Parse(withScheme(proxyURL))
	if err != nil {
		return nil, fmt.Errorf("parse http proxy %q: %w", proxyURL, err)
	}
	proxyAddr := u.Host
	if u.Port() == "" {
		proxyAddr = net.JoinHostPort(u.Hostname(), "80")
	}
	c, err := directDial(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if u.User != nil {
		pw, _ := u.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", cred)
	}
	b.WriteString("\r\n")
	if _, err := c.Write([]byte(b.String())); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("http proxy CONNECT %s: %w", addr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("http proxy CONNECT %s: %s", addr, resp.Status)
	}
	// The proxy waits for the tunnel after 200, so no app bytes are buffered; but if
	// any are, deliver them first so the splice does not lose them.
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// withScheme prepends http:// to a bare host:port so url.Parse populates Host.
func withScheme(p string) string {
	if strings.Contains(p, "://") {
		return p
	}
	return "http://" + p
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// bufferedConn is a net.Conn whose reads come from a buffered reader holding bytes
// already read from the underlying conn.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
