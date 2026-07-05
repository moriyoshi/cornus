//go:build linux

package containerdhost

import (
	"encoding/json"
	"testing"

	"cornus/pkg/api"
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
