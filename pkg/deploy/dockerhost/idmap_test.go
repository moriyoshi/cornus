package dockerhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cornus/pkg/deploy"
)

// TestPodmanIDMappingsReadTheRealShape uses the exact JSON the rootless daemon
// in the E2E runner returned from libpod /info. The field names are snake_case
// and the two-range structure is not obvious, so a measured fixture is what
// keeps the tags and the arithmetic honest.
func TestPodmanIDMappingsReadTheRealShape(t *testing.T) {
	const body = `{"host":{"security":{"rootless":true},"idMappings":{` +
		`"uidmap":[{"container_id":0,"host_id":1001,"size":1},{"container_id":1,"host_id":100000,"size":65536}],` +
		`"gidmap":[{"container_id":0,"host_id":1001,"size":1},{"container_id":1,"host_id":100000,"size":65536}]}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.4.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	m, err := e.idMappings(context.Background())
	if err != nil {
		t.Fatalf("idMappings: %v", err)
	}

	// Container root maps to the podman USER.
	if got, ok := m.HostUID(0); !ok || got != 1001 {
		t.Fatalf("HostUID(0) = %d,%v, want 1001,true", got, ok)
	}
	// And a NON-ROOT workload maps into the subuid range — not to the base,
	// which is container root and just as unreadable to it. This is the case the
	// whole facility exists for.
	if got, ok := m.HostUID(1000); !ok || got != 100999 {
		t.Fatalf("HostUID(1000) = %d,%v, want 100999,true", got, ok)
	}
	if got, ok := m.HostGID(1000); !ok || got != 100999 {
		t.Fatalf("HostGID(1000) = %d,%v, want 100999,true", got, ok)
	}
	// Past the allocation there is no answer, rather than a wrong one.
	if _, ok := m.HostUID(65537); ok {
		t.Fatal("a uid past the subuid allocation resolved anyway")
	}
}

// TestPodmanIDMappingsNullIsIdentity: a ROOTFUL daemon reports null here, which
// must mean "no remapping" and not "no answer" — otherwise rootful podman would
// lose credential files it has always been able to serve.
func TestPodmanIDMappingsNullIsIdentity(t *testing.T) {
	const body = `{"host":{"security":{"rootless":false},"idMappings":{"uidmap":null,"gidmap":null}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.4.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	m, err := e.idMappings(context.Background())
	if err != nil {
		t.Fatalf("idMappings: %v", err)
	}
	if got, ok := m.HostUID(1000); !ok || got != 1000 {
		t.Fatalf("HostUID(1000) = %d,%v on a rootful daemon, want the identity", got, ok)
	}
}

// TestDockerUsernsRemapIsRefusedNotGuessed. Docker advertises THAT it remaps but
// not the mapping, which lives in /etc/subuid for a user this process may not be
// able to read. Answering the identity would write files owned by ids the
// workload cannot see and report success; the refusal is the honest answer.
func TestDockerUsernsRemapIsRefusedNotGuessed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    []string
		wantErr bool
	}{
		{"remapped", []string{"name=seccomp,profile=builtin", "name=userns"}, true},
		{"plain", []string{"name=seccomp,profile=builtin"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"SecurityOptions": tc.opts})
			}))
			t.Cleanup(srv.Close)
			ep := endpointFor(t, srv)
			c := &engineClient{
				http:       &http.Client{Transport: ep.Transport()},
				host:       ep.BaseURL(),
				dial:       ep.Dial,
				hostHeader: ep.HostHeader(),
			}
			on, err := c.usernsRemapped(context.Background())
			if err != nil {
				t.Fatalf("usernsRemapped: %v", err)
			}
			if on != tc.wantErr {
				t.Fatalf("usernsRemapped = %v, want %v", on, tc.wantErr)
			}
		})
	}
}

var _ = deploy.IDMap{}
