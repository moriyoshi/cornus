package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
