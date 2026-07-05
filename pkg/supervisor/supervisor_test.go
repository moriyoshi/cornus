package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/activity"
)

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func TestRemoveOnExitDropsAndDecrements(t *testing.T) {
	s := New(context.Background(), func(string, ...any) {})
	exit := make(chan struct{})
	s.Add("one", ServiceFunc(func(ctx context.Context) error {
		<-exit
		return nil
	}), RemoveOnExit)
	if s.Count() != 1 {
		t.Fatalf("Count = %d, want 1", s.Count())
	}
	close(exit)
	waitFor(t, func() bool { return s.Count() == 0 }, "count to drop to 0")
}

func TestIdleHookFiresAtZero(t *testing.T) {
	s := New(context.Background(), func(string, ...any) {})
	var idle atomic.Int32
	s.SetIdleHook(func() { idle.Add(1) })

	exit := make(chan struct{})
	wait := func(ctx context.Context) error {
		select {
		case <-exit:
		case <-ctx.Done():
		}
		return nil
	}
	t1 := s.Add("a", ServiceFunc(wait), RemoveOnExit)
	t2 := s.Add("b", ServiceFunc(wait), RemoveOnExit)

	// Removing one of two counted children must NOT fire idle.
	go s.Remove(t1)
	waitFor(t, func() bool { return s.Count() == 1 }, "count 1")
	if idle.Load() != 0 {
		t.Fatalf("idle fired early: %d", idle.Load())
	}
	// Removing the last one fires idle exactly once.
	close(exit)
	s.Remove(t2)
	waitFor(t, func() bool { return idle.Load() == 1 }, "idle to fire once")
}

func TestPanicIsIsolatedAndSiblingSurvives(t *testing.T) {
	s := New(context.Background(), func(string, ...any) {})
	var siblingAlive atomic.Bool
	siblingAlive.Store(true)

	// A sibling that runs until cancelled.
	sibCtx := make(chan struct{})
	s.Add("sibling", ServiceFunc(func(ctx context.Context) error {
		<-ctx.Done()
		close(sibCtx)
		return nil
	}), RemoveOnExit)

	// A counted RemoveOnExit child that panics immediately: it must be dropped,
	// not crash the process or the sibling.
	s.Add("boom", ServiceFunc(func(ctx context.Context) error {
		panic("boom")
	}), RemoveOnExit)

	waitFor(t, func() bool { return s.Count() == 1 }, "boom child to be dropped, sibling remaining")
	if !siblingAlive.Load() {
		t.Fatal("sibling should still be alive")
	}
	select {
	case <-sibCtx:
		t.Fatal("sibling was cancelled by the panic")
	default:
	}
}

func TestRestartRelaunchesOnPanic(t *testing.T) {
	s := New(context.Background(), func(string, ...any) {})
	var runs atomic.Int32
	s.Add("flaky", ServiceFunc(func(ctx context.Context) error {
		runs.Add(1)
		panic("die")
	}), Restart)
	// It should be relaunched repeatedly (backoff starts at 100ms).
	waitFor(t, func() bool { return runs.Load() >= 2 }, "restart to relaunch at least twice")
	if s.Count() != 1 {
		t.Fatalf("a Restart child stays counted; Count = %d", s.Count())
	}
}

func TestContextCancelDrainsAllNoRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, func(string, ...any) {})
	var running sync.WaitGroup
	running.Add(1)
	var started atomic.Bool
	s.Add("long", ServiceFunc(func(ctx context.Context) error {
		started.Store(true)
		defer running.Done()
		<-ctx.Done()
		return ctx.Err()
	}), Restart) // Restart, but cancel must NOT relaunch it
	waitFor(t, func() bool { return started.Load() }, "child to start")
	cancel()
	s.Wait()
	running.Wait() // child drained
}

// A crash-looping child leaves nothing behind once the process is gone, so the
// restarts must be recorded — one pair per Serve attempt, with the failure.
func TestRecordsEachChildRun(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, func(string, ...any) {})
	s.SetRecorder(rec)

	attempts := make(chan struct{}, 8)
	tok := s.Add("flaky", ServiceFunc(func(ctx context.Context) error {
		select {
		case attempts <- struct{}{}:
		default:
		}
		return errors.New("boom")
	}), Restart)

	// Wait for a couple of restarts, then stop it.
	for i := 0; i < 2; i++ {
		select {
		case <-attempts:
		case <-time.After(5 * time.Second):
			t.Fatal("child did not restart")
		}
	}
	s.Remove(tok)

	events, err := activity.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	var runs, failures int
	for _, e := range events {
		if e.Kind != activity.KindService || e.Target != "flaky" {
			continue
		}
		if e.Phase == activity.PhaseBegin {
			runs++
			if e.Attrs["policy"] != "restart" {
				t.Errorf("policy attr = %q, want restart", e.Attrs["policy"])
			}
		}
		if e.Phase == activity.PhaseEnd && e.Status == activity.StatusError {
			failures++
			if !strings.Contains(e.Err, "boom") {
				t.Errorf("end did not carry the failure: %+v", e)
			}
		}
	}
	if runs < 2 {
		t.Errorf("recorded %d runs, want one per Serve attempt", runs)
	}
	if failures < 1 {
		t.Errorf("recorded %d failures, want the crash recorded", failures)
	}
}

// A child stopped because the supervisor asked it to is a clean stop. Serve
// conventionally returns ctx.Err() there, and recording that as a failure would
// make every ordinary shutdown read as an incident.
func TestCancelledChildIsNotRecordedAsAFailure(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, func(string, ...any) {})
	s.SetRecorder(rec)

	started := make(chan struct{})
	tok := s.Add("worker", ServiceFunc(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err() // the usual shape
	}), Restart)
	<-started
	s.Remove(tok)

	events, _ := activity.Read(dir)
	for _, e := range events {
		if e.Kind == activity.KindService && e.Phase == activity.PhaseEnd && e.Status == activity.StatusError {
			t.Fatalf("a cancelled child was recorded as a failure: %+v", e)
		}
	}
	// And it must be closed, not left looking like it died with the process.
	if open, _ := activity.Unfinished(dir); len(open) != 0 {
		t.Errorf("cancelled child left an open record: %+v", open)
	}
}

// A child still running when the process dies leaves an open record — which is
// the closest thing to a snapshot of what the process was doing at the moment
// it stopped.
func TestRunningChildStaysUnfinished(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, func(string, ...any) {})
	s.SetRecorder(rec)

	started := make(chan struct{})
	s.Add("long-running", ServiceFunc(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return nil
	}), Restart)
	<-started

	// Simulate the process vanishing: the recorder is closed without the child
	// ever ending, exactly as a SIGKILL would leave it.
	_ = rec.Close()

	open, _ := activity.Unfinished(dir)
	var found bool
	for _, e := range open {
		if e.Kind == activity.KindService && e.Target == "long-running" {
			found = true
		}
	}
	if !found {
		t.Error("a service still running at process death must be visible as unfinished")
	}
	cancel()
	s.Wait()
}

// A supervisor with no recorder (the client agent) must behave exactly as before.
func TestNoRecorderIsANoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, func(string, ...any) {})
	done := make(chan struct{})
	s.Add("plain", ServiceFunc(func(context.Context) error { close(done); return nil }), RemoveOnExit)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child never ran without a recorder")
	}
}
