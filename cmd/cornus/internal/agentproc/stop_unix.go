//go:build unix

package agentproc

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// pidIsAgent reports whether pid is (still) a cornus agent process, guarding
// Stop's signal fallback so a stale/reused pid from a state file left behind by
// an unclean death (OOM/SIGKILL/crash) never gets SIGTERM/SIGKILL after the OS
// recycled it onto an unrelated process. The daemon re-execs this same binary
// (daemonize.Spawn uses os.Executable), so a live agent's /proc/<pid>/exe
// resolves to the same path as ours. It is a package var so tests can inject a
// deterministic identity without spawning real processes.
var pidIsAgent = func(pid int) bool {
	self := procExe(os.Getpid())
	if self == "" {
		// Identity is unverifiable on this host (e.g. no /proc); fall back to the
		// legacy signal behavior rather than refuse to stop anything.
		return true
	}
	return procExe(pid) == self
}

// procExe resolves pid's running executable path via /proc, or "" when it cannot
// be determined (no /proc, process gone, or insufficient permission).
func procExe(pid int) string {
	p, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ""
	}
	return p
}

// signalAndWait sends SIGTERM to pid and waits up to timeout for it to exit,
// escalating to SIGKILL. A missing process — or one whose identity no longer
// matches a cornus agent — is treated as already stopped.
func signalAndWait(pid int, timeout time.Duration) error {
	if !pidIsAgent(pid) {
		return nil // stale/reused pid — the agent is already gone
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil // already gone
		}
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Re-verify identity before the hard kill: the pid may have exited and been
	// recycled during the grace period.
	if !pidIsAgent(pid) {
		return nil
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return fmt.Errorf("process %d did not exit within %s (sent SIGKILL)", pid, timeout)
}
