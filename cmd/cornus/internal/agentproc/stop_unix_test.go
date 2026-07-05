//go:build unix

package agentproc

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid is still a live process.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestSignalAndWaitSkipsUnrelatedPid locks the fix for the stale/reused-pid
// hazard: when the pid no longer belongs to a cornus agent, signalAndWait must
// treat it as already gone and NOT signal it, so it can never SIGTERM/SIGKILL an
// unrelated process that recycled the agent's pid.
func TestSignalAndWaitSkipsUnrelatedPid(t *testing.T) {
	// A real, unrelated process standing in for the recycled-pid victim.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	pid := victim.Process.Pid
	t.Cleanup(func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() })

	orig := pidIsAgent
	t.Cleanup(func() { pidIsAgent = orig })
	pidIsAgent = func(int) bool { return false } // identity does not match the agent

	if err := signalAndWait(pid, 200*time.Millisecond); err != nil {
		t.Fatalf("signalAndWait on a non-agent pid = %v, want nil", err)
	}
	if !alive(pid) {
		t.Fatal("signalAndWait killed an unrelated process whose identity did not match")
	}
}

// TestSignalAndWaitStopsAgentPid confirms the guard still lets a genuine agent
// pid be signalled to death.
func TestSignalAndWaitStopsAgentPid(t *testing.T) {
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	pid := victim.Process.Pid
	done := make(chan struct{})
	go func() { _, _ = victim.Process.Wait(); close(done) }()
	t.Cleanup(func() { _ = victim.Process.Kill() })

	orig := pidIsAgent
	t.Cleanup(func() { pidIsAgent = orig })
	pidIsAgent = func(int) bool { return true } // identity matches the agent

	err := signalAndWait(pid, 2*time.Second)
	if err != nil {
		t.Fatalf("signalAndWait = %v, want nil after graceful SIGTERM exit", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process was not stopped")
	}
}

// TestStopFallbackHonorsIdentityGuard drives the guard through Stop: a failed
// graceful stop with a stale pid in the state file must not signal an unrelated
// process.
func TestStopFallbackHonorsIdentityGuard(t *testing.T) {
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	pid := victim.Process.Pid
	t.Cleanup(func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() })

	orig := pidIsAgent
	t.Cleanup(func() { pidIsAgent = orig })
	pidIsAgent = func(int) bool { return false }

	spec := Spec{Socket: "/x.sock", StatePath: filepath.Join(t.TempDir(), "s.json")}
	if err := WriteState(spec.StatePath, State{Pid: pid, Socket: spec.Socket}); err != nil {
		t.Fatal(err)
	}
	err := Stop(spec, func(string) error { return fmt.Errorf("dead socket") })
	if err != nil {
		t.Fatalf("Stop = %v, want nil (stale pid treated as gone)", err)
	}
	if !alive(pid) {
		t.Fatal("Stop signalled an unrelated process via a stale pid")
	}
	if st, _ := ReadState(spec.StatePath); st != nil {
		t.Fatal("Stop did not remove state after treating the pid as gone")
	}
}
