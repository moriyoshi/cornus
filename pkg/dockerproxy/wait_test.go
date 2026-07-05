package dockerproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/api"
)

// fastExitPoll shrinks the wait poll interval for the duration of a test.
func fastExitPoll(t *testing.T) {
	t.Helper()
	prev := exitPollInterval
	exitPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { exitPollInterval = prev })
}

// newWaitProxy builds a proxy + test server, returning a teardown that stops
// every live session BEFORE closing the server: a blocked wait handler would
// otherwise keep httptest.Server.Close waiting forever.
func newWaitProxy(t *testing.T, fa *fakeAttacher) (*httptest.Server, func()) {
	t.Helper()
	p := New(fa)
	srv := httptest.NewServer(p.Handler())
	return srv, func() {
		p.Close()
		srv.Close()
	}
}

// startContainer creates and starts a container against the proxy, returning its
// id.
func startContainer(t *testing.T, srv *httptest.Server, name string) string {
	t.Helper()
	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, err := http.Post(srv.URL+"/containers/create?name="+name, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	return cr.ID
}

// waitFor issues POST /containers/{id}/wait and decodes the docker wait body.
func waitFor(t *testing.T, srv *httptest.Server, id, query string) waitResponse {
	t.Helper()
	resp, err := http.Post(srv.URL+"/containers/"+id+"/wait"+query, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out waitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode wait body: %v", err)
	}
	return out
}

func exited(code int) []api.InstanceStatus {
	return []api.InstanceStatus{{ID: "i0", State: "exited", ExitCode: &code}}
}

// TestWaitReportsNonZeroExit is the regression guard for the bug this replaced:
// `docker wait` answered {"StatusCode":0} unconditionally, so a workload that
// CRASHED was reported to CI and to wait-for-completion loops as a success.
//
// The workload terminates while its deploy-attach session is still held (the
// server keeps the session until the caller disconnects, so session.Done() never
// fires on a self-exit) — the exit code has to come from the backend status.
func TestWaitReportsNonZeroExit(t *testing.T) {
	fastExitPoll(t)
	fa := &fakeAttacher{}
	srv, done := newWaitProxy(t, fa)
	defer done()

	id := startContainer(t, srv, "crasher")
	fa.setInstances(exited(3)) // the workload dies

	got := waitFor(t, srv, id, "?condition=next-exit")
	if got.StatusCode != 3 {
		t.Fatalf("wait StatusCode = %d, want 3", got.StatusCode)
	}
	if got.Error != nil {
		t.Fatalf("wait Error = %+v, want none for a known exit code", got.Error)
	}

	// The resolved code is remembered, so a follow-up inspect agrees rather than
	// reporting the same 0 the wait used to.
	resp := do(t, http.MethodGet, srv.URL+"/containers/"+id+"/json", nil)
	defer resp.Body.Close()
	var insp containerJSON
	if err := json.NewDecoder(resp.Body).Decode(&insp); err != nil {
		t.Fatal(err)
	}
	if insp.State.ExitCode != 3 {
		t.Fatalf("inspect State.ExitCode = %d, want 3", insp.State.ExitCode)
	}
}

// TestWaitReportsZeroExit: a genuinely successful workload still reports 0, with
// no Error — the fix must not turn every wait into a failure.
func TestWaitReportsZeroExit(t *testing.T) {
	fastExitPoll(t)
	fa := &fakeAttacher{}
	srv, done := newWaitProxy(t, fa)
	defer done()

	id := startContainer(t, srv, "oneshot")
	fa.setInstances(exited(0))

	got := waitFor(t, srv, id, "?condition=next-exit")
	if got.StatusCode != 0 || got.Error != nil {
		t.Fatalf("wait = %+v, want StatusCode 0 with no Error", got)
	}
}

// TestWaitUnknownExitIsNotZero covers a backend that cannot report an exit code
// at all (barehost/incushost never fill InstanceStatus.ExitCode) combined with a
// teardown while the workload was still running: nobody can say how it ended.
//
// The answer must NOT be 0. It is Docker's own "the daemon cannot tell you"
// encoding — an Error member, which the docker CLI turns into exit status 125 —
// plus a non-zero StatusCode for anything reading the raw API.
func TestWaitUnknownExitIsNotZero(t *testing.T) {
	fastExitPoll(t)
	fa := &fakeAttacher{} // Status reports no instances at all
	srv, done := newWaitProxy(t, fa)
	defer done()

	id := startContainer(t, srv, "opaque")

	waited := make(chan waitResponse, 1)
	go func() { waited <- waitFor(t, srv, id, "?condition=next-exit") }()

	// Nothing has terminated, so the wait must still be blocked.
	select {
	case got := <-waited:
		t.Fatalf("wait answered %+v while the container was running", got)
	case <-time.After(100 * time.Millisecond):
	}

	do(t, http.MethodPost, srv.URL+"/containers/"+id+"/stop", nil).Body.Close()

	select {
	case got := <-waited:
		if got.StatusCode == 0 {
			t.Fatal("wait reported StatusCode 0 for an unknown exit status — the exact bug this guards")
		}
		if got.StatusCode != unknownExitCode {
			t.Fatalf("wait StatusCode = %d, want %d", got.StatusCode, unknownExitCode)
		}
		if got.Error == nil || got.Error.Message == "" {
			t.Fatalf("wait = %+v, want an Error explaining the unknown exit status", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not answer after stop")
	}
}

// TestWaitExitCodeFromTerminalEvent covers the source that survives teardown:
// once the server has deleted the workload its status is gone for good, so the
// code travels on the terminal deploy-attach event (deploywire.Event.ExitCode),
// captured by the session's event hook. Here the backend status stays empty, so
// the wire value is the only thing that can answer.
func TestWaitExitCodeFromTerminalEvent(t *testing.T) {
	fastExitPoll(t)
	code := 7
	fa := &fakeAttacher{doneExitCode: &code}
	srv, done := newWaitProxy(t, fa)
	defer done()

	id := startContainer(t, srv, "reported")

	waited := make(chan waitResponse, 1)
	go func() { waited <- waitFor(t, srv, id, "?condition=next-exit") }()

	// Let the wait reach the handler before stopping: a wait that arrives AFTER
	// the session is gone parks on the next start instead (condition=next-exit),
	// which would be testing something else entirely.
	select {
	case got := <-waited:
		t.Fatalf("wait answered %+v while the container was running", got)
	case <-time.After(100 * time.Millisecond):
	}
	do(t, http.MethodPost, srv.URL+"/containers/"+id+"/stop", nil).Body.Close()

	select {
	case got := <-waited:
		if got.StatusCode != 7 || got.Error != nil {
			t.Fatalf("wait = %+v, want StatusCode 7 with no Error", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not answer after stop")
	}
}

// TestWaitAfterExitRepeats: the default condition (not-running) on an already
// finished container answers immediately, from the code established earlier —
// not by re-blocking and not by falling back to 0.
func TestWaitAfterExitRepeats(t *testing.T) {
	fastExitPoll(t)
	fa := &fakeAttacher{}
	srv, done := newWaitProxy(t, fa)
	defer done()

	id := startContainer(t, srv, "twice")
	fa.setInstances(exited(4))

	if got := waitFor(t, srv, id, "?condition=next-exit"); got.StatusCode != 4 {
		t.Fatalf("first wait = %+v, want StatusCode 4", got)
	}
	// Stop the session and empty the backend status, as a real teardown would.
	do(t, http.MethodPost, srv.URL+"/containers/"+id+"/stop", nil).Body.Close()
	fa.setInstances(nil)

	if got := waitFor(t, srv, id, ""); got.StatusCode != 4 || got.Error != nil {
		t.Fatalf("second wait = %+v, want the remembered StatusCode 4", got)
	}
}
