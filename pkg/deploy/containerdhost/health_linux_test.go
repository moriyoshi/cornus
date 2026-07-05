//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"testing"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/deploy/healthengine"
	"cornus/pkg/deploy/internal/hostrun"
)

// containerd used to WARN that it ignores healthchecks. It no longer does, and
// the tests that asserted that warning were removed — so this is the replacement
// contract: the healthcheck is recorded on the container, which is what lets the
// probe engine re-arm after a server restart, when the spec is gone.

func TestContainerLabelsRecordTheHealthcheck(t *testing.T) {
	hc := &api.Healthcheck{Test: []string{"CMD", "pg_isready"}, Interval: "5s", Retries: 2}
	l, err := containerLabels(api.DeploySpec{Name: "db", Healthcheck: hc}, hostrun.Attachment{}, nil, "")
	if err != nil {
		t.Fatalf("containerLabels: %v", err)
	}
	raw, ok := l[labelHealthcheck]
	if !ok {
		t.Fatal("no healthcheck label: after a server restart the probe engine has no way to " +
			"recover the check, so health would never come back for an already-deployed workload")
	}
	var got api.Healthcheck
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("healthcheck label is not readable JSON (%q): %v", raw, err)
	}
	if len(got.Test) != 2 || got.Test[1] != "pg_isready" || got.Interval != "5s" || got.Retries != 2 {
		t.Fatalf("round-tripped healthcheck = %+v, want the one that went in", got)
	}
}

// TestNoHealthcheckLabelWhenThereIsNothingToProbe: a workload with no check, or
// one explicitly disabled with Compose's `test: [NONE]`, must not get a label —
// otherwise syncHealth would arm a probe for a workload that asked for none.
func TestNoHealthcheckLabelWhenThereIsNothingToProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		hc   *api.Healthcheck
	}{
		{"absent", nil},
		{"disabled", &api.Healthcheck{Test: []string{"NONE"}}},
		{"empty test", &api.Healthcheck{Interval: "5s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := containerLabels(api.DeploySpec{Name: "x", Healthcheck: tc.hc}, hostrun.Attachment{}, nil, "")
			if err != nil {
				t.Fatalf("containerLabels: %v", err)
			}
			if raw, ok := l[labelHealthcheck]; ok {
				t.Fatalf("healthcheck label %q written for %s", raw, tc.name)
			}
		})
	}
}

// TestServerRestartRearmsProbing is the other half of persisting the healthcheck:
// recording it is worthless if nothing ever reads it back.
//
// A restart is simulated by building a SECOND backend over the same container
// store, which is what a restarted cornus sees — the first backend's in-memory
// engine is gone, and only the labels remain. Watch sets `starting` the moment an
// instance is armed, so this observes the arming itself rather than waiting out a
// 30s probe interval.
//
// The stopped deployment is the load-bearing half of the assertion. Without it a
// pass that armed EVERYTHING would look correct here, and it would report a
// stopped workload as unhealthy where Stop's own unwatch reports "".
func TestServerRestartRearmsProbing(t *testing.T) {
	f := newFakeClient()
	first, _ := newTestBackend(t, f)
	ctx := context.Background()
	hc := &api.Healthcheck{Test: []string{"CMD", "true"}}
	for _, name := range []string{"up", "down"} {
		if _, err := first.Apply(ctx, api.DeploySpec{Name: name, Image: "img", Healthcheck: hc}); err != nil {
			t.Fatalf("Apply %s: %v", name, err)
		}
	}
	if err := first.Stop(ctx, "down"); err != nil {
		t.Fatalf("Stop down: %v", err)
	}
	first.health.StopAll()

	restarted, _ := newTestBackend(t, f)
	t.Cleanup(restarted.health.StopAll)
	if _, _, err := restarted.reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := restarted.health.State("cornus-up-0"); got != healthengine.StateStarting {
		t.Fatalf("health of the running instance after restart = %q, want %q: the probe was not "+
			"re-armed, so this workload would report no health until someone redeployed it",
			got, healthengine.StateStarting)
	}
	if got := restarted.health.State("cornus-down-0"); got != "" {
		t.Fatalf("health of the STOPPED instance after restart = %q, want empty: a restart must not "+
			"arm a probe that Stop disarmed, or the instance reports unhealthy for being stopped", got)
	}
}

// TestDesiredRunningReadsPersistedIntent pins the predicate that decides which
// stopped containers the re-arm pass still probes: one the restart monitor is
// about to resurrect, not one the operator stopped.
func TestDesiredRunningReadsPersistedIntent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"managed and desired running", map[string]string{restart.PolicyLabel: "unless-stopped"}, true},
		{"no restart policy: nothing will resurrect it", map[string]string{}, false},
		{"explicitly stopped", map[string]string{restart.PolicyLabel: "always", restart.ExplicitlyStoppedLabel: "true"}, false},
		{"stop label cleared by Start", map[string]string{restart.PolicyLabel: "always", restart.ExplicitlyStoppedLabel: "false"}, true},
		{"desired state is stopped", map[string]string{restart.PolicyLabel: "always", restart.StatusLabel: string(ctd.Stopped)}, false},
	} {
		if got := desiredRunning(tc.labels); got != tc.want {
			t.Errorf("%s: desiredRunning = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestHealthcheckFromLabelsSurvivesGarbage: an unreadable label means no probing,
// which is what this backend did before the engine existed. Failing the deploy
// over it would be a worse outcome than not probing.
func TestHealthcheckFromLabelsSurvivesGarbage(t *testing.T) {
	if hc := healthcheckFromLabels(map[string]string{labelHealthcheck: "{not json"}); hc != nil {
		t.Fatalf("garbage label produced %+v, want nil", hc)
	}
	if hc := healthcheckFromLabels(nil); hc != nil {
		t.Fatalf("absent label produced %+v, want nil", hc)
	}
}
