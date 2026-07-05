//go:build linux

package containerdhost

import (
	"testing"
	"time"

	"cornus/pkg/api"
)

// TestExecRegistryReapsFinishedSessions verifies that add() reaps finished
// sessions older than execRetention, so a long-lived daemon serving many
// execs does not accumulate execSession entries without bound. Running
// sessions and recently-finished ones are retained (a late ExecInspect still
// works within the retention window).
func TestExecRegistryReapsFinishedSessions(t *testing.T) {
	r := newExecRegistry()

	// A finished session older than the retention window: must be reaped.
	oldID, oldSess := r.add("c-old", api.ExecConfig{})
	oldSess.mu.Lock()
	oldSess.finishedAt = time.Now().Add(-2 * execRetention)
	oldSess.mu.Unlock()

	// A finished session inside the retention window: must be kept.
	recentID, recentSess := r.add("c-recent", api.ExecConfig{})
	recentSess.mu.Lock()
	recentSess.finishedAt = time.Now()
	recentSess.mu.Unlock()

	// A still-running session (finishedAt zero): must never be reaped.
	runningID, _ := r.add("c-running", api.ExecConfig{})

	// Any subsequent add triggers a reap sweep.
	r.add("c-trigger", api.ExecConfig{})

	if _, err := r.get(oldID); err == nil {
		t.Fatalf("stale finished session %q should have been reaped", oldID)
	}
	if _, err := r.get(recentID); err != nil {
		t.Fatalf("recently-finished session %q should be retained: %v", recentID, err)
	}
	if _, err := r.get(runningID); err != nil {
		t.Fatalf("running session %q must never be reaped: %v", runningID, err)
	}
}
