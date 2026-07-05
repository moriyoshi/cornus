package main

import (
	"errors"
	"testing"

	"cornus/cmd/cornus/internal/execdrive"
	"cornus/pkg/api"
)

// TestExecExitCode confirms that a failed ExecInspect never yields a success
// (0) exit code: a non-nil error maps to the distinct execdrive.InspectFailCode
// so a transient inspect failure cannot mask a command that actually failed,
// while a successful inspect propagates the reported ExitCode verbatim. The
// mapping now lives in the shared execdrive package (used by both `cornus exec`
// and `cornus compose exec`).
func TestExecExitCode(t *testing.T) {
	// Inspect failed: the reported ExitCode is the zero value, but we must not
	// trust it — expect the distinct failure code, not 0.
	if got := execdrive.ExitCode(api.ExecState{ExitCode: 0}, errors.New("502 bad gateway")); got != execdrive.InspectFailCode {
		t.Errorf("ExitCode(err) = %d, want %d", got, execdrive.InspectFailCode)
	}
	// Even if a failed inspect somehow carried a code, the error still wins.
	if got := execdrive.ExitCode(api.ExecState{ExitCode: 3}, errors.New("boom")); got != execdrive.InspectFailCode {
		t.Errorf("ExitCode(err, code=3) = %d, want %d", got, execdrive.InspectFailCode)
	}
	// Successful inspect: propagate the real exit code.
	if got := execdrive.ExitCode(api.ExecState{ExitCode: 0}, nil); got != 0 {
		t.Errorf("ExitCode(ok, 0) = %d, want 0", got)
	}
	if got := execdrive.ExitCode(api.ExecState{ExitCode: 3}, nil); got != 3 {
		t.Errorf("ExitCode(ok, 3) = %d, want 3", got)
	}
}
