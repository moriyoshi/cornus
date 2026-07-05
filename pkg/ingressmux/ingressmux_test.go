package ingressmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cornus/pkg/api"
)

// echoDialer answers every PortForward with an in-memory HTTP server that echoes
// back the workload, port, Host and path it was reached with, so a test can prove
// exactly which rule a request resolved to.
type echoDialer struct {
	failFor map[string]error
}

func (d echoDialer) PortForward(ctx context.Context, name string, port int, _ string) (net.Conn, error) {
	if err := d.failFor[name]; err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "workload=%s port=%d host=%s path=%s xfh=%s", name, port, r.Host, r.URL.Path, r.Header.Get("X-Forwarded-Host"))
		})}
		_ = srv.Serve(&singleConnListener{conn: server, done: make(chan struct{})})
	}()
	return client, nil
}

// singleConnListener hands an http.Server exactly one already-established
// connection and then blocks, so the server never serves the same pipe twice.
type singleConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var first bool
	l.once.Do(func() { first = true })
	if first {
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	close(l.done)
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// serveOnce runs one request through the proxy and returns the response.
func serveOnce(t *testing.T, p *Proxy, host, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec.Result()
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestTableLookupLongestPathWins(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"app.example.com"}, Path: "/", TargetPort: 80}})
	tbl.Set("api", []api.IngressRoute{{Hosts: []string{"app.example.com"}, Path: "/api", TargetPort: 8080}})

	for _, tc := range []struct{ path, want string }{
		{"/", "web"},
		{"/index.html", "web"},
		{"/api", "api"},
		{"/api/v1/things", "api"},
		{"/apiary", "web"}, // element-boundary match, not a string prefix
	} {
		route, known, ok := tbl.Lookup("app.example.com", tc.path)
		if !known || !ok {
			t.Fatalf("Lookup(%q): known=%v ok=%v, want both true", tc.path, known, ok)
		}
		if route.Workload != tc.want {
			t.Errorf("Lookup(%q) = %q, want %q", tc.path, route.Workload, tc.want)
		}
	}
}

func TestTableLookupExactBeatsPrefixOfEqualLength(t *testing.T) {
	tbl := NewTable()
	tbl.Set("prefix", []api.IngressRoute{{Hosts: []string{"h"}, Path: "/x", PathType: "Prefix", TargetPort: 1}})
	tbl.Set("exact", []api.IngressRoute{{Hosts: []string{"h"}, Path: "/x", PathType: "Exact", TargetPort: 2}})

	route, _, ok := tbl.Lookup("h", "/x")
	if !ok || route.Workload != "exact" {
		t.Fatalf("Lookup(/x) = %q (ok=%v), want exact", route.Workload, ok)
	}
	// The Exact rule does not match a deeper path, so the Prefix rule takes it.
	route, _, ok = tbl.Lookup("h", "/x/y")
	if !ok || route.Workload != "prefix" {
		t.Fatalf("Lookup(/x/y) = %q (ok=%v), want prefix", route.Workload, ok)
	}
}

func TestTableHostsAreCanonicalized(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"  App.Example.COM. "}, TargetPort: 80}})

	if got := tbl.Hosts(); len(got) != 1 || got[0] != "app.example.com" {
		t.Fatalf("Hosts() = %v, want [app.example.com]", got)
	}
	// A request arriving with different case and an explicit port still resolves.
	if _, known, ok := tbl.Lookup("APP.example.com:8443", "/"); !known || !ok {
		t.Errorf("Lookup with mixed case and a port: known=%v ok=%v, want both true", known, ok)
	}
}

func TestTableSetReplacesAndRemoveWithdraws(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"old.example.com"}, TargetPort: 80}})
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"new.example.com"}, TargetPort: 80}})

	if _, known, _ := tbl.Lookup("old.example.com", "/"); known {
		t.Error("a redeploy must withdraw the host it previously served")
	}
	if _, known, _ := tbl.Lookup("new.example.com", "/"); !known {
		t.Error("a redeploy must serve its new host")
	}

	tbl.Remove("web")
	if !tbl.Empty() {
		t.Error("table should be empty after the last workload is removed")
	}
	if _, known, _ := tbl.Lookup("new.example.com", "/"); known {
		t.Error("a removed workload must stop being served")
	}
}

func TestTableHostAlias(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"app.example.com"}, Path: "/", TargetPort: 80}})
	tbl.HostAlias("Random-Tunnel.ngrok.app", "app.example.com")

	route, known, ok := tbl.Lookup("random-tunnel.ngrok.app", "/")
	if !known || !ok || route.Workload != "web" {
		t.Fatalf("aliased lookup = %q (known=%v ok=%v), want web", route.Workload, known, ok)
	}
	// An alias is a name for an existing host, not a route of its own.
	if got := tbl.Hosts(); len(got) != 1 || got[0] != "app.example.com" {
		t.Errorf("Hosts() = %v, want only the canonical host", got)
	}

	tbl.HostAlias("random-tunnel.ngrok.app", "")
	if _, known, _ := tbl.Lookup("random-tunnel.ngrok.app", "/"); known {
		t.Error("clearing an alias must stop it resolving")
	}
}

func TestProxyRoutesByHostAndPath(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"app.example.com"}, Path: "/", TargetPort: 80}})
	tbl.Set("api", []api.IngressRoute{{Hosts: []string{"app.example.com"}, Path: "/api", TargetPort: 8080}})
	p := NewProxy(tbl, echoDialer{})

	resp := serveOnce(t, p, "app.example.com", "/api/v1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got, want := bodyOf(t, resp), "workload=api port=8080 host=app.example.com path=/api/v1"; !strings.Contains(got, want) {
		t.Errorf("body = %q, want it to contain %q", got, want)
	}

	resp = serveOnce(t, p, "app.example.com", "/")
	if got, want := bodyOf(t, resp), "workload=web port=80"; !strings.Contains(got, want) {
		t.Errorf("body = %q, want it to contain %q", got, want)
	}
}

func TestProxyPreservesClientHostAndSetsForwardedHeaders(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"app.example.com"}, TargetPort: 80}})
	tbl.HostAlias("abc.ngrok.app", "app.example.com")
	p := NewProxy(tbl, echoDialer{})

	// Reached through the tunnel hostname: the app must see the name the client
	// actually used, so its redirects and cookies stay on that name.
	body := bodyOf(t, serveOnce(t, p, "abc.ngrok.app", "/"))
	if !strings.Contains(body, "host=abc.ngrok.app") {
		t.Errorf("body = %q, want the upstream to see the client's Host", body)
	}
	if !strings.Contains(body, "xfh=abc.ngrok.app") {
		t.Errorf("body = %q, want X-Forwarded-Host set", body)
	}
}

func TestProxyUnknownHostIs421AndUnmatchedPathIs404(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"app.example.com"}, Path: "/api", PathType: "Exact", TargetPort: 80}})
	p := NewProxy(tbl, echoDialer{})

	if got := serveOnce(t, p, "nope.example.com", "/").StatusCode; got != http.StatusMisdirectedRequest {
		t.Errorf("unknown host status = %d, want 421", got)
	}
	if got := serveOnce(t, p, "app.example.com", "/other").StatusCode; got != http.StatusNotFound {
		t.Errorf("unmatched path status = %d, want 404", got)
	}
}

func TestProxyUnreachableWorkloadExplainsItself(t *testing.T) {
	tbl := NewTable()
	tbl.Set("web", []api.IngressRoute{{Hosts: []string{"app.example.com"}, TargetPort: 80}})
	p := NewProxy(tbl, echoDialer{failFor: map[string]error{"web": errors.New("no ready instance")}})

	resp := serveOnce(t, p, "app.example.com", "/")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	// An empty-body 502 renders as a blank page and reads like a dropped
	// connection; the cause has to be in the body.
	for _, want := range []string{"web", "80", "no ready instance"} {
		if !strings.Contains(body, want) {
			t.Errorf("502 body = %q, want it to mention %q", body, want)
		}
	}
}
