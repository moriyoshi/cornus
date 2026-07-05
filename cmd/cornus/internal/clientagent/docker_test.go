package clientagent

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
