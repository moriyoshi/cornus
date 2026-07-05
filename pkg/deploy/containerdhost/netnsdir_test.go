package containerdhost

import (
	"testing"

	"cornus/pkg/deploy/internal/hostrun"
)

// TestNetnsDirConstantsAgree pins the one thing neither constant can check about
// itself: that they are the same path.
//
// hostrun.NetnsDir is where the CNI manager CREATES the pins. containerdhost.NetnsDir
// is what the startup preflight checks the runtime can SEE (pkg/hostcheck cannot
// import the internal package, so the value is re-exported and handed over). Each
// spelling is perfectly defensible on its own, and nothing else in the tree
// compares them — so if they ever drift, the preflight cheerfully verifies a
// directory the backend does not use, reports OK, and every deploy fails anyway.
//
// This is the shape of failure the repo has been bitten by before: two sides
// individually correct, where only their AGREEMENT was the contract.
func TestNetnsDirConstantsAgree(t *testing.T) {
	if NetnsDir != hostrun.NetnsDir {
		t.Fatalf("containerdhost.NetnsDir = %q but hostrun.NetnsDir = %q: the preflight would check a directory the backend does not pin into",
			NetnsDir, hostrun.NetnsDir)
	}
	// And it must be an absolute path outside the data dir, which is the whole
	// reason it needs its own check rather than riding the data-dir bind.
	if NetnsDir == "" || NetnsDir[0] != '/' {
		t.Fatalf("NetnsDir = %q, want an absolute path", NetnsDir)
	}
}
