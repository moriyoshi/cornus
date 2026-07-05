//go:build linux

package incushost

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy/healthengine"
)

// incus used to WARN that it ignores healthchecks; the tests asserting that
// warning are gone. This is the replacement contract: the check is recorded on the
// INSTANCE CONFIG, which is what lets the probe engine re-arm after a server
// restart, when the spec is no longer in hand.

func TestHealthcheckRoundTripsThroughInstanceConfig(t *testing.T) {
	hc := &api.Healthcheck{Test: []string{"CMD", "redis-cli", "ping"}, Interval: "2s", Retries: 15}
	raw := healthcheckToConfig(hc)
	if raw == "" {
		t.Fatal("a real healthcheck was not recorded: after a server restart the engine could not " +
			"recover it, and health would never come back for an already-deployed workload")
	}
	got := healthcheckFromConfig(map[string]string{healthcheckConfigKey: raw})
	if got == nil {
		t.Fatal("recorded healthcheck did not read back")
	}
	if got.Interval != "2s" || got.Retries != 15 || len(got.Test) != 3 {
		t.Fatalf("round-tripped healthcheck = %+v, want the one that went in", got)
	}
	// Must live in incus's user-writable key space, or the create is rejected.
	if want := configKeyPrefix; len(healthcheckConfigKey) <= len(want) || healthcheckConfigKey[:len(want)] != want {
		t.Errorf("healthcheck config key %q is outside the %q key space incus allows", healthcheckConfigKey, want)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Errorf("recorded value is not JSON: %v", err)
	}
}

// TestNothingRecordedWhenThereIsNothingToProbe: a workload with no check, or one
// explicitly disabled with Compose's `test: [NONE]`, must record nothing —
// otherwise syncHealth would arm a probe for a workload that asked for none.
func TestNothingRecordedWhenThereIsNothingToProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		hc   *api.Healthcheck
	}{
		{"absent", nil},
		{"disabled", &api.Healthcheck{Test: []string{"NONE"}}},
		{"empty test", &api.Healthcheck{Interval: "5s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthcheckToConfig(tc.hc); got != "" {
				t.Fatalf("recorded %q for %s, want nothing", got, tc.name)
			}
		})
	}
}

// TestServerRestartRearmsProbing is the other half of recording the check:
// storing it is worthless if nothing ever reads it back.
//
// Unlike containerd and bare, this backend has no startup reconcile pass, so the
// re-arm is driven lazily from the read paths — which is enough to arm itself,
// because `service_healthy` converges by POLLING Status. Watch sets `starting`
// the moment an instance is armed, so this observes the arming rather than
// waiting out a 30s probe interval.
//
// The stopped and companion instances are the load-bearing half: a pass that
// armed EVERYTHING would look correct without them, and it would report a
// stopped workload as unhealthy where Stop's own unwatch reports "".
func TestServerRestartRearmsProbing(t *testing.T) {
	f := newFakeConn()
	raw := healthcheckToConfig(&api.Healthcheck{Test: []string{"CMD", "true"}})
	seed := func(name string, status incusapi.StatusCode, extra map[string]string) {
		cfg := map[string]string{
			configKeyPrefix + "cornus.managed": "true",
			configKeyPrefix + "cornus.app":     "app",
			healthcheckConfigKey:               raw,
		}
		for k, v := range extra {
			cfg[k] = v
		}
		f.insts[name] = &incusapi.Instance{
			Name:            name,
			InstancePut:     incusapi.InstancePut{Config: cfg},
			StatusCode:      status,
			ExpandedConfig:  cfg,
			ExpandedDevices: map[string]map[string]string{},
		}
	}
	seed("cornus-app-0", incusapi.Running, nil)
	seed("cornus-app-1", incusapi.Stopped, nil)
	seed("cornus-app-relay-0", incusapi.Running, map[string]string{companionRoleKey: "port-forward"})

	// A backend as a restarted cornus builds one: the daemon still holds the
	// instances, and the engine knows nothing about them.
	b := testBackend(f)
	b.health = healthengine.New(func(context.Context, string, []string) (int, error) { return 0, nil })
	t.Cleanup(b.health.StopAll)

	if _, err := b.Status(context.Background(), "app"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := b.health.State("cornus-app-0"); got != healthengine.StateStarting {
		t.Fatalf("health of the running instance after restart = %q, want %q: the probe was not "+
			"re-armed, so this workload would report no health until someone redeployed it",
			got, healthengine.StateStarting)
	}
	if got := b.health.State("cornus-app-1"); got != "" {
		t.Fatalf("health of the STOPPED instance after restart = %q, want empty: a restart must not "+
			"arm a probe that Stop disarmed, or the instance reports unhealthy for being stopped", got)
	}
	if got := b.health.State("cornus-app-relay-0"); got != "" {
		t.Fatalf("health of the COMPANION after restart = %q, want empty: a caretaker is not a "+
			"replica of the workload and carries no healthcheck of its own", got)
	}
}

// TestRearmDoesNotResetALiveVerdict: the pass is triggered by a READ, so it can
// run after an Apply in the same process. Watch restarts the loop from
// `starting`, which would throw away a healthy verdict this server had already
// earned — and a compose `depends_on: service_healthy` polls Status, so it would
// be discarding exactly the answer it is waiting for.
func TestRearmDoesNotResetALiveVerdict(t *testing.T) {
	f := newFakeConn()
	f.insts["cornus-app-0"] = &incusapi.Instance{
		Name: "cornus-app-0",
		InstancePut: incusapi.InstancePut{Config: map[string]string{
			configKeyPrefix + "cornus.managed": "true",
			configKeyPrefix + "cornus.app":     "app",
			// Milliseconds, so a real probe reaches a verdict inside the test rather
			// than needing a hook into the engine to fake one.
			healthcheckConfigKey: healthcheckToConfig(&api.Healthcheck{Test: []string{"CMD", "true"}, Interval: "1ms"}),
		}},
		StatusCode: incusapi.Running,
	}
	b := testBackend(f)
	b.health = healthengine.New(func(context.Context, string, []string) (int, error) { return 0, nil })
	t.Cleanup(b.health.StopAll)

	b.syncHealth(context.Background(), "app") // as Apply does
	deadline := time.Now().Add(5 * time.Second)
	for b.health.State("cornus-app-0") != healthengine.StateHealthy {
		if time.Now().After(deadline) {
			t.Fatalf("the probe never reported healthy (state %q); the rest of this test proves nothing",
				b.health.State("cornus-app-0"))
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := b.Status(context.Background(), "app"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := b.health.State("cornus-app-0"); got != healthengine.StateHealthy {
		t.Fatalf("health after the lazy re-arm ran = %q, want %q: the pass restarted a loop that "+
			"was already running and discarded its verdict", got, healthengine.StateHealthy)
	}
}

func TestHealthcheckFromConfigSurvivesGarbage(t *testing.T) {
	if hc := healthcheckFromConfig(map[string]string{healthcheckConfigKey: "{not json"}); hc != nil {
		t.Fatalf("garbage config produced %+v, want nil", hc)
	}
	if hc := healthcheckFromConfig(nil); hc != nil {
		t.Fatalf("absent key produced %+v, want nil", hc)
	}
}
