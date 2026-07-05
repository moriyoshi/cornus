//go:build linux

package incushost

import (
	"context"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// TestFSOpAnswersUnsupportedWithoutAChannel pins the refusal, which is the half
// most easily got wrong: an instance that is stopped, or a daemon that will not
// open the channel, must answer UNSUPPORTED rather than error.
//
// The distinction is the caller's next move. Unsupported means "relay this
// yourself"; an error means the user's copy failed. Returning the latter for a
// stopped instance would turn a working fallback into a visible failure.
func TestFSOpAnswersUnsupportedWithoutAChannel(t *testing.T) {
	b := testBackend(newFakeConn())
	resp, err := b.FSOp(context.Background(), "web", api.FSOpRequest{Op: api.FSOpStat, Path: "/etc"}, nil, nil)
	if err != nil {
		t.Fatalf("FSOp returned a transport error where a positive refusal was required: %v", err)
	}
	if resp.Code != api.FSErrUnsupported {
		t.Fatalf("code = %q, want %q: the caller decides to relay from this code alone",
			resp.Code, api.FSErrUnsupported)
	}
}

// TestBackendDeclaresFSOperator: the capability is a type assertion elsewhere, so
// nothing would fail if it were dropped.
func TestBackendDeclaresFSOperator(t *testing.T) {
	var _ = deployFSOperator(testBackend(newFakeConn()))
}

// deployFSOperator fails to compile if the backend stops implementing the
// capability, which a bare `var _ = ...` in the production file also does — this
// one additionally runs, so the assertion appears in test output rather than only
// in the build.
func deployFSOperator(b *Backend) deploy.FSOperator { return b }
