package clientagent

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentDockerServeAndStop(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := filepath.Join(t.TempDir(), "docker.sock")

	resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}})
	if !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}

	// The Docker API answers on the socket (a real http.Server bound by the agent).
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	waitFor(t, func() bool {
		r, err := client.Get("http://docker/_ping")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == http.StatusOK
	}, "docker _ping to answer")

	// A second docker-serve on the same socket is loud, not a silent OK that would
	// drop the new flags.
	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}}); resp.OK {
		t.Fatalf("second docker-serve on the same socket should error, got %+v", resp)
	}

	// The status inventory lists the socket.
	if inv := a.inventory(); len(inv.Dockers) != 1 || inv.Dockers[0] != sock {
		t.Fatalf("inventory dockers = %v, want [%s]", inv.Dockers, sock)
	}
	// A shared connState backs it.
	a.mu.Lock()
	nConns := len(a.conns)
	a.mu.Unlock()
	if nConns != 1 {
		t.Fatalf("conns = %d, want 1", nConns)
	}

	// docker-stop closes the frontend and releases the connState.
	if resp := a.doDockerStop(Request{Socket: sock}); !resp.OK {
		t.Fatalf("docker-stop = %+v", resp)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := client.Get("http://docker/_ping"); err != nil {
			break // socket gone
		}
		if time.Now().After(deadline) {
			t.Fatal("docker socket still answering after stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	nConns, nDockers := len(a.conns), len(a.dockers)
	a.mu.Unlock()
	if nConns != 0 || nDockers != 0 {
		t.Fatalf("after stop conns=%d dockers=%d, want 0,0", nConns, nDockers)
	}
}

// TestReapDockerReleasesRefs covers the crash-orphan fix: reaping a docker
// frontend (as the child's unexpected-exit path does) must release its shared
// conn/conduit refs, drop the map entry, and remove the socket.
func TestReapDockerReleasesRefs(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := filepath.Join(t.TempDir(), "docker.sock")
	if resp := a.doDockerServe(Request{Socket: sock, Conn: ConnSpec{Server: "http://fake:5000"}}); !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	a.mu.Lock()
	nConns := len(a.conns)
	a.mu.Unlock()
	if nConns != 1 {
		t.Fatalf("conns = %d, want 1", nConns)
	}

	a.reapDocker(sock) // simulate the http.Server unexpected-exit reap

	a.mu.Lock()
	nConns, nDockers := len(a.conns), len(a.dockers)
	a.mu.Unlock()
	if nConns != 0 || nDockers != 0 {
		t.Fatalf("after reap conns=%d dockers=%d, want 0,0", nConns, nDockers)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket not removed by reap")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestAgentDockerServeReturnsConduitBanner pins the plumbing the CLI print path
// depends on: `cornus daemon docker` is the ONLY place a session-local SOCKS5
// proxy's address is ever shown, because the agent binds it on a port the caller
// did not choose (Socks5Listen "127.0.0.1:0" here, as a profile's session-local
// conduit does).
//
// The response has always carried it; the CLI discarded it, so a `daemon docker`
// session had a working proxy nobody could address. This asserts the agent half
// so a regression there cannot make the restored print silently empty.
func TestAgentDockerServeReturnsConduitBanner(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	sock := filepath.Join(t.TempDir(), "docker.sock")

	resp := a.doDockerServe(Request{
		Socket:  sock,
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Conduit: ToWireConduit(socks5Conduit()),
	})
	if !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
	if len(resp.Banners) == 0 {
		t.Fatal("no conduit banner: the session-local proxy address would be unknowable")
	}
	joined := strings.Join(resp.Banners, "\n")
	if !strings.Contains(joined, "SOCKS5 proxy listening on") {
		t.Fatalf("banner = %q, want the SOCKS5 listener line", joined)
	}
	// The bound address, not the requested ":0" — that is the whole point.
	if strings.Contains(joined, "127.0.0.1:0") {
		t.Fatalf("banner = %q, want the RESOLVED port, not the :0 that was asked for", joined)
	}

	// No conduit configured: nothing to announce, and no empty line printed.
	sock2 := filepath.Join(t.TempDir(), "docker2.sock")
	if resp := a.doDockerServe(Request{Socket: sock2, Conn: ConnSpec{Server: "http://fake:5000"}}); !resp.OK {
		t.Fatalf("docker-serve without a conduit = %+v", resp)
	} else if len(resp.Banners) != 0 {
		t.Fatalf("banners = %v, want none when no conduit is configured", resp.Banners)
	}
}
