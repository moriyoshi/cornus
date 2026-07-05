package dockerproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestEventsEmitsStart mirrors the devcontainer CLI's up flow: it subscribes to
// /events with an event=start filter BEFORE running the container, and must
// receive a start event whose Actor.Attributes carry the container's labels
// (that is how the CLI recognizes the container it launched).
func TestEventsEmitsStart(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/events?filters="+url.QueryEscape(`{"event":{"start":true}}`), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	b, _ := json.Marshal(createRequest{
		Image:  "img",
		Labels: map[string]string{"devcontainer.local_folder": "/ws"},
	})
	cresp, _ := http.Post(srv.URL+"/containers/create?name=dc", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	// A die event must NOT pass the event=start filter.
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/stop", nil).Body.Close()

	got := make(chan eventMessage, 1)
	go func() {
		var m eventMessage
		if err := json.NewDecoder(resp.Body).Decode(&m); err == nil {
			got <- m
		}
	}()
	select {
	case m := <-got:
		if m.Action != "start" || m.Status != "start" || m.Type != "container" {
			t.Fatalf("event = %+v, want container start", m)
		}
		if m.ID != cr.ID || m.Actor.Attributes["devcontainer.local_folder"] != "/ws" {
			t.Fatalf("event actor = %+v, want id %s with labels", m, cr.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no start event within 5s")
	}
}

// TestWaitNextExit covers docker run's foreground sequence: wait with
// condition=next-exit is issued BEFORE start and must not answer until the
// container has started AND exited. The pre-fix behavior (answering
// {"StatusCode":0} immediately for a not-yet-started container) made docker
// run report exit the moment start returned.
func TestWaitNextExit(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	cresp, _ := http.Post(srv.URL+"/containers/create?name=w", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()

	waitDone := make(chan int, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/containers/"+cr.ID+"/wait?condition=next-exit", "application/json", nil)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		var out struct{ StatusCode int }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		waitDone <- out.StatusCode
	}()

	select {
	case <-waitDone:
		t.Fatal("wait?condition=next-exit answered before the container started")
	case <-time.After(150 * time.Millisecond):
	}

	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	select {
	case <-waitDone:
		t.Fatal("wait?condition=next-exit answered while the container was running")
	case <-time.After(150 * time.Millisecond):
	}

	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/stop", nil).Body.Close()
	select {
	case code := <-waitDone:
		if code != 0 {
			t.Fatalf("wait status code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait?condition=next-exit did not answer after stop")
	}

	// The default condition (not-running) still answers immediately for a
	// container that is not running.
	resp := do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/wait", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default wait status = %d", resp.StatusCode)
	}
}

// TestAttachBeforeStart covers the other half of docker run's foreground
// sequence: the attach arrives BEFORE start. The proxy must hold the hijacked
// connection open and bridge it once the deploy-attach session is live (real
// dockerd accepts an attach to a created container), instead of closing it.
func TestAttachBeforeStart(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	cresp, _ := http.Post(srv.URL+"/containers/create?name=pre", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()

	conn := rawDial(t, srv.URL)
	defer conn.Close()
	req := "POST /containers/" + cr.ID + "/attach?stream=1&stdin=1&stdout=1&stderr=1 HTTP/1.1\r\n" +
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

	// Only now start the container; the parked attach must come alive.
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	if _, err := io.WriteString(conn, "pre-start-attach"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("pre-start-attach"))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "pre-start-attach" {
		t.Fatalf("attach echo = %q", got)
	}
}
