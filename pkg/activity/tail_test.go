package activity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appendRaw writes literal bytes to a stream file, for the cases a Recorder
// cannot produce on purpose: a half-written line, a rotation, a torn record.
func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func kinds(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, string(e.Kind)+"/"+string(e.Phase))
	}
	return out
}

// The first Next must hand back the whole existing stream. A follower that read
// the history through some other call and then started tailing would drop
// everything written in the gap — and the records most worth following are
// written exactly when the system is busiest.
func TestTailerFirstNextReturnsTheBacklog(t *testing.T) {
	dir := t.TempDir()
	rec, err := Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(KindServer, "", nil)
	rec.Begin(KindBuild, "demo:v1", nil).End(nil)

	got, err := NewTailer(dir).Next()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"server/begin", "build/begin", "build/end"}; strings.Join(kinds(got), ",") != strings.Join(want, ",") {
		t.Fatalf("first Next = %v, want the whole backlog %v", kinds(got), want)
	}
}

// The point of a tailer: a second Next returns only what is new. Re-reporting
// the backlog on every poll would make a followed stream unreadable.
func TestTailerNextReturnsOnlyNewRecords(t *testing.T) {
	dir := t.TempDir()
	rec, err := Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(KindServer, "", nil)

	tl := NewTailer(dir)
	if _, err := tl.Next(); err != nil {
		t.Fatal(err)
	}
	if got, _ := tl.Next(); len(got) != 0 {
		t.Fatalf("Next with nothing appended = %v, want empty", kinds(got))
	}

	rec.Begin(KindDeploy, "web", nil)
	got, err := tl.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != KindDeploy {
		t.Fatalf("Next after one append = %v, want just the deploy begin", kinds(got))
	}
}

// A caretaker starting mid-flight adds a NEW stream to the directory. Its whole
// file is new, so all of it must be picked up — the seam between server and
// caretaker is where the interesting failures live, and a follower that only
// watched the stream it started with would miss that half of the incident.
func TestTailerPicksUpAStreamThatAppearsLater(t *testing.T) {
	dir := t.TempDir()
	srv, err := Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.Begin(KindServer, "", nil)

	tl := NewTailer(dir)
	if _, err := tl.Next(); err != nil {
		t.Fatal(err)
	}

	care, err := Open(dir, "caretaker")
	if err != nil {
		t.Fatal(err)
	}
	defer care.Close()
	care.Begin(KindCaretaker, "", nil)

	got, err := tl.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != KindCaretaker {
		t.Fatalf("Next after a new stream appeared = %v, want the caretaker's lifetime", kinds(got))
	}
}

// A writer appends one line at a time, but a reader can land between the two
// halves of a write. A partial trailing line must be left alone and completed on
// the next round — not consumed, which would drop that record permanently once
// the offset moved past it.
func TestTailerWaitsForACompleteLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName("server"))
	appendRaw(t, path, `{"ts":"2026-07-26T10:00:00Z","kind":"deploy","phase":"begin","id":"a"}`+"\n")
	appendRaw(t, path, `{"ts":"2026-07-26T10:00:01Z","kind":"deploy","pha`) // torn mid-write

	tl := NewTailer(dir)
	got, err := tl.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("Next = %v, want only the complete record", kinds(got))
	}

	appendRaw(t, path, `se":"end","id":"a2"}`+"\n")
	got, err = tl.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a2" {
		t.Fatalf("Next after the line was completed = %+v, want the once-partial record delivered whole", got)
	}
}

// Rotation renames the live file aside and starts a new one. The retained
// generation holds records already delivered, so its appearance must not replay
// a whole generation of history into the middle of a live stream; the new live
// file, meanwhile, is a different file at the same path and must be read from
// its start rather than from the old offset.
func TestTailerAcrossARotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName("server"))
	appendRaw(t, path, `{"ts":"2026-07-26T10:00:00Z","kind":"build","phase":"begin","id":"old"}`+"\n")

	tl := NewTailer(dir)
	got, _ := tl.Next()
	if len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("backlog = %+v", got)
	}

	if err := os.Rename(path, rotatedPath(path)); err != nil {
		t.Fatal(err)
	}
	appendRaw(t, path, `{"ts":"2026-07-26T10:00:02Z","kind":"build","phase":"begin","id":"new"}`+"\n")

	got, err := tl.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "new" {
		var ids []string
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		t.Fatalf("Next across a rotation = %v, want only the new file's record (the retained generation is history already delivered)", ids)
	}
}

// A directory that does not exist yet is first boot, not a failure: a follower
// started before the server wrote anything must wait, not error out.
func TestTailerOnAMissingDirectory(t *testing.T) {
	tl := NewTailer(filepath.Join(t.TempDir(), "nope"))
	got, err := tl.Next()
	if err != nil {
		t.Fatalf("missing dir = %v, want it treated as empty", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d events from a missing dir", len(got))
	}
}

// Follow delivers the backlog and then live appends, and ends on cancellation
// rather than treating it as a failure.
func TestFollowDeliversBacklogThenLiveRecords(t *testing.T) {
	dir := t.TempDir()
	rec, err := Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(KindServer, "", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, dir, time.Millisecond, func(e Event) error {
			seen <- e
			return nil
		})
	}()

	first := <-seen
	if first.Kind != KindServer {
		t.Fatalf("first followed event = %v, want the backlog's lifetime record", first.Kind)
	}
	rec.Begin(KindMount9P, "/mnt/x", nil)
	select {
	case e := <-seen:
		if e.Kind != KindMount9P {
			t.Fatalf("live event = %v, want the mount", e.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Follow never delivered a record appended while it was running")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Follow returned %v on cancel, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Follow did not return after cancellation")
	}
}

// An error from the callback stops the follow: it is how a caller whose consumer
// went away (a hung-up HTTP client) gets out of the loop.
func TestFollowStopsOnCallbackError(t *testing.T) {
	dir := t.TempDir()
	rec, err := Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(KindServer, "", nil)

	boom := errors.New("client hung up")
	got := Follow(context.Background(), dir, time.Millisecond, func(Event) error { return boom })
	if !errors.Is(got, boom) {
		t.Fatalf("Follow = %v, want the callback's error", got)
	}
}

func TestParseSince(t *testing.T) {
	if ts, err := ParseSince(""); err != nil || !ts.IsZero() {
		t.Errorf("empty = %v, %v; want the zero time and no error", ts, err)
	}
	ts, err := ParseSince("2h")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(ts); d < 90*time.Minute || d > 150*time.Minute {
		t.Errorf("ParseSince(2h) is %v ago, want about two hours", d)
	}
	if ts, err := ParseSince("2026-07-26T10:00:00Z"); err != nil || ts.Year() != 2026 {
		t.Errorf("RFC3339 = %v, %v", ts, err)
	}
	if _, err := ParseSince("yesterday"); err == nil {
		t.Error("an unparseable --since must be rejected, not silently ignored")
	}
}
