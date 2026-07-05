package dockerproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
)

// This file covers the foreground `docker run` attach path: create -> attach ->
// start. Both defects it guards were live for months behind the fact that NO
// scenario drove a foreground attached run — every dockerd*.star uses `run -d` —
// so the only signal was a third-party CLI timing out after ten minutes.
//
// The asymmetry that causes both: real dockerd registers the attach BEFORE the
// container's process starts, so nothing can be missed and the stream EOFs when
// the process ends. Cornus deploys (which starts the container) and only then
// opens the tunnel, so it is always attaching to a container that is already
// running — or already gone.

// startAttachBeforeStart performs docker run's ordering: create the container,
// open the hijacked attach, and only then start it. It returns the container id
// and the raw attach connection, positioned just past the 101 handshake.
func startAttachBeforeStart(t *testing.T, srv *httptest.Server, name string) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	b, _ := json.Marshal(createRequest{Image: "img"})
	cresp, err := http.Post(srv.URL+"/containers/create?name="+name, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var cr createResponse
	_ = json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()

	conn := rawDial(t, srv.URL)
	req := "POST /containers/" + cr.ID + "/attach?stream=1&stdout=1&stderr=1 HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("attach handshake = %q, want 101", status)
	}
	drainHeaders(t, br)

	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	return cr.ID, conn, br
}

// TestForegroundAttachReplaysFromContainerStart is the regression test for lost
// output on `docker run`.
//
// The proxy cannot open the attach tunnel until the deploy has already STARTED
// the container, and dockerd's attach carries only what is written AFTER it is
// registered. Everything in that window — measured under 500ms, but the ENTIRE
// output of anything short-lived — was silently discarded. A plain
// `docker run alpine echo hi` printed nothing, and the devcontainer CLI, which
// waits to SEE a string on the attached stdout, waited ten minutes for one it had
// already missed.
//
// Asking the backend to replay from the container's first byte is what closes the
// window, so the request itself is the thing worth pinning.
func TestForegroundAttachReplaysFromContainerStart(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	_, conn, _ := startAttachBeforeStart(t, srv, "fg")
	defer conn.Close()

	waitUntil(t, 3*time.Second, "the proxy to open the attach tunnel", func() bool {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		return len(fa.attachCfgs) > 0
	})
	if cfg := fa.lastAttachCfg(t); !cfg.Logs {
		t.Fatal("the attach opened with Logs=false: output written between container start and " +
			"tunnel open is discarded, which for a short-lived workload is all of it")
	}
}

// TestAttachToRunningContainerDoesNotReplay is the other side of the same
// decision, and the reason the replay is conditional.
//
// `docker attach` on an already-established session missed nothing — that path
// worked all along. Replaying there would re-print the container's whole history
// to a caller that asked only for what happens next, so the fix must NOT be
// "always set Logs".
func TestAttachToRunningContainerDoesNotReplay(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	cresp, _ := http.Post(srv.URL+"/containers/create?name=established", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()
	// Start FIRST, so the attach below finds a live session and never parks.
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	conn := rawDial(t, srv.URL)
	defer conn.Close()
	req := "POST /containers/" + cr.ID + "/attach?stream=1&stdout=1&stderr=1 HTTP/1.1\r\n" +
		"Host: docker\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("attach handshake = %q, want 101", status)
	}
	drainHeaders(t, br)

	waitUntil(t, 3*time.Second, "the proxy to open the attach tunnel", func() bool {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		return len(fa.attachCfgs) > 0
	})
	if cfg := fa.lastAttachCfg(t); cfg.Logs {
		t.Fatal("an attach to an established session asked for a replay; it would re-print " +
			"history the caller never asked for")
	}
}

// TestForegroundAttachEndsWhenWorkloadExits is the regression test for the hang.
//
// dockerd does NOT EOF an attach opened against a container that has ALREADY
// exited — measured directly against the daemon: a stream opened while the
// container ran EOFs ~20ms after it stops, while one opened afterwards stays open
// indefinitely. Since the proxy always attaches after the deploy has started the
// container, a short-lived workload leaves the bridge blocked in its output copy
// forever: `docker run --rm alpine echo` produced no output AND never returned
// (measured at 281s, killable only with SIGKILL).
//
// The proxy therefore has to decide for itself that the stream is complete. What
// it must NOT do is close on the exit alone — a previous attempt did exactly that
// and truncated, turning a visible hang into a silent empty success. So this
// asserts BOTH halves: the output arrives, and only then does the stream end.
func TestForegroundAttachEndsWhenWorkloadExits(t *testing.T) {
	// Poll fast: awaitExit's one-second production interval would otherwise
	// dominate the test's runtime for no added coverage.
	old := exitPollInterval
	exitPollInterval = 20 * time.Millisecond
	defer func() { exitPollInterval = old }()

	fa := &fakeAttacher{output: "workload-output", attachNeverEOF: true}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	_, conn, br := startAttachBeforeStart(t, srv, "shortlived")
	defer conn.Close()

	// The workload terminates while its deploy-attach session is still held open
	// — which is exactly what the real server does, and why session.Done() is the
	// WRONG signal to end the stream on: it never fires here.
	code := 0
	fa.setInstances([]api.InstanceStatus{{ID: "1", State: "exited", Running: false, ExitCode: &code}})

	// The stream must deliver everything and THEN end. A read to EOF gets both
	// facts at once: the payload proves nothing was truncated, the EOF proves the
	// caller is not left hanging.
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("the attach stream never ended after the workload exited (%v) — `docker run` hangs", err)
	}
	if !strings.Contains(string(got), "workload-output") {
		t.Fatalf("attach delivered %q, want it to contain the workload's output — ending the "+
			"stream must not truncate it", got)
	}
}

// waitUntil polls cond until it holds or the budget runs out.
func waitUntil(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
