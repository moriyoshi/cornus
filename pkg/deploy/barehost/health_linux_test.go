//go:build linux

package barehost

import (
	"testing"

	"cornus/pkg/api"
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
