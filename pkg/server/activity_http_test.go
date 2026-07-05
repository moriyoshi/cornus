package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/activity"
)

// followServer stands the activity endpoint up over a real HTTP server, which
// this needs rather than httptest.NewRecorder: the recorder buffers, and every
// property under test here is about bytes reaching the client BEFORE the handler
// returns.
func followServer(t *testing.T, dataDir string) (*httptest.Server, *Server) {
	t.Helper()
	s := recoveringServer(t, dataDir)
	ts := httptest.NewServer(http.HandlerFunc(s.handleActivity))
	t.Cleanup(ts.Close)
	return ts, s
}

// sseFrame is one dispatched Server-Sent Event.
type sseFrame struct {
	event string
	data  string
}

// readFrames reads SSE frames off r into a channel, so a test can wait for the
// next one with a timeout instead of blocking forever on a stream that is
// supposed to stay open.
func readFrames(t *testing.T, r *bufio.Scanner) <-chan sseFrame {
	t.Helper()
	out := make(chan sseFrame, 16)
	go func() {
		defer close(out)
		var f sseFrame
		for r.Scan() {
			line := r.Text()
			switch {
			case line == "":
				if f.data != "" {
					out <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, "event:"):
				f.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				f.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return out
}

func nextFrame(t *testing.T, frames <-chan sseFrame, why string) sseFrame {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatalf("stream ended before %s", why)
		}
		return f
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", why)
		return sseFrame{}
	}
}

func decodeEvent(t *testing.T, f sseFrame) activity.Event {
	t.Helper()
	var e activity.Event
	if err := json.Unmarshal([]byte(f.data), &e); err != nil {
		t.Fatalf("decoding %q: %v", f.data, err)
	}
	return e
}

// The property that makes follow worth having: a record written AFTER the client
// connected reaches it without the handler returning. A stream that only
// delivered on completion would be a slow one-shot read.
func TestActivityFollowStreamsLiveRecords(t *testing.T) {
	dir := t.TempDir()
	ts, s := followServer(t, dir)

	// A record already on disk, so the backlog half is exercised too.
	s.activity.Begin(activity.KindServer, "", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?follow=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	// The live instance has to be on the RESPONSE headers, which arrive before any
	// record: a follower must be able to label the running incarnation immediately,
	// not only once something happens.
	if got := resp.Header.Get(LiveInstanceHeader); got != s.activity.Instance() {
		t.Errorf("%s = %q, want the serving instance %q", LiveInstanceHeader, got, s.activity.Instance())
	}

	frames := readFrames(t, bufio.NewScanner(resp.Body))
	if e := decodeEvent(t, nextFrame(t, frames, "the backlog record")); e.Kind != activity.KindServer {
		t.Fatalf("first frame = %v, want the backlog's lifetime record", e.Kind)
	}

	// Written while the response is open — this is the whole point.
	s.activity.Begin(activity.KindMount9P, "/mnt/live", map[string]string{"deployment": "web"})
	f := nextFrame(t, frames, "a record written while following")
	if f.event != ActivityEventName {
		t.Errorf("event name = %q, want %q", f.event, ActivityEventName)
	}
	e := decodeEvent(t, f)
	if e.Kind != activity.KindMount9P || e.Target != "/mnt/live" {
		t.Fatalf("live frame = %+v, want the mount just recorded", e)
	}
	if e.Attrs["deployment"] != "web" {
		t.Errorf("attrs lost in transit: %+v", e.Attrs)
	}
}

// Filters apply to the live stream the same way they apply to a one-shot read;
// otherwise `--follow --kind 9p-mount` would quietly become "everything".
func TestActivityFollowAppliesTheKindFilter(t *testing.T) {
	dir := t.TempDir()
	ts, s := followServer(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?follow=1&kind=9p-mount", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := readFrames(t, bufio.NewScanner(resp.Body))

	s.activity.Begin(activity.KindBuild, "demo:v1", nil) // filtered out
	s.activity.Begin(activity.KindMount9P, "/mnt/x", nil)

	e := decodeEvent(t, nextFrame(t, frames, "the mount record"))
	if e.Kind != activity.KindMount9P {
		t.Fatalf("first frame = %v, want the build filtered out and only the mount delivered", e.Kind)
	}
}

// "Unfinished" is a property of the whole stream, not of an event: a begin is
// unfinished only until its end arrives. Streaming it would emit records the
// next line makes false, with no way to retract them — so the combination is
// refused rather than served as a view that lies as it goes.
func TestActivityFollowRefusesUnfinished(t *testing.T) {
	ts, _ := followServer(t, t.TempDir())
	resp, err := http.Get(ts.URL + "?follow=1&unfinished=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "snapshot") {
		t.Errorf("the error must explain why the two are incompatible, got %q", body["error"])
	}
}

// Without follow the endpoint stays a plain NDJSON document — the same bytes as
// the records on disk. Follow is an addition, not a change to the read path.
func TestActivityWithoutFollowStaysNDJSON(t *testing.T) {
	dir := t.TempDir()
	ts, s := followServer(t, dir)
	s.activity.Begin(activity.KindDeploy, "web", nil).End(nil)

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", got)
	}
	var n int
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var e activity.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %q is not a bare record: %v", sc.Text(), err)
		}
		n++
	}
	if n != 2 {
		t.Errorf("got %d records, want the deploy's begin and end", n)
	}
}

// One record must be one data line. SSE splits a payload on newlines and a
// reader has to rejoin the pieces; keeping the JSON compact means that never
// arises, and this pins the property so a future switch to indented output
// cannot silently break every reader.
func TestWriteSSEEventIsOneDataLine(t *testing.T) {
	var b strings.Builder
	e := activity.Event{
		TS: "2026-07-26T10:00:00Z", Kind: activity.KindMount9P, Phase: activity.PhaseBegin,
		ID: "a1", Target: "/mnt/x", Attrs: map[string]string{"deployment": "web"},
	}
	if err := writeSSEEvent(&b, ActivityEventName, e); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("frame must end with a blank line to dispatch, got %q", got)
	}
	// Exactly two lines: the event name and one data line. Any additional line is
	// a newline inside the payload, which SSE would either require the reader to
	// rejoin or — since a continuation line carries no `data:` prefix — silently
	// drop, truncating the record.
	lines := strings.Split(strings.TrimSuffix(got, "\n\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("frame is %d lines, want event + one data line (a payload newline breaks every reader): %q", len(lines), got)
	}
	if lines[0] != "event: "+ActivityEventName {
		t.Errorf("frame must be named so readers can ignore future event kinds: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "data: ") {
		t.Errorf("payload line = %q, want a data field", lines[1])
	}
}
