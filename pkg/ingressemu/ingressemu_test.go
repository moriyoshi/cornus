package ingressemu

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"cornus/pkg/memlisten"
)

// fakeDialer is a portfwd.Dialer that dials a fixed backend address and records the
// (name, port) each PortForward was asked for.
type fakeDialer struct {
	target string
	mu     sync.Mutex
	calls  []string
}

func (f *fakeDialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s:%d/%s", name, port, proto))
	f.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.target)
}

func (f *fakeDialer) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestResolve(t *testing.T) {
	ports := []api.PortMapping{{Container: 8080}, {Container: 9090}}
	cases := []struct {
		name       string
		spec       *api.IngressSpec
		wantHosts  []string
		wantPort   int
		wantErr    bool
		suffixDom  string
		deployName string
	}{
		{name: "explicit host, first port", spec: &api.IngressSpec{Hosts: []string{"app.example.com"}}, wantHosts: []string{"app.example.com"}, wantPort: 8080, deployName: "web"},
		{name: "explicit port must match", spec: &api.IngressSpec{Hosts: []string{"a.example.com"}, Port: 9090}, wantHosts: []string{"a.example.com"}, wantPort: 9090, deployName: "web"},
		{name: "explicit port mismatch errors", spec: &api.IngressSpec{Hosts: []string{"a.example.com"}, Port: 1234}, wantErr: true, deployName: "web"},
		{name: "derive from name + default suffix", spec: &api.IngressSpec{Enabled: true}, wantHosts: []string{"web.cornus.internal"}, wantPort: 8080, deployName: "web"},
		{name: "derive from subdomain + domain override", spec: &api.IngressSpec{Enabled: true, Domain: "preview.example.com", Subdomain: "web.proj"}, wantHosts: []string{"web.proj.preview.example.com"}, wantPort: 8080, deployName: "web"},
		{name: "apex token", spec: &api.IngressSpec{Hosts: []string{"@"}, Domain: "example.com"}, wantHosts: []string{"example.com"}, wantPort: 8080, deployName: "web"},
		{name: "sanitized name label", spec: &api.IngressSpec{Enabled: true}, wantHosts: []string{"my-web.cornus.internal"}, wantPort: 8080, deployName: "My_Web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hosts, port, err := Resolve(tc.spec, ports, tc.deployName, tc.suffixDom)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Resolve err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if fmt.Sprint(hosts) != fmt.Sprint(tc.wantHosts) {
				t.Errorf("hosts = %v, want %v", hosts, tc.wantHosts)
			}
			if port != tc.wantPort {
				t.Errorf("port = %d, want %d", port, tc.wantPort)
			}
		})
	}
}

func TestResolveNoPortsErrors(t *testing.T) {
	if _, _, err := Resolve(&api.IngressSpec{Hosts: []string{"a.example.com"}}, nil, "web", ""); err == nil {
		t.Fatal("Resolve with no published ports should error")
	}
}

func TestPathMatches(t *testing.T) {
	cases := []struct {
		p, pathType, req string
		want             bool
	}{
		{"/", "Prefix", "/anything", true},
		{"", "Prefix", "/anything", true},
		{"/api", "Prefix", "/api", true},
		{"/api", "Prefix", "/api/v1", true},
		{"/api", "Prefix", "/apix", false},
		{"/api", "Exact", "/api", true},
		{"/api", "Exact", "/api/v1", false},
	}
	for _, tc := range cases {
		if got := pathMatches(tc.p, tc.pathType, tc.req); got != tc.want {
			t.Errorf("pathMatches(%q,%q,%q) = %v, want %v", tc.p, tc.pathType, tc.req, got, tc.want)
		}
	}
}

func TestHandlerProxiesAndGates(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok host=%s path=%s", r.Host, r.URL.Path)
	}))
	defer backend.Close()
	d := &fakeDialer{target: backend.Listener.Addr().String()}

	h := Handler(d, "web", 8080, "/api", "Prefix", []string{"app.example.com"})

	// Matching host + path prefix -> proxied to the backend, dialed via PortForward.
	req := httptest.NewRequest("GET", "http://app.example.com/api/v1", nil)
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("matching request code = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if want := "host=app.example.com"; !strings.Contains(string(body), want) {
		t.Errorf("body %q missing %q (Host not preserved)", body, want)
	}
	if calls := d.got(); len(calls) != 1 || calls[0] != "web:8080/tcp" {
		t.Errorf("PortForward calls = %v, want [web:8080/tcp]", calls)
	}

	// Non-matching path -> 404, no dial.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://app.example.com/other", nil)
	req.Host = "app.example.com"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-matching path code = %d, want 404", rec.Code)
	}

	// Unrecognized Host -> 421, no dial.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://evil.example.com/api", nil)
	req.Host = "evil.example.com"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("bad-host code = %d, want 421", rec.Code)
	}
	if len(d.got()) != 1 {
		t.Errorf("expected exactly one dial across all requests, got %v", d.got())
	}
}

// failingDialer is a portfwd.Dialer whose PortForward always fails, modelling a
// workload that is down or not yet ready.
type failingDialer struct{ err error }

func (f failingDialer) PortForward(context.Context, string, int, string) (net.Conn, error) {
	return nil, f.err
}

// TestHandlerUpstreamUnreachableReturns502 proves the emulated ingress answers with
// an informative HTTP 502 — not an empty-body 502 that reads as a dropped
// connection — when it cannot reach the workload. The client is always HTTP-aware
// here (the proxy terminated HTTP/HTTPS), so it can and should say what went wrong.
func TestHandlerUpstreamUnreachableReturns502(t *testing.T) {
	h := Handler(failingDialer{err: errors.New("connection refused")}, "web", 8080, "/", "Prefix", []string{"app.example.com"})

	req := httptest.NewRequest("GET", "http://app.example.com/", nil)
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"502 Bad Gateway", `workload "web"`, "port 8080", "connection refused", "not be running or ready"} {
		if !strings.Contains(body, want) {
			t.Errorf("502 body missing %q; got:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
}

// recordingMux builds a Mux whose published listeners are captured in a map keyed by
// "host:port", so a test can drive requests over the shared :80 listener and observe
// registration/withdrawal.
type recordingMux struct {
	*Mux
	mu        sync.Mutex
	listeners map[string]*memlisten.Listener
}

func newRecordingMux() *recordingMux {
	rm := &recordingMux{listeners: map[string]*memlisten.Listener{}}
	rm.Mux = NewMux(
		func(host string, port int, lis *memlisten.Listener) {
			rm.mu.Lock()
			rm.listeners[fmt.Sprintf("%s:%d", host, port)] = lis
			rm.mu.Unlock()
		},
		func(host string, port int) {
			rm.mu.Lock()
			delete(rm.listeners, fmt.Sprintf("%s:%d", host, port))
			rm.mu.Unlock()
		},
	)
	return rm
}

func (rm *recordingMux) listener(t *testing.T, hostPort string) *memlisten.Listener {
	t.Helper()
	rm.mu.Lock()
	defer rm.mu.Unlock()
	lis := rm.listeners[hostPort]
	if lis == nil {
		t.Fatalf("no listener registered for %s (have %v)", hostPort, rm.listeners)
	}
	return lis
}

func (rm *recordingMux) registered(hostPort string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.listeners[hostPort] != nil
}

// getVia issues GET reqPath over lis (an addressless memlisten listener) with the
// given Host header and returns the status code and body.
func getVia(t *testing.T, lis *memlisten.Listener, host, reqPath string) (int, string) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return lis.DialLocal(ctx) },
	}}
	resp, err := hc.Get("http://" + host + reqPath)
	if err != nil {
		t.Fatalf("GET %s: %v", reqPath, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestMuxLongestMatchRouting proves two ingresses sharing a host resolve overlapping
// paths by longest match — like a real Kubernetes ingress — instead of the second
// registration shadowing the first. The root ("/") ingress is registered FIRST and
// the "/api" ingress SECOND, the order that made the old host:port-keyed router serve
// only whichever registered last.
func TestMuxLongestMatchRouting(t *testing.T) {
	rootBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "root")
	}))
	defer rootBackend.Close()
	apiBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "api")
	}))
	defer apiBackend.Close()

	rm := newRecordingMux()
	ports := []api.PortMapping{{Container: 8080}}

	_, cleanupRoot, err := rm.Add(Config{
		Dialer:   &fakeDialer{target: rootBackend.Listener.Addr().String()},
		Workload: "root",
		Spec:     &api.IngressSpec{Hosts: []string{"app.example.com"}, Path: "/"},
		Ports:    ports,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, cleanupAPI, err := rm.Add(Config{
		Dialer:   &fakeDialer{target: apiBackend.Listener.Addr().String()},
		Workload: "api",
		Spec:     &api.IngressSpec{Hosts: []string{"app.example.com"}, Path: "/api"},
		Ports:    ports,
	})
	if err != nil {
		t.Fatal(err)
	}

	lis := rm.listener(t, "app.example.com:80")
	cases := []struct {
		path, want string
	}{
		{"/api/v1", "api"}, // longest match wins over "/"
		{"/api", "api"},    // exact request to the longer prefix
		{"/", "root"},      // only "/" matches
		{"/other", "root"}, // "/api" is not a prefix of "/other"
		{"/apix", "root"},  // element-boundary: "/apix" is not under "/api"
	}
	for _, tc := range cases {
		code, body := getVia(t, lis, "app.example.com", tc.path)
		if code != 200 || body != tc.want {
			t.Errorf("GET %s = (%d, %q), want (200, %q)", tc.path, code, body, tc.want)
		}
	}

	// Withdraw the "/api" ingress: its path now falls through to the root backend, and
	// the shared listener stays published (the root rule keeps it alive).
	cleanupAPI()
	if !rm.registered("app.example.com:80") {
		t.Fatal("shared listener withdrawn while the root ingress still holds it")
	}
	if code, body := getVia(t, lis, "app.example.com", "/api/v1"); code != 200 || body != "root" {
		t.Errorf("after withdrawing /api, GET /api/v1 = (%d, %q), want (200, %q)", code, body, "root")
	}

	// Withdraw the last ingress: the shared listener is closed and unregistered.
	cleanupRoot()
	if rm.registered("app.example.com:80") {
		t.Error("shared listener still published after its last ingress was withdrawn")
	}
}

// TestMuxExactBeatsPrefixOnEqualLength proves an Exact rule wins over a Prefix rule of
// equal path length for an exact request, while longer prefixes still take other
// requests — the Kubernetes tie-break.
func TestMuxExactBeatsPrefixOnEqualLength(t *testing.T) {
	prefixBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "prefix")
	}))
	defer prefixBackend.Close()
	exactBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "exact")
	}))
	defer exactBackend.Close()

	rm := newRecordingMux()
	ports := []api.PortMapping{{Container: 8080}}

	if _, _, err := rm.Add(Config{
		Dialer:   &fakeDialer{target: prefixBackend.Listener.Addr().String()},
		Workload: "prefix",
		Spec:     &api.IngressSpec{Hosts: []string{"app.example.com"}, Path: "/api", PathType: "Prefix"},
		Ports:    ports,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rm.Add(Config{
		Dialer:   &fakeDialer{target: exactBackend.Listener.Addr().String()},
		Workload: "exact",
		Spec:     &api.IngressSpec{Hosts: []string{"app.example.com"}, Path: "/api", PathType: "Exact"},
		Ports:    ports,
	}); err != nil {
		t.Fatal(err)
	}

	lis := rm.listener(t, "app.example.com:80")
	if code, body := getVia(t, lis, "app.example.com", "/api"); code != 200 || body != "exact" {
		t.Errorf("GET /api = (%d, %q), want (200, %q) — Exact must beat Prefix at equal length", code, body, "exact")
	}
	if code, body := getVia(t, lis, "app.example.com", "/api/v1"); code != 200 || body != "prefix" {
		t.Errorf("GET /api/v1 = (%d, %q), want (200, %q) — only the Prefix rule matches", code, body, "prefix")
	}
}

func TestServeRegistersPortsAndTLS(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello")
	}))
	defer backend.Close()
	d := &fakeDialer{target: backend.Listener.Addr().String()}
	ca, err := generateCA()
	if err != nil {
		t.Fatal(err)
	}

	em, err := Serve(Config{
		Dialer:   d,
		Workload: "web",
		Spec:     &api.IngressSpec{Hosts: []string{"app.example.com"}, TLS: &api.IngressTLS{}},
		Ports:    []api.PortMapping{{Container: 8080}},
		CA:       ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	if _, ok := em.Listeners[80]; !ok {
		t.Error("expected a :80 listener")
	}
	if _, ok := em.Listeners[443]; !ok {
		t.Error("expected a :443 listener (TLS requested)")
	}

	// Drive a plain HTTP request over the :80 memlisten via its DialLocal.
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return em.Listeners[80].DialLocal(ctx)
		},
	}}
	resp, err := hc.Get("http://app.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "hello" {
		t.Errorf("body = %q, want hello", b)
	}
}

func TestServeTLSTerminationHandshake(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secure")
	}))
	defer backend.Close()
	d := &fakeDialer{target: backend.Listener.Addr().String()}
	ca, err := generateCA()
	if err != nil {
		t.Fatal(err)
	}
	em, err := Serve(Config{
		Dialer:   d,
		Workload: "web",
		Spec:     &api.IngressSpec{Hosts: []string{"app.example.com"}, TLS: &api.IngressTLS{}},
		Ports:    []api.PortMapping{{Container: 8080}},
		CA:       ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("append CA")
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return em.Listeners[443].DialLocal(ctx)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "app.example.com"},
	}}
	resp, err := hc.Get("https://app.example.com/")
	if err != nil {
		t.Fatalf("https over emulated ingress: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "secure" {
		t.Errorf("body = %q, want secure", b)
	}
}
