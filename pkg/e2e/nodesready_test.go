package e2e

import "testing"

// TestNodesReady pins the predicate behind the kube target's post-create wait.
// The case that matters most is the EMPTY one: the wait runs immediately after
// `kind create cluster`, when the node list can still be unpopulated, and
// treating "" as ready would reintroduce exactly the race the wait exists to
// close (`kubectl wait --all` has that bug, which is why this polls instead).
func TestNodesReady(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"no nodes yet", "", false},
		{"whitespace only", "   \n", false},
		{"single ready node", "True", true},
		{"multi-node all ready", "True True True", true},
		{"one node not ready", "True False", false},
		{"sole node not ready", "False", false},
		{"unknown condition", "Unknown", false},
		{"ready plus unknown", "True Unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodesReady(tc.out); got != tc.want {
				t.Fatalf("nodesReady(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}
