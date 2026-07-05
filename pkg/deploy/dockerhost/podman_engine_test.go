package dockerhost

// Version-prefix discovery is pinned here because getting it wrong is SILENT.
//
// libpod does not reject a version it dislikes — it serves a different payload.
// Container inspect returns the v4 schema (Entrypoint as a joined string,
// StopSignal as an integer) for any prefix below 5.0.0, with HTTP 200 and no
// warning. A test that only checked "the request succeeded" would pass against a
// prefix that quietly corrupts every inspect in the backend.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cornus/pkg/runtimeendpoint"
)

// libpodPingServer stands up a server that answers the unversioned libpod ping
// with the given version header, and records the paths of every later request.
func libpodPingServer(t *testing.T, version string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			if version != "" {
				w.Header().Set(libpodVersionHeader, version)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		seen = append(seen, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func endpointFor(t *testing.T, srv *httptest.Server) runtimeendpoint.Endpoint {
	t.Helper()
	ep, err := runtimeendpoint.Parse("tcp://"+strings.TrimPrefix(srv.URL, "http://"), "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return ep
}

func TestPodmanEnginePinsPrefixToServerMajor(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"5.8.2", "/v5.0.0"},
		{"5.0.0", "/v5.0.0"},
		{"4.9.4", "/v4.0.0"},
		{"6.1.0-rc1", "/v6.0.0"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			srv, _ := libpodPingServer(t, tc.version)
			e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
			if err != nil {
				t.Fatalf("newPodmanEngine: %v", err)
			}
			if e.prefix != tc.want {
				t.Errorf("prefix for server %q = %q, want %q", tc.version, e.prefix, tc.want)
			}
		})
	}
}

// TestPodmanEngineVersionsEveryRoute is the one that matters operationally: a
// libpod path without the version segment is a 404, and the segment must be the
// DISCOVERED one rather than a constant.
func TestPodmanEngineVersionsEveryRoute(t *testing.T) {
	srv, _ := libpodPingServer(t, "4.9.4")
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	got := e.url("/containers/json")
	if !strings.Contains(got, "/v4.0.0/libpod/containers/json") {
		t.Errorf("url() = %q, want it to carry the discovered /v4.0.0 prefix and the /libpod segment", got)
	}
}

// TestPodmanEngineRejectsANonLibpodServer: a Docker daemon answers /_ping but
// has no libpod version header. Proceeding against it with a guessed prefix
// would produce a backend that talks to the wrong daemon in the wrong dialect.
func TestPodmanEngineRejectsANonLibpodServer(t *testing.T) {
	srv, _ := libpodPingServer(t, "") // 200, but no version header
	_, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err == nil {
		t.Fatal("newPodmanEngine accepted a server with no libpod version header; " +
			"want a refusal rather than a guessed prefix")
	}
	if !strings.Contains(err.Error(), libpodVersionHeader) {
		t.Errorf("error should name the missing header so the cause is diagnosable: %v", err)
	}
}

func TestPodmanEngineRejectsUnreachableEndpoint(t *testing.T) {
	ep, err := runtimeendpoint.Parse("unix:///nonexistent/podman.sock", "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := newPodmanEngine(context.Background(), ep); err == nil {
		t.Fatal("newPodmanEngine succeeded against a nonexistent socket")
	}
}

func TestMajorVersionRejectsGarbage(t *testing.T) {
	for _, v := range []string{"", "abc", "0.1.2", "-1.0"} {
		if _, err := majorVersion(v); err == nil {
			t.Errorf("majorVersion(%q) succeeded, want an error", v)
		}
	}
}
