//go:build linux

package barehost

// The in-process supervisor's restart decisions, driven directly rather than
// through a real init exit. onExit IS the restart monitor this backend replaces
// a daemon with, so what it does after an exit — and, just as importantly, what
// it refuses to do — is the contract worth pinning.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runOnExit drives one exit through the supervisor and returns when it has
// settled. Watchers the restart arms are cancelled before the test ends so no
// goroutine outlives it.
func runOnExit(t *testing.T, b *Backend, id string, ranFor time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); b.super.onExit(context.Background(), id, ranFor) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("onExit did not settle")
	}
	// A successful restart arms a fresh watcher; cancel it so nothing outlives
	// the test.
	b.super.stopAll()
}

// writePidFile stands in for `runc create --pid-file`, which the fake runtime
// does not perform.
func writePidFile(t *testing.T, b *Backend, id, pid string) {
	t.Helper()
	dir := b.recordDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(pid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func launchCalls(rt *fakeRuntime, id string) int {
	n := 0
	for _, c := range rt.calls {
		if c == "create:"+id {
			n++
		}
	}
	return n
}

// TestOnExitRestartsADesiredRunningInstance covers the core loop: a crash of an
// instance the operator still wants running is relaunched from its existing
// bundle and the restart tally is persisted.
func TestOnExitRestartsADesiredRunningInstance(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	rec.DesiredRunning = true
	rec.Restart = "always"
	if err := b.writeRecord(rec); err != nil {
		t.Fatal(err)
	}

	runOnExit(t, b, rec.ID, time.Second)

	if got := launchCalls(rt, rec.ID); got != 1 {
		t.Errorf("create calls = %d, want exactly one relaunch; calls=%v", got, rt.calls)
	}
	after, err := b.readRecord(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RestartCount != 1 {
		t.Errorf("restartCount = %d, want 1 after one restart", after.RestartCount)
	}
}

// TestOnExitDoesNotResurrectAStoppedInstance covers the decisions that must NOT
// restart. Each of these is a way an operator or a policy says "leave it down",
// and getting any of them wrong turns a deliberate stop into a crash loop.
func TestOnExitDoesNotResurrectAStoppedInstance(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*instanceRecord)
	}{
		{"explicitly stopped", func(r *instanceRecord) { r.DesiredRunning = true; r.ExplicitlyStopped = true }},
		{"not desired running", func(r *instanceRecord) { r.DesiredRunning = false }},
		{"restart policy no", func(r *instanceRecord) { r.DesiredRunning = true; r.Restart = "no" }},
		{"on-failure attempts exhausted", func(r *instanceRecord) {
			r.DesiredRunning = true
			r.Restart = "on-failure"
			r.MaxAttempts = 2
			r.RestartCount = 2
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, rt := newTestBackend(t)
			rec := seedInstance(t, b, rt, "web", 0, false)
			tc.mut(rec)
			if err := b.writeRecord(rec); err != nil {
				t.Fatal(err)
			}

			runOnExit(t, b, rec.ID, time.Second)

			if got := launchCalls(rt, rec.ID); got != 0 {
				t.Errorf("create calls = %d, want none; calls=%v", got, rt.calls)
			}
		})
	}
}

// TestOnExitForAVanishedRecordIsQuiet covers the Delete race: the record is gone
// by the time the exit is observed, so there is nothing left to supervise.
func TestOnExitForAVanishedRecordIsQuiet(t *testing.T) {
	b, rt := newTestBackend(t)
	runOnExit(t, b, "cornus-ghost-0", time.Second)
	if len(rt.calls) != 0 {
		t.Errorf("calls = %v, want the runtime untouched for a deleted instance", rt.calls)
	}
}

// TestOnExitRestartsALongLivedInstanceWithoutTheAccumulatedBackoff pins the
// stable-run rule: a workload that ran past the stability threshold and then
// crashed restarts at the base delay instead of inheriting a backoff earned long
// ago. Note the reset applies to the DELAY only — the persisted tally is the
// instance's lifetime restart count (it also feeds the on-failure attempt cap)
// and keeps climbing.
func TestOnExitRestartsALongLivedInstanceWithoutTheAccumulatedBackoff(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	rec.DesiredRunning = true
	rec.Restart = "always"
	rec.RestartCount = 6 // would otherwise imply a multi-second backoff
	if err := b.writeRecord(rec); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	runOnExit(t, b, rec.ID, stableRunThreshold+time.Second)
	elapsed := time.Since(start)

	if want := backoffFor(6); elapsed >= want {
		t.Errorf("onExit waited %v; a stable run must restart at the %v base delay, not %v", elapsed, minBackoff, want)
	}
	if got := launchCalls(rt, rec.ID); got != 1 {
		t.Errorf("create calls = %d, want the relaunch; calls=%v", got, rt.calls)
	}
	after, err := b.readRecord(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RestartCount != 7 {
		t.Errorf("restartCount = %d, want the lifetime tally advanced to 7", after.RestartCount)
	}
}

// TestOnExitKeepsTheBackoffForACrashLooper is the counterpart: an instance that
// died quickly keeps its accumulated tally so the backoff actually grows.
func TestOnExitKeepsTheBackoffForACrashLooper(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	rec.DesiredRunning = true
	rec.Restart = "always"
	rec.RestartCount = 2
	if err := b.writeRecord(rec); err != nil {
		t.Fatal(err)
	}

	runOnExit(t, b, rec.ID, 10*time.Millisecond) // well under the stable threshold

	after, err := b.readRecord(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RestartCount != 3 {
		t.Errorf("restartCount = %d, want the tally carried forward to 3", after.RestartCount)
	}
}

// TestOnExitStopsSupervisingAfterAFailedRestart covers the give-up path: a
// relaunch that fails must not spin — the next reconcile owns the retry.
func TestOnExitStopsSupervisingAfterAFailedRestart(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	rec.DesiredRunning = true
	rec.Restart = "always"
	if err := b.writeRecord(rec); err != nil {
		t.Fatal(err)
	}
	rt.createErr = errCreateBoom

	runOnExit(t, b, rec.ID, time.Second)

	after, err := b.readRecord(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RestartCount != 0 {
		t.Errorf("restartCount = %d, want it untouched by a restart that never happened", after.RestartCount)
	}
	if got := launchCalls(rt, rec.ID); got != 1 {
		t.Errorf("create attempts = %d, want exactly one (a failed restart must not spin); calls=%v", got, rt.calls)
	}
}

// --- restartInstance ---

// TestRestartInstanceRecreatesFromTheExistingBundle pins the reuse that keeps an
// instance's IP, mounts and rootfs stable across a restart: the previous
// generation's runtime state is cleared, then the SAME bundle is run again.
func TestRestartInstanceRecreatesFromTheExistingBundle(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)

	pid, err := b.restartInstance(t.Context(), rec)
	if err != nil {
		t.Fatalf("restartInstance: %v", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0 when the runtime wrote no pid file", pid)
	}
	if got := strings.Join(rt.calls, ","); got != "delete:"+rec.ID+",create:"+rec.ID+",start:"+rec.ID {
		t.Errorf("calls = %q, want delete then create then start", got)
	}
	if st, _ := rt.State(t.Context(), rec.ID); st.Status != runcStateRunning {
		t.Errorf("state after restart = %q, want running", st.Status)
	}
}

// TestRestartInstanceReadsThePidFileTheRuntimeWrote proves the supervisor tracks
// the NEW generation's init, not a stale pid.
func TestRestartInstanceReadsThePidFileTheRuntimeWrote(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	writePidFile(t, b, rec.ID, "9091")

	pid, err := b.restartInstance(t.Context(), rec)
	if err != nil {
		t.Fatalf("restartInstance: %v", err)
	}
	if pid != 9091 {
		t.Errorf("pid = %d, want the 9091 the runtime recorded", pid)
	}
}

func TestRestartInstancePropagatesRuntimeFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeRuntime)
	}{
		{"create fails", func(rt *fakeRuntime) { rt.createErr = errCreateBoom }},
		{"start fails", func(rt *fakeRuntime) { rt.startErr = errStartBoom }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, rt := newTestBackend(t)
			rec := seedInstance(t, b, rt, "web", 0, false)
			tc.set(rt)
			if _, err := b.restartInstance(t.Context(), rec); err == nil {
				t.Error("restartInstance: want the runtime failure surfaced")
			}
		})
	}
}
