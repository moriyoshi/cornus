//go:build linux

// This file is an EXTERNAL test package (deploy_test, not deploy) because it
// imports the backend packages, and every one of them imports pkg/deploy. An
// in-package test would be an import cycle.
package deploy_test

import (
	"testing"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/barehost"
	"cornus/pkg/deploy/containerdhost"
	"cornus/pkg/deploy/dockerhost"
	"cornus/pkg/deploy/incushost"
	"cornus/pkg/deploy/kubernetes"
)

// udpPortForwarder mirrors pkg/server's unexported optional-capability interface.
// It is restated here rather than exported from pkg/server, because the thing
// under test is what the SERVER's type assertion will find, and a copy that
// drifts from it would fail loudly at the assertion below (the backends would
// stop satisfying it) rather than silently.
type udpPortForwarder interface {
	SupportsUDPPortForward() bool
}

// TestUDPForwardCapabilityValues is the second half of the UDP capability guard,
// and it exists because the first half was not enough.
//
// TestEveryUDPForwardingBackendDeclaresIt (udpforward_test.go) parses source and
// checks that a backend with a udp branch DECLARES SupportsUDPPortForward. It
// therefore observes the method's NAME and nothing else. Changing incushost's
// body to `return false` restores the original defect in full — the server's
// `!u.SupportsUDPPortForward()` refuses every udp tunnel again — and that AST
// test stays green. Verified, not assumed: the flip was applied to the tree and
// the guard passed.
//
// That is the failure the repo's own rule warns about, committed by the guard
// written to prevent it. The neutralization I ran deleted the method, which is
// the mutation the AST test was built to catch; the mutation a real regression
// takes is a one-word edit to the return, and it was never tried. A test must be
// broken the way the CODE would break, not the way the test is shaped.
//
// So this half calls the method and reads the answer. Both halves are needed and
// neither subsumes the other: this one cannot see a backend that grows a udp
// branch and never declares the capability (the original incus bug — there is no
// method to call), and the AST one cannot see a declaration that lies.
func TestUDPForwardCapabilityValues(t *testing.T) {
	// Zero values, not constructed backends: every one of these five needs a live
	// daemon (docker, containerd, an OCI runtime, incusd, a cluster) to build for
	// real, and SupportsUDPPortForward answers a static property of the backend
	// TYPE — it must not depend on connection state, or the server could not ask
	// it before dialing anything.
	cases := []struct {
		name    string
		backend deploy.Backend
		want    bool
	}{
		{"dockerhost", &dockerhost.Backend{}, true},
		{"containerdhost", &containerdhost.Backend{}, true},
		{"barehost", &barehost.Backend{}, true},
		{"incushost", &incushost.Backend{}, true},
		// kubernetes must not implement the interface at all. Its
		// pods/portforward subresource carries a TCP stream; there is nothing to
		// frame datagrams over.
		{"kubernetes", &kubernetes.Backend{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, ok := tc.backend.(udpPortForwarder)
			if !tc.want {
				if ok && u.SupportsUDPPortForward() {
					t.Errorf("%s reports UDP port-forward support; the server will ack a udp tunnel it "+
						"then cannot serve, after the client has already begun sending datagrams", tc.name)
				}
				return
			}
			if !ok {
				t.Fatalf("%s does not satisfy the server's udpPortForwarder assertion, so every udp "+
					"port-forward against it is refused as unsupported", tc.name)
			}
			if !u.SupportsUDPPortForward() {
				t.Errorf("%s.SupportsUDPPortForward() = false. The method is present, so the source-level "+
					"guard is satisfied, but the server reads the VALUE: every `cornus port-forward "+
					"...:PORT/udp` against this backend is refused with \"not supported\" while its "+
					"ForwardPort implements udp. This is the defect that shipped, restorable by one word.", tc.name)
			}
		})
	}
}
