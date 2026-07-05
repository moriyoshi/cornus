package ingressemu

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
