package activity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustOpen(t *testing.T, dir, proc string) *Recorder {
	t.Helper()
	r, err := Open(dir, proc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")

	a := r.Begin(KindBuild, "demo:v1", map[string]string{"by": "alice"})
	a.End(nil)

	events, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want a begin and an end: %+v", len(events), events)
	}
	begin, end := events[0], events[1]
	if begin.Phase != PhaseBegin || end.Phase != PhaseEnd {
		t.Errorf("phases = %q,%q", begin.Phase, end.Phase)
	}
	if begin.ID != end.ID {
		t.Errorf("begin/end ids differ: %q vs %q — they must pair", begin.ID, end.ID)
	}
	if begin.Target != "demo:v1" || begin.Attrs["by"] != "alice" {
		t.Errorf("begin lost its detail: %+v", begin)
	}
	if end.Status != StatusOK {
		t.Errorf("status = %q, want %q", end.Status, StatusOK)
	}
	// Every record must name its writer and incarnation, or a stream cannot be
	// grouped into runs.
	for _, e := range events {
		if e.Proc != "server" || e.Instance == "" || e.PID == 0 {
			t.Errorf("record missing provenance: %+v", e)
		}
	}
}

func TestEndCarriesTheError(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")
	r.Begin(KindBuild, "demo:v1", nil).End(errors.New("pull denied"))

	events, _ := Read(dir)
	end := events[len(events)-1]
	if end.Status != StatusError || !strings.Contains(end.Err, "pull denied") {
		t.Errorf("end = %+v, want the failure recorded", end)
	}
}

// The recorder's primary question: what began and never finished.
func TestUnfinished(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")

	done := r.Begin(KindMount9P, "/mnt/done", nil)
	open := r.Begin(KindMount9P, "/mnt/open", nil)
	done.End(nil)
	_ = open

	got, err := Unfinished(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Target != "/mnt/open" {
		t.Fatalf("Unfinished = %+v, want just /mnt/open", got)
	}
}

// The case the whole package exists for: the process dies between the begin and
// the end, and the next one has to be able to tell.
func TestCrashLeavesTheActivityOpen(t *testing.T) {
	dir := t.TempDir()

	// An incarnation that mounts something and is killed — no End, no Close.
	r1 := mustOpen(t, dir, "server")
	life := r1.Begin(KindServer, "", nil)
	r1.Begin(KindMount9P, "/mnt/stranded", map[string]string{"deployment": "web"})
	_ = life

	// The next incarnation reopens the same directory.
	got, err := Unfinished(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sawLifetime, sawMount bool
	for _, e := range got {
		switch e.Kind {
		case KindServer:
			sawLifetime = true
		case KindMount9P:
			sawMount = true
			if e.Target != "/mnt/stranded" || e.Attrs["deployment"] != "web" {
				t.Errorf("stranded mount lost its detail: %+v", e)
			}
		}
	}
	if !sawLifetime {
		t.Error("an unclean exit must be visible as an unfinished lifetime")
	}
	if !sawMount {
		t.Error("the stranded mount must be recoverable from the record")
	}
}

// A mount left open by a process that DID shut down cleanly is a broken unwind
// path, not an ordinary crash strand — and the two must be distinguishable, or
// a real bug gets silently cleaned up every restart.
func TestCleanExitDistinguishesABrokenUnwind(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")
	life := r.Begin(KindServer, "", nil)
	r.Begin(KindMount9P, "/mnt/leaked", nil) // never ended
	life.End(nil)                            // but the process exited cleanly

	events, _ := Read(dir)
	known, clean := CleanExit(events, r.Instance())
	if !known || !clean {
		t.Fatalf("CleanExit = (%v, %v), want a known clean exit", known, clean)
	}
	open := unfinishedFrom(events)
	if len(open) != 1 || open[0].Kind != KindMount9P {
		t.Fatalf("unfinished = %+v, want the leaked mount", open)
	}
}

func TestCleanExitUnknownInstance(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")
	r.Begin(KindBuild, "x", nil).End(nil) // no lifetime record at all

	events, _ := Read(dir)
	if known, _ := CleanExit(events, r.Instance()); known {
		t.Error("CleanExit claimed to know about an instance with no lifetime record")
	}
}

// An activity that began before a rotation and never ended must still be
// reported — otherwise it disappears exactly when the log is busiest.
func TestUnfinishedSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(MaxBytesEnv, "1") // force the next Open to rotate

	r1 := mustOpen(t, dir, "server")
	r1.Begin(KindMount9P, "/mnt/before-rotation", nil)
	_ = r1.Close()

	// Reopening rotates the oversized file to .ndjson.1 and starts a fresh one.
	r2 := mustOpen(t, dir, "server")
	r2.Begin(KindBuild, "after", nil).End(nil)

	if _, err := os.Stat(filepath.Join(dir, "server.ndjson.1")); err != nil {
		t.Fatalf("expected a retained generation: %v", err)
	}
	got, err := Unfinished(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Target != "/mnt/before-rotation" {
		t.Fatalf("Unfinished = %+v, want the pre-rotation mount", got)
	}
}

// Two processes share a directory on the host backends, and their streams must
// stay separable and both readable.
func TestSeparateStreamsPerProcess(t *testing.T) {
	dir := t.TempDir()
	srv := mustOpen(t, dir, "server")
	care := mustOpen(t, dir, "caretaker")
	srv.Begin(KindDeploy, "web", nil).End(nil)
	care.Begin(KindMount9P, "/cornus/mounts/0", nil).End(nil)

	if _, err := os.Stat(filepath.Join(dir, "caretaker.ndjson")); err != nil {
		t.Fatalf("caretaker stream missing: %v", err)
	}
	events, _ := Read(dir)
	procs := map[string]bool{}
	for _, e := range events {
		procs[e.Proc] = true
	}
	if !procs["server"] || !procs["caretaker"] {
		t.Errorf("Read must merge both streams, saw %v", procs)
	}
}

// A crash mid-write leaves a torn final line. The rest of the flight is the
// valuable part and must still parse.
func TestTornLineDoesNotHideTheRest(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")
	r.Begin(KindMount9P, "/mnt/good", nil)
	_ = r.Close()

	path := filepath.Join(dir, "server.ndjson")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"ts":"2026-07-26T00:00:00Z","kind":"9p-mo`)
	_ = f.Close()

	got, err := Unfinished(dir)
	if err != nil {
		t.Fatalf("a torn line must not fail the read: %v", err)
	}
	if len(got) != 1 || got[0].Target != "/mnt/good" {
		t.Fatalf("Unfinished = %+v, want the intact record", got)
	}
}

// Recording is instrumentation: a caller with none configured must not have to
// branch, and must never crash.
func TestNilRecorderIsANoOp(t *testing.T) {
	var r *Recorder
	a := r.Begin(KindBuild, "x", nil)
	a.End(errors.New("boom"))
	a.End(nil) // idempotent
	if r.Dir() != "" || r.Instance() != "" || r.Err() != nil {
		t.Error("nil recorder should report nothing")
	}
	if err := r.Close(); err != nil {
		t.Errorf("nil Close = %v", err)
	}
	if a.ID() != "" {
		t.Error("nil activity should have no id")
	}
}

func TestReadMissingDirIsEmpty(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Errorf("Read(missing) = (%v, %v), want (nil, nil)", got, err)
	}
}

// The line format is the machine interface; keep it one JSON object per line.
func TestOneJSONObjectPerLine(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")
	r.Begin(KindDeploy, "web", map[string]string{"replicas": "2"}).End(nil)
	_ = r.Close()

	b, err := os.ReadFile(filepath.Join(dir, "server.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for i, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Errorf("line %d is not one JSON object: %v", i, err)
		}
	}
}

func TestWriteAheadKinds(t *testing.T) {
	// Only effects that can outlive the process need the durability guarantee.
	if !KindMount9P.writeAhead() {
		t.Error("9p-mount must be write-ahead: its effect outlives the process")
	}
	for _, k := range []Kind{KindBuild, KindDeploy, KindServer, KindCaretaker} {
		if k.writeAhead() {
			t.Errorf("%s should not pay for an fsync; it strands nothing", k)
		}
	}
}

// Recovery must converge: an activity a later incarnation dealt with stops being
// unfinished, or every restart re-reports every historical crash forever. But it
// must NOT read as a clean completion, or the incident is erased.
func TestResolveClosesAForeignActivity(t *testing.T) {
	dir := t.TempDir()

	r1 := mustOpen(t, dir, "server")
	r1.Begin(KindMount9P, "/mnt/stranded", nil)
	_ = r1.Close()

	open, _ := Unfinished(dir)
	if len(open) != 1 {
		t.Fatalf("setup: unfinished = %+v", open)
	}

	r2 := mustOpen(t, dir, "server")
	r2.Resolve(open[0], StatusRecovered, "unmounted by startup recovery")

	if left, _ := Unfinished(dir); len(left) != 0 {
		t.Errorf("still unfinished after Resolve: %+v", left)
	}
	events, _ := Read(dir)
	end := events[len(events)-1]
	if end.Status != StatusRecovered {
		t.Errorf("status = %q, want %q — a recovered activity did not complete normally", end.Status, StatusRecovered)
	}
	if end.ID != open[0].ID {
		t.Errorf("Resolve must keep the original id so the pair matches: %q vs %q", end.ID, open[0].ID)
	}
	// Provenance of both sides is what makes the record readable afterwards.
	if end.Instance != r2.Instance() || end.Attrs["recoveredFrom"] != open[0].Instance {
		t.Errorf("end record should show who recovered what: %+v", end)
	}
}

// Each launch must mint its own instance id, or records from different runs
// cannot be told apart — and "did the previous instance exit cleanly?" becomes
// unanswerable.
func TestEachLaunchGetsAUniqueInstance(t *testing.T) {
	dir := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		r := mustOpen(t, dir, "server")
		if r.Instance() == "" {
			t.Fatal("launch produced no instance id")
		}
		if seen[r.Instance()] {
			t.Fatalf("instance id %q reused across launches", r.Instance())
		}
		seen[r.Instance()] = true
		r.Begin(KindServer, "", nil)
		_ = r.Close()
	}
	// Every one of those runs died without an end record, and each must be
	// individually visible as such.
	open, _ := Unfinished(dir)
	if len(open) != 5 {
		t.Fatalf("got %d unfinished lifetimes, want one per launch", len(open))
	}
}

// A recovery record closes the CRASHED run's lifetime but is written BY the
// recovering run. Matching on instance alone credited the recoverer with an end
// it never had — reporting a live server as already exited.
func TestCleanExitIgnoresARecoveryWrittenByThisInstance(t *testing.T) {
	dir := t.TempDir()

	crashed := mustOpen(t, dir, "server")
	crashed.Begin(KindServer, "", nil) // no end: killed
	_ = crashed.Close()

	live := mustOpen(t, dir, "server")
	live.Begin(KindServer, "", nil) // still running, no end
	open, _ := Unfinished(dir)
	for _, e := range open {
		if e.Instance == crashed.Instance() && e.Kind == KindServer {
			live.Resolve(e, StatusRecovered, "did not shut down cleanly")
		}
	}

	events, _ := Read(dir)
	if known, clean := CleanExit(events, live.Instance()); !known || clean {
		t.Errorf("live instance CleanExit = (%v, %v), want known but NOT clean — it is still running", known, clean)
	}
}

// Recovering a crashed run must not rewrite history into a clean exit: the
// incident has to stay legible forever, which is why StatusRecovered is a
// distinct status rather than an ordinary completion.
func TestRecoveryDoesNotMakeACrashLookClean(t *testing.T) {
	dir := t.TempDir()

	crashed := mustOpen(t, dir, "server")
	crashed.Begin(KindServer, "", nil)
	_ = crashed.Close()

	next := mustOpen(t, dir, "server")
	open, _ := Unfinished(dir)
	next.Resolve(open[0], StatusRecovered, "did not shut down cleanly")

	events, _ := Read(dir)
	known, clean := CleanExit(events, crashed.Instance())
	if !known {
		t.Fatal("the crashed run's lifetime is no longer known")
	}
	if clean {
		t.Error("a recovered crash must never read as a clean exit")
	}
}

// The ordinary case still has to work: a run that ended itself reads clean.
func TestCleanExitForARunThatEndedItself(t *testing.T) {
	dir := t.TempDir()
	r := mustOpen(t, dir, "server")
	life := r.Begin(KindServer, "", nil)
	life.End(nil)

	events, _ := Read(dir)
	if known, clean := CleanExit(events, r.Instance()); !known || !clean {
		t.Errorf("CleanExit = (%v, %v), want a clean exit", known, clean)
	}
}
