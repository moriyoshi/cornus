package clientagent

import (
	"context"
	"io"
	"net"
	"net/http"
	"regexp"
	"testing"

	"cornus/pkg/clientconduit"

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
		Conduit: socks5Conduit(),
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

	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}, Conduit: socks5Conduit()}); !resp.OK {
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
	req.Conduit = ConduitCfg{Mode: clientconduit.ModePortForward}
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
