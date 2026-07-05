//go:build linux

package incushost

import (
	"encoding/json"
	"testing"

	"cornus/pkg/api"
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

func TestHealthcheckFromConfigSurvivesGarbage(t *testing.T) {
	if hc := healthcheckFromConfig(map[string]string{healthcheckConfigKey: "{not json"}); hc != nil {
		t.Fatalf("garbage config produced %+v, want nil", hc)
	}
	if hc := healthcheckFromConfig(nil); hc != nil {
		t.Fatalf("absent key produced %+v, want nil", hc)
	}
}
