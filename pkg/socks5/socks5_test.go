package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"cornus/pkg/portfwd"
)

func TestSuffixRouterResolve(t *testing.T) {
	r, err := NewSuffixRouter("") // default .cornus.internal
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		host        string
		port        int
		wantMatched bool
		wantSvc     string
		wantPort    int
	}{
		{"web.cornus.internal", 8080, true, "web", 8080},
		{"api.staging.cornus.internal", 443, true, "api.staging", 443},
		{"example.com", 443, false, "", 0}, // public host -> direct
		{"web", 8080, false, "", 0},        // bare name lacks the suffix -> direct
	}
	for _, tt := range tests {
		res, err := r.Resolve(tt.host, tt.port)
		if err != nil {
			t.Fatalf("Resolve(%q,%d): %v", tt.host, tt.port, err)
		}
		if (res.Kind == KindService) != tt.wantMatched || res.Service != tt.wantSvc || res.Port != tt.wantPort {
			t.Errorf("Resolve(%q,%d) = %+v, want matched=%v svc=%q port=%d",
				tt.host, tt.port, res, tt.wantMatched, tt.wantSvc, tt.wantPort)
		}
	}
}

func TestRouterPortRemapAndBackrefs(t *testing.T) {
	// Sed-style \1/\2 backreferences and a port remap: any host on :5000 -> :10000.
	r, err := NewRouter([]Rule{{Pattern: `^(.*):5000$`, Replace: `\1:10000`}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve("db", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindService || res.Service != "db" || res.Port != 10000 {
		t.Fatalf("got %+v, want matched db:10000", res)
	}
	// A non-:5000 target falls through to direct.
	res, err = r.Resolve("db", 5432)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindDirect {
		t.Fatalf("got %+v, want unmatched", res)
	}
}

func TestRouterMatchedButInvalidResult(t *testing.T) {
	// A rule that rewrites to a portless result matches but errors (the CONNECT
	// must fail, not leak to direct egress).
	r, err := NewRouter([]Rule{{Pattern: `^bad:.*$`, Replace: `justaname`}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve("bad", 80)
	if err == nil {
		t.Fatalf("want error for invalid rewrite, got %+v", res)
	}
	if res.Kind != KindService {
		t.Fatalf("want KindService so it does not fall through to direct")
	}
}

// resolved is a compact Resolve outcome for table assertions.
type resolved struct {
	matched bool
	svc     string
	port    int
}

func mustResolve(t *testing.T, r *Router, host string, port int) resolved {
	t.Helper()
	res, err := r.Resolve(host, port)
	if err != nil {
		t.Fatalf("Resolve(%q,%d): %v", host, port, err)
	}
	return resolved{res.Kind == KindService, res.Service, res.Port}
}

func TestRouterAliasResolvesBothForms(t *testing.T) {
	r, err := NewSuffixRouter("") // default .cornus.internal
	if err != nil {
		t.Fatal(err)
	}
	// A compose service "web" in project "demo" deploys as "demo-web".
	r.RegisterAlias("web", "demo-web")

	tests := []struct {
		name string
		host string
		port int
		want resolved
	}{
		// Bare, single-label name (identical to the service name) routes inward.
		{"bare", "web", 8080, resolved{true, "demo-web", 8080}},
		// Suffixed unqualified name: the suffix rule strips to "web", the alias
		// remaps it to the real deployment "demo-web".
		{"suffixed", "web.cornus.internal", 8080, resolved{true, "demo-web", 8080}},
		// Fully-qualified name still works via the plain suffix rule (no remap).
		{"fully-qualified", "demo-web.cornus.internal", 8080, resolved{true, "demo-web", 8080}},
		// An unrelated bare name is not an alias -> direct egress.
		{"unaliased-bare", "example", 80, resolved{false, "", 0}},
		// A public host still egresses directly.
		{"public", "example.com", 443, resolved{false, "", 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustResolve(t, r, tt.host, tt.port); got != tt.want {
				t.Errorf("Resolve(%q,%d) = %+v, want %+v", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

func TestRouterAliasWithdrawn(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterAlias("web", "demo-web")
	r.UnregisterAlias("web", "demo-web")

	// Bare name falls back to direct egress once the alias is withdrawn.
	if got := mustResolve(t, r, "web", 8080); got.matched {
		t.Errorf("after withdrawal, bare web = %+v, want unmatched", got)
	}
	// Suffixed name no longer remaps: it strips to the literal "web".
	if got := mustResolve(t, r, "web.cornus.internal", 8080); got != (resolved{true, "web", 8080}) {
		t.Errorf("after withdrawal, web.cornus.internal = %+v, want literal web:8080", got)
	}
}

func TestRouterAliasRefCountedOverlap(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	// A recreate overlaps: the new registration lands before the old withdrawal.
	r.RegisterAlias("web", "demo-web")
	r.RegisterAlias("web", "demo-web")
	r.UnregisterAlias("web", "demo-web")

	// Still one live registration -> still resolves.
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "demo-web", 8080}) {
		t.Errorf("during overlap, bare web = %+v, want demo-web:8080", got)
	}
	r.UnregisterAlias("web", "demo-web")
	if got := mustResolve(t, r, "web", 8080); got.matched {
		t.Errorf("after last withdrawal, bare web = %+v, want unmatched", got)
	}
}

// Two projects sharing one conduit may both call a service "web". Precedence is
// JOIN ORDER: the latest claim wins, and withdrawing it restores the one before.
// This replaces the older "contested labels are not routed" rule, whose failure
// modes were each wrong in a different way — the bare form fell through to public
// DNS and the suffixed form resolved to a deployment that does not exist.
func TestRouterAliasLatestClaimWins(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.RegisterAlias("web", "a-web"); got.Winner != "a-web" || !got.Changed || got.Seq == 0 {
		t.Errorf("first claim = %+v, want winner a-web, changed, a non-zero seq", got)
	}
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "a-web", 8080}) {
		t.Errorf("bare web = %+v, want a-web:8080", got)
	}

	// A second project joins and takes the short name.
	if got := r.RegisterAlias("web", "b-web"); got.Winner != "b-web" || !got.Changed {
		t.Errorf("second claim = %+v, want winner b-web and changed", got)
	}
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "b-web", 8080}) {
		t.Errorf("bare web after the later claim = %+v, want b-web:8080", got)
	}
	if got := mustResolve(t, r, "web.cornus.internal", 8080); got != (resolved{true, "b-web", 8080}) {
		t.Errorf("suffixed web = %+v, want b-web:8080", got)
	}
	// The deployment-qualified spelling never moves, whoever else is claiming the
	// short name. It is the one a caller can rely on.
	if got := mustResolve(t, r, "a-web.cornus.internal", 8080); got != (resolved{true, "a-web", 8080}) {
		t.Errorf("a-web.cornus.internal = %+v, want a-web:8080", got)
	}

	// The later project leaves: the short name REPOINTS to the earlier claim rather
	// than going out of service. The caller is told, because nothing else would make
	// it visible — a client keeps using "web" and silently reaches another workload.
	if got := r.UnregisterAlias("web", "b-web"); got.Winner != "a-web" || !got.Changed {
		t.Errorf("withdrawal of the winner = %+v, want winner a-web and changed", got)
	}
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "a-web", 8080}) {
		t.Errorf("bare web after the winner left = %+v, want a-web:8080", got)
	}

	// And the last claim leaving takes the name out of service entirely.
	if got := r.UnregisterAlias("web", "a-web"); got.Winner != "" || !got.Changed {
		t.Errorf("withdrawal of the last claim = %+v, want an empty winner and changed", got)
	}
	if got := mustResolve(t, r, "web", 8080); got.matched {
		t.Errorf("bare web after every claim left = %+v, want unmatched", got)
	}
}

// Withdrawing a claim that is NOT the winner must leave routing alone. Getting this
// wrong would repoint the short name every time an unrelated project tore down.
func TestRouterAliasWithdrawingALoserChangesNothing(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterAlias("web", "a-web")
	r.RegisterAlias("web", "b-web")

	if got := r.UnregisterAlias("web", "a-web"); got.Winner != "b-web" || got.Changed {
		t.Errorf("withdrawing a loser = %+v, want winner b-web and unchanged", got)
	}
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "b-web", 8080}) {
		t.Errorf("bare web = %+v, want b-web:8080 (unchanged)", got)
	}
}

// A recreate registers the replacement before withdrawing the predecessor. That
// overlap must not promote the recreated deployment past a project that claimed the
// label later — otherwise restarting one service silently steals another's name.
func TestRouterAliasRecreateDoesNotStealTheLabel(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterAlias("web", "a-web") // project A claims it first
	r.RegisterAlias("web", "b-web") // project B joins later and wins

	// Project A recreates its service: register-then-withdraw, overlapping.
	if got := r.RegisterAlias("web", "a-web"); got.Winner != "b-web" || got.Changed {
		t.Errorf("recreate = %+v, want winner b-web and unchanged", got)
	}
	r.UnregisterAlias("web", "a-web")
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "b-web", 8080}) {
		t.Errorf("after A recreated, bare web = %+v, want b-web:8080", got)
	}

	// And A is still there underneath: B leaving hands the name back.
	if got := r.UnregisterAlias("web", "b-web"); got.Winner != "a-web" {
		t.Errorf("after B left, winner = %q, want a-web", got.Winner)
	}
}

func TestRouterBareServiceNamesDisabled(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterAlias("web", "demo-web")
	r.SetBareServiceNames(false)

	// Bare form is disabled -> direct egress even though the alias exists.
	if got := mustResolve(t, r, "web", 8080); got.matched {
		t.Errorf("bare web with bare names disabled = %+v, want unmatched", got)
	}
	// The suffixed form is unaffected and still remaps to the real deployment.
	if got := mustResolve(t, r, "web.cornus.internal", 8080); got != (resolved{true, "demo-web", 8080}) {
		t.Errorf("suffixed web with bare names disabled = %+v, want demo-web:8080", got)
	}
}

func TestRouterClearAliases(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterAlias("web", "demo-web")
	r.RegisterAlias("cache", "demo-cache")
	r.ClearAliases()

	for _, host := range []string{"web", "cache"} {
		if got := mustResolve(t, r, host, 8080); got.matched {
			t.Errorf("after ClearAliases, bare %q = %+v, want unmatched", host, got)
		}
	}
}

// echoServer accepts TCP connections and echoes bytes back until closed.
func echoServer(t *testing.T) net.Listener {
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
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

type fakeDialer struct {
	mu     sync.Mutex
	calls  [][2]any // {name, port}
	target string   // echo server to relay to
}

func (f *fakeDialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	f.mu.Lock()
	f.calls = append(f.calls, [2]any{name, port})
	f.mu.Unlock()
	return net.Dial("tcp", f.target)
}

func (f *fakeDialer) got() [][2]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]any(nil), f.calls...)
}

type fakeDirect struct {
	mu     sync.Mutex
	addrs  []string
	target string
}

func (f *fakeDirect) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	f.mu.Lock()
	f.addrs = append(f.addrs, address)
	f.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, f.target)
}

func (f *fakeDirect) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.addrs...)
}

// roundTrip dials target through the SOCKS5 proxy at proxyAddr, sends a probe,
// and returns the echoed bytes.
func roundTrip(t *testing.T, proxyAddr, target string) string {
	t.Helper()
	d, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.Dial("tcp", target)
	if err != nil {
		t.Fatalf("dial %s via socks: %v", target, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	const probe = "hello"
	if _, err := conn.Write([]byte(probe)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(probe))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	return string(buf)
}

func TestProxyRoutesMatchedToTunnelAndRestDirect(t *testing.T) {
	tunnelEcho := echoServer(t)
	directEcho := echoServer(t)
	dialer := &fakeDialer{target: tunnelEcho.Addr().String()}
	direct := &fakeDirect{target: directEcho.Addr().String()}

	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), dialer, router, "127.0.0.1:0", WithDirectDialer(direct))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Matched name -> tunnel via PortForward("web", 8080).
	if got := roundTrip(t, p.Addr(), "web.cornus.internal:8080"); got != "hello" {
		t.Fatalf("tunnel echo = %q", got)
	}
	// Unmatched host -> direct dial, with the ORIGINAL host:port preserved.
	if got := roundTrip(t, p.Addr(), "example.com:443"); got != "hello" {
		t.Fatalf("direct echo = %q", got)
	}

	calls := dialer.got()
	if len(calls) != 1 || calls[0][0] != "web" || calls[0][1] != 8080 {
		t.Fatalf("PortForward calls = %v, want one {web 8080}", calls)
	}
	addrs := direct.got()
	if len(addrs) != 1 || addrs[0] != "example.com:443" {
		t.Fatalf("direct dials = %v, want [example.com:443]", addrs)
	}
}

// TestProxyRejectsEmptyDomain locks the fix: a zero-length domain CONNECT must be
// rejected, never dialed as ":port" (the proxy host's own localhost).
func TestProxyRejectsEmptyDomain(t *testing.T) {
	dialer := &fakeDialer{}
	direct := &fakeDirect{}
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), dialer, router, "127.0.0.1:0", WithDirectDialer(direct))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	// Greeting: VER=5, 1 method, no-auth.
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	// CONNECT with ATYP=domain, length 0, port 80.
	if _, err := c.Write([]byte{0x05, 0x01, 0x00, 0x03, 0x00, 0x00, 0x50}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x04 { // repHostUnreachable
		t.Fatalf("reply code = %d, want repHostUnreachable(4)", reply[1])
	}
	if len(dialer.got()) != 0 || len(direct.got()) != 0 {
		t.Fatalf("empty-domain CONNECT reached a dialer: tunnel=%v direct=%v", dialer.got(), direct.got())
	}
}

// TestProxyHandshakeTimeout locks the fix for the unbounded-goroutine/FD leak: a
// client that completes the TCP handshake but sends no SOCKS5 bytes must be
// reaped by the server's handshake read deadline rather than parked forever.
func TestProxyHandshakeTimeout(t *testing.T) {
	dialer := &fakeDialer{}
	direct := &fakeDirect{}
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), dialer, router, "127.0.0.1:0",
		WithDirectDialer(direct), WithHandshakeTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Send nothing after the TCP handshake. Give the client a generous read
	// deadline so, if the server never reaps us, this Read blocks and the client
	// deadline (not the server close) is what fires -> test fails.
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("expected server to close the idle conn, read %d bytes", n)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("client timed out waiting for reap; server did not close the idle conn: %v", err)
	}
}

func TestProxyPortRemap(t *testing.T) {
	tunnelEcho := echoServer(t)
	dialer := &fakeDialer{target: tunnelEcho.Addr().String()}
	router, err := NewRouter([]Rule{{Pattern: `^(.*):5000$`, Replace: `\1:10000`}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), dialer, router, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if got := roundTrip(t, p.Addr(), "cache:5000"); got != "hello" {
		t.Fatalf("echo = %q", got)
	}
	calls := dialer.got()
	if len(calls) != 1 || calls[0][0] != "cache" || calls[0][1] != 10000 {
		t.Fatalf("PortForward calls = %v, want one {cache 10000}", calls)
	}
}

// The reason sequence numbers exist. After a conduit's host is replaced, the
// survivors replay what they hold, and their arrival order is whatever the
// reconnect race produced — commonly the REVERSE of the original, because the
// participant that won the election replays first.
//
// Replaying with the original sequences must reproduce the original winner
// regardless of that order. Measured before this existed: A claimed "web" first and
// B second, and after a takeover the order came out "shop-web then demo-web", so
// the short name silently moved to A's workload. Nothing reported it, because each
// participant replayed its own claims in its own original order.
func TestRouterAliasReplayOutOfOrderPreservesPrecedence(t *testing.T) {
	original, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	first := original.RegisterAlias("web", "demo-web")  // project A, joined first
	second := original.RegisterAlias("web", "shop-web") // project B, joined later and wins
	if got := mustResolve(t, original, "web", 8080); got != (resolved{true, "shop-web", 8080}) {
		t.Fatalf("before the takeover, bare web = %+v, want shop-web:8080", got)
	}

	// The host dies. B wins the election and replays first; A reconnects and replays
	// second — the opposite of the order in which the claims were originally made.
	replacement, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	replacement.Claim(AliasSpec{Label: "web", Deployment: "shop-web", Seq: second.Seq})
	replacement.Claim(AliasSpec{Label: "web", Deployment: "demo-web", Seq: first.Seq})

	if got := mustResolve(t, replacement, "web", 8080); got != (resolved{true, "shop-web", 8080}) {
		t.Errorf("after replaying out of order, bare web = %+v, want shop-web:8080 — precedence did not survive the takeover", got)
	}
	// And withdrawal still promotes the claim underneath, in the restored order.
	if got := replacement.UnregisterAlias("web", "shop-web"); got.Winner != "demo-web" {
		t.Errorf("after the winner left, winner = %q, want demo-web", got.Winner)
	}
}

// A claim made AFTER a takeover must outrank everything the new host inherited.
// Without advancing the counter past the restored sequences, a fresh claim would be
// assigned a low number and land silently underneath them — so a project joining a
// recovered conduit would never win a contested name.
func TestRouterAliasCounterResumesAboveRestoredSequences(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.Claim(AliasSpec{Label: "web", Deployment: "restored-web", Seq: 500})

	fresh := r.RegisterAlias("web", "new-web")
	if fresh.Seq <= 500 {
		t.Errorf("a claim made after restoring seq 500 got seq %d, want one above it", fresh.Seq)
	}
	if fresh.Winner != "new-web" || !fresh.Changed {
		t.Errorf("fresh claim = %+v, want it to win", fresh)
	}
	if got := mustResolve(t, r, "web", 8080); got != (resolved{true, "new-web", 8080}) {
		t.Errorf("bare web = %+v, want new-web:8080", got)
	}
}

// A recreate must keep its ORIGINAL sequence. Taking a fresh one would promote the
// recreated deployment past a project that claimed the label later, so restarting a
// service would silently steal another project's short name.
func TestRouterAliasRecreateKeepsItsSequence(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	first := r.RegisterAlias("web", "a-web")
	r.RegisterAlias("web", "b-web")

	again := r.RegisterAlias("web", "a-web")
	if again.Seq != first.Seq {
		t.Errorf("recreate took seq %d, want the original %d", again.Seq, first.Seq)
	}
	if again.Winner != "b-web" || again.Changed {
		t.Errorf("recreate = %+v, want b-web to keep the label", again)
	}
}

// recordingDialer notes which service each tunnel was asked for, so a test can tell
// which server a CONNECT was routed to.
type recordingDialer struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (d *recordingDialer) PortForward(_ context.Context, service string, port int, proto string) (net.Conn, error) {
	d.mu.Lock()
	d.seen = append(d.seen, fmt.Sprintf("%s:%d/%s", service, port, proto))
	d.mu.Unlock()
	return nil, fmt.Errorf("recordingDialer %s: no real tunnel", d.name)
}

func (d *recordingDialer) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

// Once conduits are joined by address, one proxy serves several projects — and
// those projects need not be talking to the same cornus server. The dialer
// therefore rides on the CLAIM, not on the proxy: a single proxy-wide dialer would
// tunnel every consolidated project's traffic to whichever server the proxy
// happened to be started for.
func TestRouterResolveCarriesTheClaimsOwnDialer(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	alpha, beta := &recordingDialer{name: "alpha"}, &recordingDialer{name: "beta"}
	r.Claim(AliasSpec{Label: "web", Deployment: "alpha-web", Dialer: alpha})
	r.Claim(AliasSpec{Label: "api", Deployment: "beta-api", Dialer: beta})

	got, err := r.Resolve("web.cornus.internal", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "alpha-web" || got.Dialer != portfwd.Dialer(alpha) {
		t.Errorf("web resolved to %s via %v, want alpha-web via alpha", got.Service, got.Dialer)
	}
	got, err = r.Resolve("api.cornus.internal", 9090)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "beta-api" || got.Dialer != portfwd.Dialer(beta) {
		t.Errorf("api resolved to %s via %v, want beta-api via beta", got.Service, got.Dialer)
	}

	// A claim registered without a dialer leaves it nil, so the proxy falls back to
	// its own — the single-project case, unchanged.
	r.RegisterAlias("plain", "plain-web")
	got, err = r.Resolve("plain.cornus.internal", 80)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dialer != nil {
		t.Errorf("a claim with no dialer resolved with %v, want nil so the proxy's own is used", got.Dialer)
	}
}

// The proxy must actually USE the claim's dialer. Resolve carrying it is only half
// the contract: a proxy that read the field and then dialed its own would pass the
// test above while sending every consolidated project's traffic to one server.
func TestProxyDialsThroughTheClaimsOwnDialer(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	claimed, fallback := &recordingDialer{name: "claimed"}, &recordingDialer{name: "fallback"}
	r.Claim(AliasSpec{Label: "web", Deployment: "alpha-web", Dialer: claimed})
	r.RegisterAlias("plain", "plain-web") // no dialer: the proxy's own must serve it

	p, err := Start(context.Background(), fallback, r, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	connectVia(t, p.Addr(), "web.cornus.internal", 8080)
	connectVia(t, p.Addr(), "plain.cornus.internal", 80)

	if got, want := claimed.calls(), []string{"alpha-web:8080/tcp"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("the claim's dialer saw %v, want %v", got, want)
	}
	if got, want := fallback.calls(), []string{"plain-web:80/tcp"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("the proxy's own dialer saw %v, want %v", got, want)
	}
}

// connectVia performs a SOCKS5 CONNECT and discards the outcome; the test asserts
// on which dialer was consulted, not on the tunnel (there is none).
func connectVia(t *testing.T, proxyAddr, host string, port int) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dialing the proxy: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
		t.Fatalf("greeting reply: %v", err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("connect: %v", err)
	}
	_, _ = io.ReadFull(c, make([]byte, 10))
}
