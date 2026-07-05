package socks5

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/proxy"
)

// fakeLocal is a LocalDialer that connects to a real echo listener, and records
// how often it was dialed.
type fakeLocal struct {
	target string

	mu    sync.Mutex
	calls int
}

func (f *fakeLocal) DialLocal(ctx context.Context) (net.Conn, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.target)
}

func (f *fakeLocal) got() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestRouterLocalResolves(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	fl := &fakeLocal{}
	r.RegisterLocal("cornus.internal", 80, fl)

	res, err := r.Resolve("cornus.internal", 80)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLocal {
		t.Fatalf("Resolve(cornus.internal,80).Kind = %v, want KindLocal", res.Kind)
	}
	if res.Local != LocalDialer(fl) {
		t.Fatalf("Resolve returned a different dialer than was registered")
	}
}

// TestRouterLocalIsPortKeyed proves the host:port keying: only the published port
// is claimed, so https://cornus.internal (:443) still egresses directly rather than
// tunneling TLS into a plaintext handler.
func TestRouterLocalIsPortKeyed(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterLocal("cornus.internal", 80, &fakeLocal{})

	res, err := r.Resolve("cornus.internal", 443)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindDirect {
		t.Fatalf("Resolve(cornus.internal,443).Kind = %v, want KindDirect", res.Kind)
	}
}

// TestRouterLocalBeatsRules is the precedence rule under test. A catch-all rule
// (the shape the suite's own TestRouter* corpus uses) would otherwise swallow the
// published name.
func TestRouterLocalBeatsRules(t *testing.T) {
	r, err := NewRouter([]Rule{{Pattern: `^(.*)$`, Replace: `\1:80`}})
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterLocal("cornus.internal", 80, &fakeLocal{})

	res, err := r.Resolve("cornus.internal", 80)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLocal {
		t.Fatalf("Kind = %v, want KindLocal (a rule shadowed the published name)", res.Kind)
	}
	// Everything else still routes by the rule. The catch-all matches the whole
	// "host:port" subject, so \1 carries the port too and splitServicePort splits on
	// the last colon — hence the service label keeps the original ":80".
	res, err = r.Resolve("other.example", 80)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindService || res.Service != "other.example:80" {
		t.Fatalf("got %+v, want KindService other.example:80", res)
	}
}

// TestRouterLocalSurvivesDotlessSuffix covers the real misconfiguration that makes
// locals-first load-bearing: a service-host suffix spelled without its leading dot
// is accepted, and its rule then matches the suffix's own apex, rewrites it to
// ":80", and fails the CONNECT instead of falling through. The published name must
// resolve anyway.
func TestRouterLocalSurvivesDotlessSuffix(t *testing.T) {
	r, err := NewSuffixRouter("cornus.internal") // no leading dot
	if err != nil {
		t.Fatal(err)
	}
	// Without a published name the apex is claimed by the rule and errors.
	if _, err := r.Resolve("cornus.internal", 80); err == nil {
		t.Fatal("expected the dotless-suffix rule to claim and fail the apex")
	}
	r.RegisterLocal("cornus.internal", 80, &fakeLocal{})
	res, err := r.Resolve("cornus.internal", 80)
	if err != nil {
		t.Fatalf("published name must outrank the rule: %v", err)
	}
	if res.Kind != KindLocal {
		t.Fatalf("Kind = %v, want KindLocal", res.Kind)
	}
}

func TestRouterUnregisterAndClearLocals(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterLocal("cornus.internal", 80, &fakeLocal{})
	r.UnregisterLocal("cornus.internal", 80)
	if res, _ := r.Resolve("cornus.internal", 80); res.Kind != KindDirect {
		t.Fatalf("after UnregisterLocal, Kind = %v, want KindDirect", res.Kind)
	}

	r.RegisterLocal("a.internal", 80, &fakeLocal{})
	r.RegisterLocal("b.internal", 80, &fakeLocal{})
	r.ClearLocals()
	for _, h := range []string{"a.internal", "b.internal"} {
		if res, _ := r.Resolve(h, 80); res.Kind != KindDirect {
			t.Fatalf("after ClearLocals, %s Kind = %v, want KindDirect", h, res.Kind)
		}
	}
}

func TestRegisterLocalIgnoresInvalid(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterLocal("", 80, &fakeLocal{})
	r.RegisterLocal("x.internal", 0, &fakeLocal{})
	r.RegisterLocal("y.internal", 70000, &fakeLocal{})
	r.RegisterLocal("z.internal", 80, nil)
	for _, h := range []string{"", "x.internal", "y.internal", "z.internal"} {
		if h == "" {
			continue
		}
		if res, _ := r.Resolve(h, 80); res.Kind != KindDirect {
			t.Fatalf("%s should not have been published, got %v", h, res.Kind)
		}
	}
}

// TestProxyRoutesLocalToItsDialer drives a real SOCKS5 CONNECT through the proxy
// and asserts the published name reaches its LocalDialer and never the
// port-forward transport or the direct dialer.
func TestProxyRoutesLocalToItsDialer(t *testing.T) {
	localEcho := echoServer(t)
	dialer := &fakeDialer{}
	direct := &fakeDirect{}
	fl := &fakeLocal{target: localEcho.Addr().String()}

	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	router.RegisterLocal("cornus.internal", 80, fl)

	p, err := Start(context.Background(), dialer, router, "127.0.0.1:0", WithDirectDialer(direct))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if got := roundTrip(t, p.Addr(), "cornus.internal:80"); got != "hello" {
		t.Fatalf("local echo = %q", got)
	}
	if fl.got() != 1 {
		t.Fatalf("LocalDialer calls = %d, want 1", fl.got())
	}
	if calls := dialer.got(); len(calls) != 0 {
		t.Fatalf("PortForward calls = %v, want none", calls)
	}
	if addrs := direct.got(); len(addrs) != 0 {
		t.Fatalf("direct dials = %v, want none", addrs)
	}
}

// TestStartRefusesNonLoopbackBind covers security fix (a): this proxy offers only
// the no-auth method and dials arbitrary destinations from its host, so binding it
// off-host is an open proxy and must be refused unless explicitly opted into.
func TestStartRefusesNonLoopbackBind(t *testing.T) {
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"0.0.0.0:0", ":0"} {
		p, err := Start(context.Background(), &fakeDialer{}, router, addr)
		if err == nil {
			p.Close()
			t.Fatalf("Start(%q) succeeded, want refusal", addr)
		}
		if !strings.Contains(err.Error(), "refusing to bind") {
			t.Fatalf("Start(%q) err = %v, want a refusing-to-bind error", addr, err)
		}
	}
}

func TestStartAllowsLoopbackBind(t *testing.T) {
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		p, err := Start(context.Background(), &fakeDialer{}, router, addr)
		if err != nil {
			t.Fatalf("Start(%q): %v", addr, err)
		}
		p.Close()
	}
}

func TestStartAllowsNonLoopbackWithOptIn(t *testing.T) {
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), &fakeDialer{}, router, "0.0.0.0:0", WithAllowNonLoopback(true))
	if err != nil {
		t.Fatalf("Start with opt-in: %v", err)
	}
	defer p.Close()
}

// TestNonLoopbackProxyRefusesLoopbackPivot covers the other half of fix (a): even
// when a caller opts into an off-host bind, a remote client must not be able to use
// the proxy to reach services on the proxy host itself.
func TestNonLoopbackProxyRefusesLoopbackPivot(t *testing.T) {
	victim := echoServer(t)
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), &fakeDialer{}, router, "0.0.0.0:0", WithAllowNonLoopback(true))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// The proxy bound 0.0.0.0; reach it over loopback and try to pivot to a
	// loopback service. The CONNECT must fail rather than splice.
	_, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", port), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := d.Dial("tcp", victim.Addr().String()); err == nil {
		c.Close()
		t.Fatal("loopback pivot succeeded through a non-loopback proxy, want refusal")
	}
}

// TestLoopbackProxyStillDialsLoopback guards against over-reach: the guard applies
// only to a non-loopback bind, so the ordinary loopback proxy keeps working (E2E
// scenarios and local tooling depend on reaching 127.0.0.1 through it).
func TestLoopbackProxyStillDialsLoopback(t *testing.T) {
	target := echoServer(t)
	router, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), &fakeDialer{}, router, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if got := roundTrip(t, p.Addr(), target.Addr().String()); got != "hello" {
		t.Fatalf("echo = %q", got)
	}
}
