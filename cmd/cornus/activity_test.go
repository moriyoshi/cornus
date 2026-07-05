package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/activity"
)

// --unfinished asks a question about the whole stream: a begin is unfinished
// only until its end arrives. As a feed it would print records that the next
// line makes false, with nothing printed to retract them. Refuse locally rather
// than round-trip to a server that will refuse anyway.
func TestActivityFollowRejectsUnfinished(t *testing.T) {
	c := &ActivityCmd{Follow: true, Unfinished: true, Local: true}
	err := c.Run(&CLI{}, cliout.New(cliout.Options{Output: "plain"}))
	if err == nil {
		t.Fatal("--follow --unfinished must be refused, not silently reinterpreted")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("the error must say why the two cannot combine, got %q", err)
	}
}

// The live stream cannot use the grouped view: an incarnation's verdict is only
// known once it has ended, which live is exactly what has not happened. So every
// line has to name its own writer, or a stream carrying a server and two
// caretakers is unreadable.
func TestActivityStreamEventNamesItsWriter(t *testing.T) {
	e := activity.Event{
		TS: "2026-07-26T10:00:00Z", Proc: "caretaker", Instance: "6c8ba5e0d63f",
		Kind: activity.KindMount9P, Phase: activity.PhaseBegin, ID: "a1",
		Target: "/var/lib/cornus/mounts/sess-1/m0", Attrs: map[string]string{"deployment": "web"},
	}
	var buf bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: &buf, Stderr: &buf, Output: "plain"})
	if err := d.Emit(activityStreamEvent{Event: e}); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	for _, want := range []string{"2026-07-26T10:00:00Z", "caretaker", "6c8ba5e0d63f", "9p-mount", "begin", "/var/lib/cornus/mounts/sess-1/m0", "deployment=web"} {
		if !strings.Contains(line, want) {
			t.Errorf("streamed line %q is missing %q", strings.TrimSpace(line), want)
		}
	}
}

// In json mode a followed stream must be NDJSON of the SAME objects the one-shot
// read returns — not a wrapper — so anything already parsing `cornus --output
// json activity` keeps working when --follow is added.
func TestActivityStreamEventJSONIsTheBareRecord(t *testing.T) {
	e := activity.Event{TS: "2026-07-26T10:00:00Z", Proc: "server", Kind: activity.KindBuild, Phase: activity.PhaseBegin, ID: "a1"}
	var buf bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: &buf, Stderr: &buf, Output: "json"})
	if err := d.Emit(activityStreamEvent{Event: e}); err != nil {
		t.Fatal(err)
	}
	var got activity.Event
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json mode emitted %q, which is not a bare record: %v", buf.String(), err)
	}
	if got.ID != "a1" || got.Kind != activity.KindBuild {
		t.Errorf("round-tripped record = %+v", got)
	}
}

// The grouped (non-follow) rendering leads with the timestamp; splitting the
// formatter in two for the stream must not have changed it.
func TestDescribeEventStillLeadsWithTheTimestamp(t *testing.T) {
	e := activity.Event{TS: "2026-07-26T10:00:00Z", Kind: activity.KindDeploy, Phase: activity.PhaseEnd, Status: "ok"}
	got := describeEvent(e)
	if !strings.HasPrefix(got, "2026-07-26T10:00:00Z ") {
		t.Fatalf("describeEvent = %q, want it to lead with the timestamp", got)
	}
	if !strings.Contains(got, "deploy") || !strings.Contains(got, "[ok]") {
		t.Errorf("describeEvent = %q, want the kind and outcome", got)
	}
}

// --local --follow reads straight off disk with no server: the mode for a host
// where nothing is running and nothing is coming back. It must deliver the
// backlog and then live appends, and exit 0 when interrupted.
func TestActivityFollowLocalStreamsFromDisk(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(filepath.Join(dir, "activity"), "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(activity.KindServer, "", nil)

	var buf lockedBuffer
	cli := &CLI{DataDir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	cli.baseCtx = ctx
	d := cliout.New(cliout.Options{Stdout: &buf, Stderr: &buf, Output: "plain"})

	done := make(chan error, 1)
	go func() { done <- (&ActivityCmd{Follow: true, Local: true}).Run(cli, d) }()

	waitFor(t, &buf, "server", "the record already on disk")
	rec.Begin(activity.KindMount9P, "/mnt/live", nil)
	waitFor(t, &buf, "9p-mount", "a record written while following")

	cancel()
	select {
	case err := <-done:
		// Interrupting is how this command normally ends; reporting failure would
		// make it unusable in a script and alarming by hand.
		if err != nil {
			t.Fatalf("--follow returned %v on interrupt, want a clean exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--follow did not return after its context was cancelled")
	}
}

func waitFor(t *testing.T, buf *lockedBuffer, want, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (%q); output so far: %q", why, want, buf.String())
}

// lockedBuffer is a bytes.Buffer a test goroutine can read while the command
// under test writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
