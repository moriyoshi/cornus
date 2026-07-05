package runtimeendpoint

import (
	"net/http"
	"strings"
	"testing"
)

func TestParseUnix(t *testing.T) {
	ep, err := Parse("unix:///run/docker.sock", "unix:///var/run/docker.sock")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := ep.UnixSocketPath(), "/run/docker.sock"; got != want {
		t.Errorf("UnixSocketPath() = %q, want %q", got, want)
	}
	if got, want := ep.BaseURL(), "http://docker"; got != want {
		t.Errorf("BaseURL() = %q, want %q", got, want)
	}
	if got, want := ep.HostHeader(), "docker"; got != want {
		t.Errorf("HostHeader() = %q, want %q", got, want)
	}
	if ep.NonLocal() {
		t.Error("NonLocal() = true for a unix socket, want false")
	}
}

func TestParseTCP(t *testing.T) {
	ep, err := Parse("tcp://10.0.0.5:2375", "unix:///var/run/docker.sock")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := ep.BaseURL(), "http://10.0.0.5:2375"; got != want {
		t.Errorf("BaseURL() = %q, want %q", got, want)
	}
	if got, want := ep.HostHeader(), "10.0.0.5:2375"; got != want {
		t.Errorf("HostHeader() = %q, want %q", got, want)
	}
	if !ep.NonLocal() {
		t.Error("NonLocal() = false for a tcp endpoint, want true")
	}
	// A tcp endpoint has no socket FILE, so a caller that must bind-mount one
	// into a container has to be able to detect that rather than bind "".
	if got := ep.UnixSocketPath(); got != "" {
		t.Errorf("UnixSocketPath() = %q for a tcp endpoint, want \"\"", got)
	}
}

func TestParseEmptyUsesDefault(t *testing.T) {
	ep, err := Parse("", "unix:///var/run/docker.sock")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := ep.UnixSocketPath(), "/var/run/docker.sock"; got != want {
		t.Errorf("UnixSocketPath() = %q, want the default %q", got, want)
	}
}

// TestParseRejectsUnknownScheme pins fail-closed behaviour.
//
// The alternative — falling back to the default on an unrecognized value — is
// the dangerous one: a typo like "unix:/var/run/docker.sock" (one slash) would
// silently connect to whatever the default names, and nothing anywhere would
// report that the operator's setting had been ignored.
func TestParseRejectsUnknownScheme(t *testing.T) {
	for _, raw := range []string{
		"ssh://user@host/run/podman.sock", // not supported yet — must not silently default
		"unix:/var/run/docker.sock",       // one slash: a real and easy typo
		"http://localhost:2375",
		"/var/run/docker.sock", // bare path, no scheme
		"unix://",              // scheme with no path
		"tcp://",               // scheme with no address
	} {
		if _, err := Parse(raw, "unix:///var/run/docker.sock"); err == nil {
			t.Errorf("Parse(%q) succeeded; want an error rather than a silent fallback to the default", raw)
		}
	}
}

// TestTransportIsNilForTCP pins the contract that the three callers depend on.
//
// http.Client treats a nil Transport as http.DefaultTransport. A TCP endpoint
// already carries its address in the request URL and needs no custom dialer, so
// returning one would silently discard everything DefaultTransport brings —
// connection pooling limits, idle timeouts, HTTP/2, proxy handling. Returning
// nil is what lets every caller write `&http.Client{Transport: ep.Transport()}`
// without branching.
//
// The nil must be an UNTYPED nil interface: a (*http.Transport)(nil) stored in
// an http.RoundTripper is non-nil to `== nil` and would make http.Client call
// RoundTrip on a nil pointer instead of falling back.
func TestTransportIsNilForTCP(t *testing.T) {
	ep, err := Parse("tcp://127.0.0.1:2375", "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rt := ep.Transport()
	if rt != nil {
		t.Fatalf("Transport() = %#v for a tcp endpoint, want an untyped nil so http.Client uses DefaultTransport", rt)
	}
	// And the http.Client contract that relies on it.
	c := &http.Client{Transport: ep.Transport()}
	if c.Transport != nil {
		t.Errorf("http.Client.Transport = %#v, want nil", c.Transport)
	}
}

// TestTransportDialsTheSocketForUnix confirms the unix case gets a real custom
// dialer — the half that would break loudly, but is worth pinning beside the
// half that would break quietly.
func TestTransportDialsTheSocketForUnix(t *testing.T) {
	ep, err := Parse("unix:///nonexistent/docker.sock", "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rt := ep.Transport()
	if rt == nil {
		t.Fatal("Transport() = nil for a unix endpoint; DefaultTransport cannot reach a unix socket")
	}
	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("Transport() = %T, want *http.Transport", rt)
	}
	if tr.DialContext == nil {
		t.Fatal("unix Transport has no DialContext; it would dial the placeholder host over TCP")
	}
	// The dialer must target the socket path, not the placeholder authority.
	if _, err := tr.DialContext(t.Context(), "tcp", "docker:80"); err == nil {
		t.Error("dial of a nonexistent socket succeeded; the dialer is not using the endpoint address")
	} else if !strings.Contains(err.Error(), "/nonexistent/docker.sock") {
		t.Errorf("dial error = %v, want it to name the socket path (the dialer ignored the endpoint)", err)
	}
}
