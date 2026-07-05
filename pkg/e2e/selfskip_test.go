package e2e

import (
	"io"
	"testing"

	"go.starlark.net/starlark"
)

// TestIsSelfSkipMatchesTheConventionAndNothingElse pins the pattern the runner's
// skip reporting rests on.
//
// The convention is "<label>: skipped (<reason>)" — 168 occurrences across
// e2e/scenarios as of 2026-08-06. Both directions of the test matter:
//
//   - miss a real skip and the runner reports "all N passed" over a scenario that
//     never ran, which is the bug this reporting exists to prevent;
//   - match a PARTIAL skip and a scenario that did most of its work is reported as
//     having possibly exercised nothing, which under-reports real coverage. That is
//     the opposite error, and just as misleading.
func TestIsSelfSkipMatchesTheConventionAndNothingElse(t *testing.T) {
	for _, msg := range []string{
		"server-in-container-containerd: skipped (containerd-only)",
		"activity-follow: skipped (docker-only; needs a live deploy to record work)",
		"registry-s3: skipped (set CORNUS_S3_ENDPOINT to run against a live S3)",
	} {
		if !isSelfSkip(msg) {
			t.Errorf("isSelfSkip(%q) = false, want true — a whole-scenario skip must be reported", msg)
		}
	}
	for _, msg := range []string{
		// Verbatim from dockerd-exit-code.star:67, which SKIPS ONE ARM and
		// keeps going, so counting it as skipped would understate what it proved.
		"! curl absent: skipping the raw wait-body assertions (the docker CLI assertions still ran)",
		"✓ port-forward reached an unpublished container port end to end",
		"deploying nginx and skipping nothing at all",
		"",
	} {
		if isSelfSkip(msg) {
			t.Errorf("isSelfSkip(%q) = true, want false — only a whole-scenario skip may be reported", msg)
		}
	}
}

// TestSkipsRecordsWhatTheScenarioLogged covers the harness side THROUGH bLog, which
// is the only path a scenario can reach.
//
// Driving bLog rather than appending to h.skips is the whole point: an earlier version
// of this test set the field directly, so deleting the recording from bLog left it
// passing — it asserted that a slice I had filled contained what I put in it. The
// wiring is the behaviour under test, not the accessor.
func TestSkipsRecordsWhatTheScenarioLogged(t *testing.T) {
	h := &Harness{out: io.Discard}
	if got := h.Skips(); len(got) != 0 {
		t.Fatalf("a fresh harness reports %v, want no skips", got)
	}
	for _, msg := range []string{
		"one: skipped (no image)",
		"✓ did something",
		"two: skipped (wrong target)",
	} {
		if _, err := h.bLog(nil, nil, starlark.Tuple{starlark.String(msg)}, nil); err != nil {
			t.Fatalf("log(%q): %v", msg, err)
		}
	}
	got := h.Skips()
	if len(got) != 2 || got[0] != "one: skipped (no image)" || got[1] != "two: skipped (wrong target)" {
		t.Errorf("Skips() = %v, want both skip messages in order and nothing else", got)
	}
}
