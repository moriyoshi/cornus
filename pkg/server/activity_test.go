package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cornus/pkg/activity"
	"cornus/pkg/config"
)

// recoveringServer is a Server with just enough wired for the startup recovery
// pass: its own recorder and a data dir. Constructing the whole server would
// drag in storage and a backend for no benefit here.
func recoveringServer(t *testing.T, dataDir string) *Server {
	t.Helper()
	cfg := config.Config{DataDir: dataDir}
	rec, err := activity.Open(ActivityDir(cfg), "server")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return &Server{cfg: cfg, activity: rec}
}

// The incident this whole mechanism exists for: a process wrote a mount record,
// died before unmounting, and the next run has to find and undo it.
//
// The mountpoint here is an ordinary directory rather than a real 9P mount —
// unprivileged tests cannot mount — which exercises the "target already absent"
// path. That is the same path a crash between the write-ahead record and the
// mount syscall produces, and it must read as success rather than as a failure
// that keeps the record open forever.
func TestRecoverActivitiesClosesAStrandedMount(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}

	// A previous incarnation: began a lifetime and a mount, then vanished.
	prev, err := activity.Open(ActivityDir(cfg), "server")
	if err != nil {
		t.Fatal(err)
	}
	prev.Begin(activity.KindServer, "", nil)
	prev.Begin(activity.KindMount9P, filepath.Join(dir, "mounts", "sess-1", "m0"),
		map[string]string{"deployment": "web"})
	_ = prev.Close()

	s := recoveringServer(t, dir)
	s.recoverActivities(context.Background())

	left, err := activity.Unfinished(ActivityDir(cfg))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		if e.Instance != s.activity.Instance() {
			t.Errorf("previous run's %s activity still unfinished after recovery: %+v", e.Kind, e)
		}
	}

	// The incident must remain legible: recovered, not silently completed.
	events, _ := activity.Read(ActivityDir(cfg))
	var recovered int
	for _, e := range events {
		if e.Phase == activity.PhaseEnd && e.Status == activity.StatusRecovered {
			recovered++
		}
	}
	if recovered != 2 {
		t.Errorf("got %d recovered records, want the lifetime and the mount both marked recovered", recovered)
	}
}

// Recovery must not touch the live run's own records — the server begins its
// lifetime before the pass runs, and closing it would declare the running
// process crashed.
func TestRecoverActivitiesLeavesTheLiveRunAlone(t *testing.T) {
	dir := t.TempDir()
	s := recoveringServer(t, dir)

	life := s.activity.Begin(activity.KindServer, "", nil)
	defer life.End(nil)
	s.recoverActivities(context.Background())

	events, _ := activity.Read(ActivityDir(s.cfg))
	for _, e := range events {
		if e.Instance == s.activity.Instance() && e.Phase == activity.PhaseEnd {
			t.Fatalf("recovery closed the live run's own lifetime: %+v", e)
		}
	}
}

// An unfinished kind this build cannot undo must be reported and LEFT OPEN.
// Closing it would claim something was dealt with when nothing was.
func TestRecoverActivitiesLeavesUnknownKindsOpen(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}

	prev, err := activity.Open(ActivityDir(cfg), "server")
	if err != nil {
		t.Fatal(err)
	}
	prev.Begin(activity.Kind("some-future-effect"), "/somewhere", nil)
	_ = prev.Close()

	s := recoveringServer(t, dir)
	s.recoverActivities(context.Background())

	left, _ := activity.Unfinished(ActivityDir(cfg))
	var found bool
	for _, e := range left {
		if e.Kind == "some-future-effect" {
			found = true
		}
	}
	if !found {
		t.Error("an activity with no recovery for its kind must stay recorded, not be closed unhandled")
	}
}

// A missing activity directory is the normal first-boot case, not an error.
func TestRecoverActivitiesOnFirstBoot(t *testing.T) {
	s := recoveringServer(t, t.TempDir())
	s.recoverActivities(context.Background()) // must not panic or fail
}

// A server with no recorder (the log could not be opened) must still run.
func TestRecoverActivitiesWithoutARecorder(t *testing.T) {
	s := &Server{cfg: config.Config{DataDir: t.TempDir()}}
	s.recoverActivities(context.Background())
}

func TestActivityDirLivesUnderTheDataDir(t *testing.T) {
	// The data dir is the one thing deployments keep persistent (a StatefulSet
	// volume, a host bind), which is what lets records outlive the container.
	got := ActivityDir(config.Config{DataDir: "/var/lib/cornus"})
	if got != "/var/lib/cornus/activity" {
		t.Errorf("ActivityDir = %q", got)
	}
}

// The server's own records must land where a later run will look for them.
func TestServerRecordsAreReadableFromTheDataDir(t *testing.T) {
	dir := t.TempDir()
	s := recoveringServer(t, dir)
	s.activity.Begin(activity.KindDeploy, "web", nil).End(nil)

	b, err := os.ReadFile(filepath.Join(dir, "activity", "server.ndjson"))
	if err != nil {
		t.Fatalf("server stream not written under the data dir: %v", err)
	}
	if !strings.Contains(string(b), `"kind":"deploy"`) {
		t.Errorf("stream missing the deploy record: %s", b)
	}
}

// The unfinished filter must be resolved over the WHOLE stream before any
// narrowing. An activity's begin and end can straddle any window, so filtering
// first would report a completed activity as unfinished purely because its end
// fell outside the range — turning a healthy history into fake incidents.
func TestFilterActivityResolvesUnfinishedBeforeNarrowing(t *testing.T) {
	events := []activity.Event{
		{TS: "2026-07-26T10:00:00Z", Kind: activity.KindBuild, Phase: activity.PhaseBegin, ID: "a"},
		{TS: "2026-07-26T12:00:00Z", Kind: activity.KindBuild, Phase: activity.PhaseEnd, ID: "a"},
		{TS: "2026-07-26T10:30:00Z", Kind: activity.KindMount9P, Phase: activity.PhaseBegin, ID: "b"},
	}
	// A window that excludes the build's END but includes its begin.
	since, _ := time.Parse(time.RFC3339, "2026-07-26T09:00:00Z")
	got := filterActivity(events, since, "", true)
	for _, e := range got {
		if e.ID == "a" {
			t.Fatalf("a completed activity was reported unfinished because its end fell outside the window: %+v", got)
		}
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("filterActivity = %+v, want only the genuinely unfinished mount", got)
	}
}

func TestFilterActivityByKindAndSince(t *testing.T) {
	events := []activity.Event{
		{TS: "2026-07-26T10:00:00Z", Kind: activity.KindBuild, Phase: activity.PhaseBegin, ID: "a"},
		{TS: "2026-07-26T11:00:00Z", Kind: activity.KindDeploy, Phase: activity.PhaseBegin, ID: "b"},
		{TS: "2026-07-26T12:00:00Z", Kind: activity.KindDeploy, Phase: activity.PhaseBegin, ID: "c"},
	}
	since, _ := time.Parse(time.RFC3339, "2026-07-26T11:30:00Z")
	got := filterActivity(events, since, "deploy", false)
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("filterActivity = %+v, want only the later deploy", got)
	}
	// Kind matching is case-insensitive: an operator typing --kind Deploy should
	// not silently get nothing back.
	if got := filterActivity(events, time.Time{}, "DEPLOY", false); len(got) != 2 {
		t.Errorf("kind match should be case-insensitive, got %d", len(got))
	}
}
