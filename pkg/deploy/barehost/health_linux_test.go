//go:build linux

package barehost

import (
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy/healthengine"
)

// bare used to WARN that it ignores healthchecks; the tests asserting that
// warning are gone. This is the replacement contract: the check is PERSISTED on
// the instance record, which is what lets the probe engine re-arm after a server
// restart, when the spec is no longer in hand.

func TestHealthcheckToRecordKeepsWhatIsProbeable(t *testing.T) {
	hc := &api.Healthcheck{Test: []string{"CMD", "pg_isready"}, Interval: "5s", Retries: 2}
	got := healthcheckToRecord(hc)
	if got == nil {
		t.Fatal("a real healthcheck was not persisted: after a server restart the engine could " +
			"not recover it, and health would never come back for an already-deployed workload")
	}
	if got.Interval != "5s" || got.Retries != 2 || len(got.Test) != 2 {
		t.Fatalf("persisted healthcheck = %+v, want the one that went in", got)
	}
}

// TestHealthcheckToRecordDropsWhatIsNot: a workload with no check, or one
// explicitly disabled with Compose's `test: [NONE]`, must persist nothing —
// otherwise syncHealth would arm a probe for a workload that asked for none.
func TestHealthcheckToRecordDropsWhatIsNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		hc   *api.Healthcheck
	}{
		{"absent", nil},
		{"disabled", &api.Healthcheck{Test: []string{"NONE"}}},
		{"empty test", &api.Healthcheck{Interval: "5s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthcheckToRecord(tc.hc); got != nil {
				t.Fatalf("persisted %+v for %s, want nil", got, tc.name)
			}
		})
	}
}

// TestStartupReconcileRearmsProbing is the other half of persisting the check:
// recording it is worthless if nothing ever reads it back.
//
// reconcile() is the pass New kicks on startup, so this is what a restarted
// cornus does with instances that outlived it. Watch sets `starting` the moment
// an instance is armed, so this observes the arming itself rather than waiting
// out a 30s probe interval.
//
// The explicitly-stopped instance is the load-bearing half. Without it a pass
// that armed EVERYTHING would look correct here, and it would report a stopped
// workload as unhealthy where Stop's own unwatch reports "".
func TestStartupReconcileRearmsProbing(t *testing.T) {
	b, rt := newTestBackend(t)
	t.Cleanup(b.health.StopAll)
	hc := &api.Healthcheck{Test: []string{"CMD", "true"}}

	up := seedInstance(t, b, rt, "up", 0, true)
	up.DesiredRunning = true
	up.Healthcheck = hc
	if err := b.writeRecord(up); err != nil {
		t.Fatalf("writeRecord up: %v", err)
	}
	down := seedInstance(t, b, rt, "down", 0, false)
	down.DesiredRunning = false
	down.ExplicitlyStopped = true
	down.Healthcheck = hc
	if err := b.writeRecord(down); err != nil {
		t.Fatalf("writeRecord down: %v", err)
	}

	b.reconcile()

	if got := b.health.State(up.ID); got != healthengine.StateStarting {
		t.Fatalf("health of the running instance after a startup reconcile = %q, want %q: the probe "+
			"was not re-armed, so this workload would report no health until someone redeployed it",
			got, healthengine.StateStarting)
	}
	if got := b.health.State(down.ID); got != "" {
		t.Fatalf("health of the STOPPED instance after a startup reconcile = %q, want empty: a "+
			"restart must not arm a probe that Stop disarmed, or the instance reports unhealthy "+
			"for being stopped", got)
	}
}
