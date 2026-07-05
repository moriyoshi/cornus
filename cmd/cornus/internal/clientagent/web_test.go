package clientagent

import (
	"context"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"cornus/pkg/clientconduit"
	"cornus/pkg/socks5"

	"golang.org/x/net/proxy"
)

func socks5Conduit() ConduitCfg {
	return ConduitCfg{Mode: clientconduit.ModeSocks5, Socks5Listen: "127.0.0.1:0"}
}

func webServeReq(name string, port int) Request {
	return Request{
		Action:  "web-serve",
		Web:     WebSpec{Name: name, Port: port},
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Conduit: ToWireConduit(socks5Conduit()),
	}
}

func TestAgentWebServeAndStop(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	resp, fe := a.doWebServe(webServeReq("cornus.internal", 80))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	// Inventory lists the published name, and one shared connState + conduit back it.
	if inv := a.inventory(); len(inv.Webs) != 1 || inv.Webs[0] != "cornus.internal:80" {
		t.Fatalf("inventory webs = %v, want [cornus.internal:80]", inv.Webs)
	}
	a.mu.Lock()
	nConns := len(a.conns)
	a.mu.Unlock()
	if nConns != 1 {
		t.Fatalf("conns = %d, want 1", nConns)
	}

	// web-stop releases everything.
	if resp := a.doWebStop(Request{Web: WebSpec{Name: "cornus.internal"}}); !resp.OK {
		t.Fatalf("web-stop = %+v", resp)
	}
	a.mu.Lock()
	nConns, nWebs := len(a.conns), len(a.webs)
	a.mu.Unlock()
	if nConns != 0 || nWebs != 0 {
		t.Fatalf("after web-stop conns=%d webs=%d, want 0,0", nConns, nWebs)
	}
}

// TestAgentWebServeSharesConduitWithDocker is the point of the feature: a docker
// frontend and a web UI with identical socks5 config join ONE shared conduit
// (refs==2), so one browser proxy setting reaches both.
func TestAgentWebServeSharesConduitWithDocker(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := t.TempDir() + "/docker.sock"

	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: ToWireConduit(socks5Conduit())}); !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	resp, fe := a.doWebServe(webServeReq("cornus.internal", 80))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	a.mu.Lock()
	if len(a.conns) != 1 {
		a.mu.Unlock()
		t.Fatalf("conns = %d, want 1 (docker + web must share)", len(a.conns))
	}
	var refs int
	for _, cs := range a.conns {
		if len(cs.conduit) != 1 {
			a.mu.Unlock()
			t.Fatalf("conduits on the shared conn = %d, want 1", len(cs.conduit))
		}
		for _, es := range cs.conduit {
			refs = es.refs
		}
	}
	a.mu.Unlock()
	if refs != 2 {
		t.Fatalf("shared conduit refs = %d, want 2 (docker + web)", refs)
	}

	// Releasing the web keeps the shared conduit up for docker.
	a.reapWeb("cornus.internal")
	a.mu.Lock()
	for _, cs := range a.conns {
		for _, es := range cs.conduit {
			refs = es.refs
		}
	}
	nConns := len(a.conns)
	a.mu.Unlock()
	if nConns != 1 || refs != 1 {
		t.Fatalf("after web reap conns=%d refs=%d, want 1,1", nConns, refs)
	}
}

func TestAgentWebServeRejectsDuplicateName(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	if resp, fe := a.doWebServe(webServeReq("cornus.internal", 80)); !resp.OK || fe == nil {
		t.Fatalf("first web-serve = %+v", resp)
	}
	resp, fe := a.doWebServe(webServeReq("cornus.internal", 80))
	if resp.OK || fe != nil {
		t.Fatalf("duplicate name should error, got %+v", resp)
	}
}

func TestAgentWebServeRejectsPortForwardMode(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	req := webServeReq("cornus.internal", 80)
	req.Conduit = ToWireConduit(ConduitCfg{Mode: clientconduit.ModePortForward})
	resp, fe := a.doWebServe(req)
	if resp.OK || fe != nil {
		t.Fatalf("port-forward mode should error, got %+v", resp)
	}
}

// TestAgentWebKeepsAgentAlive locks the idle-exit fix: a published web UI is a
// work unit, so idleCheck must not stop the agent while one is registered, and
// must once it is withdrawn.
func TestAgentWebKeepsAgentAlive(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	if resp, fe := a.doWebServe(webServeReq("cornus.internal", 80)); !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}
	a.idleCheck()
	if a.ctx.Err() != nil {
		t.Fatal("idleCheck stopped the agent while a web UI was published")
	}
	a.reapWeb("cornus.internal")
	a.idleCheck()
	if a.ctx.Err() == nil {
		t.Fatal("idleCheck did not stop the agent after the web UI was withdrawn")
	}
}

// TestAgentWebServeEndToEnd drives a real browser-style request: SOCKS5 CONNECT to
// cornus.internal:80 through the conduit's proxy, resolved to the in-process BFF,
// answering /.cornus/web/config. It proves the whole path (proxy -> KindLocal ->
// memlisten -> BFF) composes.
func TestAgentWebServeEndToEnd(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	resp, fe := a.doWebServe(webServeReq("cornus.internal", 80))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	// The proxy's bound address is in the conduit banner.
	proxyAddr := regexp.MustCompile(`127\.0\.0\.1:\d+`).FindString(firstOr(resp.Banners))
	if proxyAddr == "" {
		t.Fatalf("no proxy address in banners %v", resp.Banners)
	}
	d, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	cl := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.(proxy.ContextDialer).DialContext(ctx, "tcp", "cornus.internal:80")
		},
	}}
	waitFor(t, func() bool {
		r, err := cl.Get("http://cornus.internal/.cornus/web/config")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		_, _ = io.Copy(io.Discard, r.Body)
		return r.StatusCode == http.StatusOK
	}, "the published UI to answer through the proxy")
}

func firstOr(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// --- joining an existing shared conduit ------------------------------------

// socks5ConduitWith is socks5Conduit with a distinguishing service-host suffix.
// Every conduit in these tests binds 127.0.0.1:0, which is load-bearing: a second
// conduit therefore STARTS FINE, so "web-serve returned OK" proves nothing about
// joining and the assertions have to be on the conduit count and the refcounts.
func socks5ConduitWith(suffix string) ConduitCfg {
	cfg := socks5Conduit()
	cfg.Socks5Suffix = suffix
	return cfg
}

func joinReq(name string, cfg ConduitCfg) Request {
	req := webServeReq(name, 80)
	req.Web.JoinConduit = true
	req.Conduit = ToWireConduit(cfg)
	return req
}

// theConduit returns the agent's single connState and its conduits, failing if
// there is not exactly one connection.
func theConn(t *testing.T, a *Agent) *connState {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.conns) != 1 {
		t.Fatalf("conns = %d, want 1", len(a.conns))
	}
	for _, cs := range a.conns {
		return cs
	}
	return nil
}

func conduitCounts(t *testing.T, a *Agent, cs *connState) (n int, refs map[conduitKey]int) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	refs = map[conduitKey]int{}
	for k, es := range cs.conduit {
		refs[k] = es.refs
	}
	return len(cs.conduit), refs
}

// The headline behavior: a `cornus web` that pinned nothing publishes in the
// conduit the agent ALREADY runs, even though its own resolved settings differ.
// The docker frontend here stands in for whatever brought the proxy up first.
func TestWebServeJoinsExistingConduit(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := t.TempDir() + "/docker.sock"

	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: ToWireConduit(socks5ConduitWith(".demo.internal"))}); !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	cs := theConn(t, a)
	a.mu.Lock()
	dockerKey := a.dockers[sock].egKey
	a.mu.Unlock()

	// Different settings on purpose — this is the case that used to fork.
	resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".other.internal")))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	n, refs := conduitCounts(t, a, cs)
	if n != 1 {
		t.Fatalf("conduits = %d, want 1: the UI started a second proxy instead of joining the one already running", n)
	}
	if refs[dockerKey] != 2 {
		t.Fatalf("shared conduit refs = %d, want 2 (docker + web)", refs[dockerKey])
	}
	if fe.egKey != dockerKey {
		t.Fatal("the web frontend recorded a key other than the conduit it joined; its release would not find the conduit")
	}
	// The name follows the conduit it JOINED, not the settings it asked for. Both
	// spellings resolve through the proxy (router locals beat every rule), so only
	// this assertion can tell the two apart — and the wrong one is refused by the
	// BFF's own Host check with a 421.
	if resp.WebName != "demo.internal" {
		t.Fatalf("WebName = %q, want demo.internal (the JOINED conduit's apex, not the requested .other.internal)", resp.WebName)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("joining a conduit with settings other than the requested ones must say so")
	}
}

// The refcount half of the same story, and the one that fails silently: the key a
// frontend releases under must be the key it acquired. releaseConduitLocked returns
// without complaint for a key that is not in the map, so recording the REQUESTED
// key leaks the conduit forever with no error anywhere.
func TestWebServeJoinedConduitReleasesExactlyOnce(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := t.TempDir() + "/docker.sock"
	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: ToWireConduit(socks5ConduitWith(".demo.internal"))}); !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	cs := theConn(t, a)
	resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".other.internal")))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	// Under-release (the leak) shows up as refs stuck at 2...
	a.reapWeb(resp.WebName)
	n, refs := conduitCounts(t, a, cs)
	if n != 1 {
		t.Fatalf("conduits = %d after the web was reaped, want 1 (docker still holds it)", n)
	}
	for k, r := range refs {
		if r != 1 {
			t.Fatalf("conduit %v refs = %d after the web was reaped, want 1", k, r)
		}
	}
	// ...and over-release as the conduit already being gone here.
	if resp := a.doDockerStop(Request{Socket: sock}); !resp.OK {
		t.Fatalf("docker-stop = %+v", resp)
	}
	if n, _ := conduitCounts(t, a, cs); n != 0 {
		t.Fatalf("conduits = %d after the last tenant left, want 0", n)
	}
}

// Determinism. More than one adoptable conduit means genuinely different bind
// addresses, so an answer that depends on map order hands the same agent a
// different proxy on every publish. A single call would be a coin flip that passes
// half the time; the loop drives that to nothing.
func TestPickSharedConduitIsDeterministic(t *testing.T) {
	shared := func(suffix string, refs int) (conduitKey, *conduitState) {
		cfg := canonicalConduitCfg(socks5ConduitWith(suffix))
		return cfg.Identity(""), &conduitState{cfg: cfg, refs: refs}
	}
	kA, esA := shared(".a.internal", 1)
	kB, esB := shared(".b.internal", 3)
	conduits := map[conduitKey]*conduitState{kA: esA, kB: esB}

	for i := 0; i < 64; i++ {
		got, others, ok := pickSharedConduit(conduits, conduitKey{})
		if !ok {
			t.Fatal("two shared socks5 conduits are adoptable, but none was picked")
		}
		if got != kB {
			t.Fatalf("iteration %d picked the conduit with %d refs, want the most-shared one (%d refs)", i, conduits[got].refs, esB.refs)
		}
		if len(others) != 1 || others[0] != kA {
			t.Fatalf("iteration %d: others = %v, want exactly the runner-up", i, others)
		}
	}

	// An exact match on the requested settings outranks the most-shared one, so
	// joining never changes where an already-agreeing caller would have landed.
	if got, _, _ := pickSharedConduit(conduits, kA); got != kA {
		t.Fatal("an exact settings match must win outright")
	}
}

// Private conduits are not adoptable. A port-forward conduit resolves no names at
// all, and a session-local proxy is isolated on purpose — publishing the UI into
// one would put it exactly where the operator asked for privacy.
func TestWebServeDoesNotJoinPrivateConduits(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	conn := ConnSpec{Server: "http://fake:5000"}

	if resp, _ := a.dispatch(fwdOnlyReqWith("proj", "svc", ToWireConduit(ConduitCfg{Mode: clientconduit.ModePortForward}))); !resp.OK {
		t.Fatalf("up (port-forward) = %+v", resp)
	}
	local := socks5ConduitWith(".private.internal")
	local.Socks5SessionLocal = true
	if resp := a.doDockerServe(Request{Socket: t.TempDir() + "/d.sock", Conn: conn, Conduit: ToWireConduit(local)}); !resp.OK {
		t.Fatalf("docker-serve (session-local) = %+v", resp)
	}
	cs := theConn(t, a)
	before, refsBefore := conduitCounts(t, a, cs)

	resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".web.internal")))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}
	after, refsAfter := conduitCounts(t, a, cs)
	if after != before+1 {
		t.Fatalf("conduits %d -> %d, want one MORE: neither the port-forward nor the session-local conduit is adoptable", before, after)
	}
	for k, r := range refsBefore {
		if refsAfter[k] != r {
			t.Fatalf("conduit %v refs %d -> %d: the web UI joined a conduit it must not touch", k, r, refsAfter[k])
		}
	}
	if resp.WebName != "web.internal" {
		t.Fatalf("WebName = %q, want web.internal (its own fallback settings)", resp.WebName)
	}
}

// The flag must actually GATE the behavior. Without this, every test above would
// pass just as well with joining hard-coded on — silently breaking the contract
// that naming an address or suffix means "use exactly these settings".
func TestWebServeWithoutJoinStartsItsOwn(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := t.TempDir() + "/docker.sock"
	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: ToWireConduit(socks5ConduitWith(".demo.internal"))}); !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	cs := theConn(t, a)

	req := joinReq("", socks5ConduitWith(".pinned.internal"))
	req.Web.JoinConduit = false // the caller PINNED settings
	resp, fe := a.doWebServe(req)
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}
	if n, _ := conduitCounts(t, a, cs); n != 2 {
		t.Fatalf("conduits = %d, want 2: pinned settings must be used exactly, not overridden by a join", n)
	}
	if resp.WebName != "pinned.internal" {
		t.Fatalf("WebName = %q, want pinned.internal", resp.WebName)
	}
}

// With nothing to join, the fallback settings still stand a conduit up — the
// standalone `cornus web --publish-in-conduit` case.
func TestWebServeStandaloneCreatesConduit(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".solo.internal")))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}
	if n, _ := conduitCounts(t, a, theConn(t, a)); n != 1 {
		t.Fatalf("conduits = %d, want 1", n)
	}
	if resp.WebName != "solo.internal" {
		t.Fatalf("WebName = %q, want solo.internal", resp.WebName)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("creating the only conduit warned about nothing in particular: %v", resp.Warnings)
	}
}

// A session-local request is refused rather than quietly published somewhere
// private. It is also what keeps the conduit session string inert in doWebServe,
// which is what lets the name be derived AFTER the conduit is chosen.
func TestWebServeRejectsSessionLocal(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	cfg := socks5Conduit()
	cfg.Socks5SessionLocal = true
	resp, fe := a.doWebServe(joinReq("ui.internal", cfg))
	if resp.OK || fe != nil {
		t.Fatalf("a session-local conduit should be refused, got %+v", resp)
	}
}

// The suffix -> apex rule, in the one place that now applies it.
func TestDefaultPublishedNameFromAdoptedConduit(t *testing.T) {
	for _, tc := range []struct {
		cfg  ConduitCfg
		want string
		why  string
	}{
		{socks5ConduitWith(".demo.internal"), "demo.internal", "the everyday dotted suffix"},
		{socks5ConduitWith("demo.internal"), "demo.internal", "a suffix written without its leading dot"},
		{socks5ConduitWith(""), "cornus.internal", "no suffix at all falls back to the socks5 default"},
		{func() ConduitCfg {
			cfg := socks5Conduit()
			cfg.Socks5Resolve = []socks5.Rule{{Pattern: "^x:(.*)$", Replace: `y:\1`}}
			return canonicalConduitCfg(cfg)
		}(), "cornus.internal", "a rules-driven conduit has no apex; the default still resolves via router locals"},
	} {
		if got := defaultPublishedName(tc.cfg); got != tc.want {
			t.Errorf("defaultPublishedName(%q) = %q, want %q (%s)", tc.cfg.Socks5Suffix, got, tc.want, tc.why)
		}
	}
}

// Refusing a duplicate name must not leak the refs taken to get far enough to
// notice. The check now runs AFTER the conduit is resolved (it has to — the name
// may come from that conduit), so the failure path has more to give back than it
// used to.
func TestWebServeDuplicateNameReleasesRefs(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	if resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".demo.internal"))); !resp.OK || fe == nil {
		t.Fatalf("first web-serve = %+v", resp)
	}
	cs := theConn(t, a)
	before, refsBefore := conduitCounts(t, a, cs)

	resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".demo.internal")))
	if resp.OK || fe != nil {
		t.Fatalf("duplicate name should error, got %+v", resp)
	}
	after, refsAfter := conduitCounts(t, a, cs)
	if after != before {
		t.Fatalf("conduits %d -> %d across a refused publish", before, after)
	}
	for k, r := range refsBefore {
		if refsAfter[k] != r {
			t.Fatalf("conduit %v refs %d -> %d across a refused publish: the failure path leaked a reference", k, r, refsAfter[k])
		}
	}
	a.mu.Lock()
	connRefs := cs.refs
	a.mu.Unlock()
	if connRefs != 1 {
		t.Fatalf("connState refs = %d after a refused publish, want 1", connRefs)
	}
}

// The derived name is a SECURITY-relevant value, not a label: webbff pins its
// DNS-rebinding Host allow-list to it. A name carrying the requested suffix while
// the UI sits in a conduit with a different one still resolves through the proxy —
// router locals are consulted before any rule — and is then refused with 421. So
// "the request got a response" is exactly the false pass here, and only the 200
// tells the two apart.
func TestWebServeDerivedNamePassesHostGuard(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := t.TempDir() + "/docker.sock"
	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: ToWireConduit(socks5ConduitWith(".demo.internal"))}); !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	resp, fe := a.doWebServe(joinReq("", socks5ConduitWith(".other.internal")))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}
	name := resp.WebName

	proxyAddr := regexp.MustCompile(`127\.0\.0\.1:\d+`).FindString(firstOr(resp.Banners))
	if proxyAddr == "" {
		t.Fatalf("no proxy address in banners %v", resp.Banners)
	}
	d, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	cl := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.(proxy.ContextDialer).DialContext(ctx, "tcp", name+":80")
		},
	}}
	// Polled inline rather than through waitFor, whose message is a plain string
	// built before the first attempt: the STATUS is the whole diagnostic here (421
	// says the allow-list disagrees, and is a completely different bug from a
	// connection that never came up), so it has to be read after the last attempt.
	var last int
	var lastErr error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := cl.Get("http://" + name + "/.cornus/web/config")
		if err != nil {
			lastErr = err
			time.Sleep(5 * time.Millisecond)
			continue
		}
		last, lastErr = r.StatusCode, nil
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if last == http.StatusOK {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the UI published as %s answered %d (err %v) through the proxy, want 200; 421 means the BFF's Host allow-list was pinned to a name other than the one it was published under", name, last, lastErr)
}

// --- bind conflicts --------------------------------------------------------

// freeAddr returns a loopback address that was bindable a moment ago. It is
// inherently a small race, which is fine: the point is a CONCRETE port so two
// conduits are asked for the same one, not an exclusive reservation.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// Two conduits that disagree cannot both bind one address. Left to the kernel that
// reads "bind: address already in use", which names neither the session in the way
// nor the setting that made this a second conduit rather than a shared one.
func TestConduitBindConflictNamesTheHolder(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	addr := freeAddr(t)

	held := socks5ConduitWith(".demo.internal")
	held.Socks5Listen = addr
	resp, fe := a.doWebServe(joinReq("", held))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}
	name := resp.WebName

	// Same address, different settings, and pinned — so joining is not an option
	// and the collision is real.
	want := socks5ConduitWith(".other.internal")
	want.Socks5Listen = addr
	got := a.doDockerServe(Request{Socket: t.TempDir() + "/d.sock", Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: ToWireConduit(want)})
	if got.OK {
		t.Fatal("a second SOCKS5 proxy on a held address must be refused")
	}
	for _, want := range []string{addr, name, "cornus web"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("error does not name %q: %s", want, got.Error)
		}
	}
	if !strings.Contains(got.Error, "other.internal") {
		t.Errorf("error does not say WHICH setting differs: %s", got.Error)
	}
	if strings.Contains(got.Error, "address already in use") {
		t.Errorf("the raw bind error reached the user instead of the explanation: %s", got.Error)
	}

	// The refused acquire must disturb nothing: the published UI is still there.
	if n, _ := conduitCounts(t, a, theConn(t, a)); n != 1 {
		t.Fatalf("conduits = %d after a refused acquire, want the 1 that was already there", n)
	}
	a.mu.Lock()
	_, stillPublished := a.webs[name]
	a.mu.Unlock()
	if !stillPublished {
		t.Fatal("a refused acquire withdrew the published UI it collided with")
	}
}

// The predicate must be the ADDRESS, not the disagreement. Two conduits with
// different settings on different ports have always coexisted, and an over-broad
// refusal would break that (socks5-coexist.star pins it end to end).
func TestConduitDifferentAddressesDoNotConflict(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	conn := ConnSpec{Server: "http://fake:5000"}

	first := socks5ConduitWith(".demo.internal")
	first.Socks5Listen = freeAddr(t)
	if resp := a.doDockerServe(Request{Socket: t.TempDir() + "/a.sock", Conn: conn, Conduit: ToWireConduit(first)}); !resp.OK {
		t.Fatalf("first docker-serve = %+v", resp)
	}
	second := socks5ConduitWith(".other.internal")
	second.Socks5Listen = freeAddr(t)
	if resp := a.doDockerServe(Request{Socket: t.TempDir() + "/b.sock", Conn: conn, Conduit: ToWireConduit(second)}); !resp.OK {
		t.Fatalf("a conduit on a DIFFERENT address must be allowed: %+v", resp)
	}
	if n, _ := conduitCounts(t, a, theConn(t, a)); n != 2 {
		t.Fatalf("conduits = %d, want 2", n)
	}

	// An ephemeral bind takes whatever is free, so it can never collide either.
	ephemeral := socks5ConduitWith(".ephemeral.internal")
	if resp := a.doDockerServe(Request{Socket: t.TempDir() + "/c.sock", Conn: conn, Conduit: ToWireConduit(ephemeral)}); !resp.OK {
		t.Fatalf("an ephemeral (127.0.0.1:0) bind must never be refused: %+v", resp)
	}
}
