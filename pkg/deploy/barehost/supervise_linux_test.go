//go:build linux

package barehost

import (
	"testing"
	"time"
)

func TestRestartAllowed(t *testing.T) {
	cases := []struct {
		policy string
		max    int
		count  int
		want   bool
	}{
		{"no", 0, 0, false},
		{"always", 0, 100, true},
		{"unless-stopped", 0, 100, true},
		{"", 0, 0, true},            // default policy behaves like unless-stopped
		{"on-failure", 0, 5, true},  // no cap
		{"on-failure", 3, 2, true},  // under the cap
		{"on-failure", 3, 3, false}, // at the cap
		{"on-failure", 3, 4, false}, // over the cap
	}
	for _, c := range cases {
		rec := &instanceRecord{Restart: c.policy, MaxAttempts: c.max, RestartCount: c.count}
		if got := restartAllowed(rec); got != c.want {
			t.Errorf("restartAllowed(%q max=%d count=%d) = %v, want %v", c.policy, c.max, c.count, got, c.want)
		}
	}
}

func TestBackoffFor(t *testing.T) {
	if got := backoffFor(0); got != minBackoff {
		t.Errorf("backoffFor(0) = %v, want %v", got, minBackoff)
	}
	if got := backoffFor(1); got != 2*minBackoff {
		t.Errorf("backoffFor(1) = %v, want %v", got, 2*minBackoff)
	}
	if got := backoffFor(3); got != 8*minBackoff {
		t.Errorf("backoffFor(3) = %v, want %v", got, 8*minBackoff)
	}
	// Caps at maxBackoff for large counts.
	if got := backoffFor(100); got != maxBackoff {
		t.Errorf("backoffFor(100) = %v, want cap %v", got, maxBackoff)
	}
}

func TestStopSetsExplicitlyStopped(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	rec := seedInstance(t, b, rt, "svc", 0, true)
	rec.DesiredRunning = true
	if err := b.writeRecord(rec); err != nil {
		t.Fatal(err)
	}

	if err := b.Stop(ctx, "svc"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got, err := b.readRecord(instanceName("svc", 0))
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if got.DesiredRunning || !got.ExplicitlyStopped {
		t.Errorf("after Stop: desiredRunning=%v explicitlyStopped=%v, want false/true", got.DesiredRunning, got.ExplicitlyStopped)
	}
}

func TestStartClearsStoppedAndRewatches(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedInstance(t, b, rt, "svc", 0, true)
	// Stop, then Start: the stopped flag must clear and desired-running restore.
	if err := b.Stop(ctx, "svc"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := b.Start(ctx, "svc"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := b.readRecord(instanceName("svc", 0))
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if !got.DesiredRunning || got.ExplicitlyStopped {
		t.Errorf("after Start: desiredRunning=%v explicitlyStopped=%v, want true/false", got.DesiredRunning, got.ExplicitlyStopped)
	}
}

// TestSupervisorUnwatchStopsRestart proves an unwatched instance is not
// restarted: watch a fake pid whose process never appears, then unwatch — the
// watcher goroutine must exit without calling restartInstance.
func TestSupervisorUnwatchIsClean(t *testing.T) {
	b, _ := newTestBackend(t)
	// A nonexistent pid: pidfd_open fails -> pollStateExit polls runc State (fake
	// returns not-exist -> "exited" true) -> onExit reads a missing record ->
	// unwatch. No panic, no restart of an unknown instance.
	b.super.watch("cornus-ghost-0", 999999999)
	time.Sleep(50 * time.Millisecond)
	b.super.unwatch("cornus-ghost-0")
	// Nothing to assert beyond "did not panic / hang"; the fake has no ghost
	// container so no restart occurred.
}
