package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
)

// scriptedBackend returns a preset sequence of Status results (the last entry
// repeats once exhausted), so a test can model a workload coming up over several
// polls or staying wedged. It embeds fakeBackend for the rest of the interface.
type scriptedBackend struct {
	fakeBackend
	mu   sync.Mutex
	seq  []api.DeployStatus
	idx  int
	seen int // total Status calls, for assertions
}

func (b *scriptedBackend) Status(_ context.Context, name string) (api.DeployStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen++
	i := b.idx
	if i >= len(b.seq) {
		i = len(b.seq) - 1
	} else {
		b.idx++
	}
	return b.seq[i], nil
}

// svc is the spec for a long-lived service (default restart policy) named name.
func svc(name string) api.DeploySpec {
	return api.DeploySpec{Name: name, Image: "img"}
}

// oneShot is the spec for a run-to-completion workload (restart: no) named name.
func oneShot(name string) api.DeploySpec {
	return api.DeploySpec{Name: name, Image: "img", Restart: "no"}
}

// completed is a status whose single instance has terminated cleanly (exit 0) —
// what a finished one-shot Job reports.
func completed(name string) api.DeployStatus {
	zero := 0
	return api.DeployStatus{Name: name, Backend: "fake", Instances: []api.InstanceStatus{
		{ID: name + "-0", State: "succeeded", Running: false, ExitCode: &zero},
	}}
}

func pending(name, msg string) api.DeployStatus {
	return api.DeployStatus{Name: name, Backend: "fake", Instances: []api.InstanceStatus{
		{ID: name + "-0", State: "pending", Running: false, Message: msg},
	}}
}

func running(name string) api.DeployStatus {
	return api.DeployStatus{Name: name, Backend: "fake", Instances: []api.InstanceStatus{
		{ID: name + "-0", State: "running", Running: true},
	}}
}

// collectEmit returns an emit func and a pointer to the slice of events it records.
func collectEmit() (func(deploywire.Event), *[]deploywire.Event) {
	var mu sync.Mutex
	var evs []deploywire.Event
	return func(e deploywire.Event) {
		mu.Lock()
		evs = append(evs, e)
		mu.Unlock()
	}, &evs
}

func withFastTimers(t *testing.T, poll, timeout time.Duration) {
	t.Helper()
	op, ot := readyPollInterval, readyTimeout
	readyPollInterval, readyTimeout = poll, timeout
	t.Cleanup(func() { readyPollInterval, readyTimeout = op, ot })
}

func TestAwaitReadyShortCircuitsWhenAlreadyRunning(t *testing.T) {
	be := &scriptedBackend{seq: []api.DeployStatus{running("web")}}
	emit, evs := collectEmit()
	st, err := awaitReady(context.Background(), emit, be, svc("web"), running("web"))
	if err != nil {
		t.Fatalf("awaitReady: %v", err)
	}
	if !allReady(st, false) {
		t.Fatalf("status not running: %+v", st)
	}
	if be.seen != 0 {
		t.Errorf("Status polled %d times; want 0 (initial was already ready)", be.seen)
	}
	if len(*evs) != 0 {
		t.Errorf("emitted %d events; want 0", len(*evs))
	}
}

func TestAwaitReadyWaitsThenReady(t *testing.T) {
	withFastTimers(t, time.Millisecond, 2*time.Second)
	be := &scriptedBackend{seq: []api.DeployStatus{
		pending("web", ""),
		pending("web", ""),
		running("web"),
	}}
	emit, evs := collectEmit()
	st, err := awaitReady(context.Background(), emit, be, svc("web"), pending("web", ""))
	if err != nil {
		t.Fatalf("awaitReady: %v", err)
	}
	if !allReady(st, false) {
		t.Fatalf("status not running: %+v", st)
	}
	for _, e := range *evs {
		if e.Err != "" {
			t.Errorf("unexpected error event while coming up cleanly: %q", e.Err)
		}
	}
}

func TestAwaitReadyStreamsCrashLoopThenTimesOut(t *testing.T) {
	withFastTimers(t, time.Millisecond, 40*time.Millisecond)
	const diag = "cornus-caretaker: CrashLoopBackOff: back-off restarting failed container"
	be := &scriptedBackend{seq: []api.DeployStatus{pending("mnt", diag)}}
	emit, evs := collectEmit()

	_, err := awaitReady(context.Background(), emit, be, svc("mnt"), pending("mnt", diag))
	if err == nil {
		t.Fatal("awaitReady returned nil; want a timeout error")
	}
	if !strings.Contains(err.Error(), "did not become ready") || !strings.Contains(err.Error(), diag) {
		t.Errorf("error = %q, want it to mention the timeout and the crash-loop diagnostic", err)
	}
	// The crash-loop diagnostic must have been streamed to the caller exactly once
	// (deduped) despite being reported on every poll.
	var crashEvents int
	for _, e := range *evs {
		if e.Err == diag {
			crashEvents++
		}
	}
	if crashEvents != 1 {
		t.Errorf("crash-loop diagnostic streamed %d times; want exactly 1 (deduped)", crashEvents)
	}
}

// A one-shot that has already completed cleanly is ready immediately: readiness
// for a run-to-completion workload accepts a clean exit, not only Running. A
// long-lived spec with the same completed status would NOT be ready (regression
// guard for the relaxation being scoped to one-shots).
func TestAwaitReadyOneShotCompletedIsReady(t *testing.T) {
	be := &scriptedBackend{seq: []api.DeployStatus{completed("init")}}
	emit, _ := collectEmit()
	if _, err := awaitReady(context.Background(), emit, be, oneShot("init"), completed("init")); err != nil {
		t.Fatalf("one-shot completed cleanly must be ready: %v", err)
	}
	if allReady(completed("init"), false) {
		t.Fatal("a completed instance must NOT be ready for a long-lived (non one-shot) spec")
	}
	if !allReady(completed("init"), true) {
		t.Fatal("a cleanly-completed instance must be ready for a one-shot spec")
	}
}

// A one-shot that runs first, then completes, becomes ready on the completing poll.
func TestAwaitReadyOneShotRunsThenCompletes(t *testing.T) {
	withFastTimers(t, time.Millisecond, 2*time.Second)
	be := &scriptedBackend{seq: []api.DeployStatus{
		pending("init", ""),
		running("init"),
		completed("init"),
	}}
	emit, _ := collectEmit()
	if _, err := awaitReady(context.Background(), emit, be, oneShot("init"), pending("init", "")); err != nil {
		t.Fatalf("one-shot run-then-complete must become ready: %v", err)
	}
}

func TestAwaitReadyStopsOnContextCancel(t *testing.T) {
	withFastTimers(t, time.Millisecond, time.Hour)
	be := &scriptedBackend{seq: []api.DeployStatus{pending("mnt", "")}}
	emit, _ := collectEmit()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client disconnected
	_, err := awaitReady(ctx, emit, be, svc("mnt"), pending("mnt", ""))
	if err == nil {
		t.Fatal("awaitReady returned nil; want the cancelled context error")
	}
}
