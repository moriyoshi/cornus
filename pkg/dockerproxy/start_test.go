package dockerproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestConcurrentStartNoPanic is the regression guard for the check-then-act race
// in Proxy.start: two (or more) concurrent POST /containers/{id}/start for the
// same freshly-created container could both observe session()==nil and both call
// setRunning, whose second close(startedC) panicked ("close of closed channel")
// and crashed the whole proxy. The fix makes the install atomic, so exactly one
// start wins (204) and the losers get 304 without leaking their session.
func TestConcurrentStartNoPanic(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	const n = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all requests at once to maximize the race
			r := do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil)
			codes[i] = r.StatusCode
			r.Body.Close()
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, c := range codes {
		switch c {
		case http.StatusNoContent:
			won++
		case http.StatusNotModified:
		default:
			t.Fatalf("start[%d] status = %d, want 204 or 304", i, c)
		}
	}
	if won != 1 {
		t.Fatalf("exactly one start must win with 204, got %d winners", won)
	}

	// The server survived (no panicked goroutine): a follow-up request succeeds
	// and the container is running.
	resp = do(t, http.MethodGet, srv.URL+"/containers/web/json", nil)
	var cj containerJSON
	_ = json.NewDecoder(resp.Body).Decode(&cj)
	resp.Body.Close()
	if !cj.State.Running {
		t.Fatalf("container not running after concurrent start: %+v", cj.State)
	}
}

// TestSelfExitReconcilesState is the regression guard for a detached container
// (`docker run -d`) whose workload exits on its own with no client blocked in
// wait: sess.done closes but nothing observed it, so the record stayed
// "running" forever and its published-port listeners leaked. The fix spawns a
// watcher in start() that flips the record to exited and withdraws the port
// exposure when the session ends by itself.
func TestSelfExitReconcilesState(t *testing.T) {
	fa := &fakeAttacher{selfExit: make(chan struct{})}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", hostPort)

	b, _ := json.Marshal(createRequest{
		Image: "img",
		HostConfig: hostConfig{
			PortBindings: map[string][]portBinding{"80/tcp": {{HostPort: strconv.Itoa(hostPort)}}},
		},
	})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	// start (like docker run -d): no wait is ever issued.
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	// The published port listener is bound while running.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("port not published after start: %v", err)
	}
	c.Close()

	// The workload exits on its own.
	close(fa.selfExit)

	// The watcher must reconcile the record to exited and release the listener.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = do(t, http.MethodGet, srv.URL+"/containers/web/json", nil)
		var cj containerJSON
		_ = json.NewDecoder(resp.Body).Decode(&cj)
		resp.Body.Close()
		if !cj.State.Running && cj.State.Status == "exited" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container still running 5s after self-exit: %+v", cj.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn, err := net.Dial("tcp", addr); err == nil {
		conn.Close()
		t.Fatal("published-port listener still accepting after self-exit")
	}
}
