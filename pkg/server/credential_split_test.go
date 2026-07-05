package server

import (
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// specWith builds a one-source spec carrying the given deliveries.
func specWith(ds ...api.CredentialDelivery) api.DeploySpec {
	return api.DeploySpec{
		Name: "app",
		Credentials: &api.CredentialSpec{
			Sources: []api.CredentialSource{{Name: "db", Deliveries: ds}},
		},
	}
}

// TestCoLocatedSplitLeavesNothingForACaretaker is the integration the per-piece
// tests missed.
//
// The co-located path materializes files and serves endpoints ITSELF, then asks
// buildAttachCredentials to split the same source again and asserts that nothing
// runtime-bound survives. Those two steps have to be told the same capability.
// If the split is told less than the path actually did, a delivery the server
// already realized is re-classified as caretaker-bound and the deploy fails on an
// "internal:" guard — with the credential in fact correctly in place, which is
// the most confusing way for it to go wrong.
//
// This is checked at the SPLIT rather than through applyWithHostAttachments
// because the guard reads exactly this: CredentialRuntimeKinds over the split's
// output.
func TestCoLocatedSplitLeavesNothingForACaretaker(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
		can  deploy.ServerDelivers
	}{
		{
			name: "file the server materialized",
			spec: specWith(api.CredentialDelivery{Kind: "file", Path: "/creds/db.json"}),
			can:  deploy.ServerDelivers{Files: true},
		},
		{
			name: "endpoint the server serves",
			spec: specWith(api.CredentialDelivery{Kind: "endpoint"}),
			can:  deploy.ServerDelivers{Endpoints: true},
		},
		{
			name: "endpoint spelled by omission",
			spec: specWith(api.CredentialDelivery{}),
			can:  deploy.ServerDelivers{Endpoints: true},
		},
		{
			name: "both at once",
			spec: specWith(
				api.CredentialDelivery{Kind: "file", Path: "/creds/db.json"},
				api.CredentialDelivery{Kind: "endpoint"},
			),
			can: deploy.ServerDelivers{Files: true, Endpoints: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The dispatch must actually route this deploy to the co-located
			// path, or the rest of the case is about a path it never takes.
			if kinds := deploy.SpecCaretakerKinds(tc.spec, tc.can); len(kinds) > 0 {
				t.Fatalf("precondition: the dispatch would send this to a caretaker (%v), not the co-located path", kinds)
			}
			// Nil session is safe: no env delivery, so nothing is fetched.
			out, err := realizeCoLocatedCredentials(nil, tc.spec, tc.can)
			if err != nil {
				t.Fatalf("the co-located path realized these deliveries itself, yet finishing the "+
					"deploy failed: %v\n(the credential is correctly in place; only the bookkeeping "+
					"disagrees about who delivered it)", err)
			}
			if out.Credentials != nil {
				t.Fatal("the credential block must be cleared once realized, or every backend logs " +
					"that it ignored credentials it in fact delivered")
			}
		})
	}
}

// TestSplitWithoutCapabilityKeepsDeliveriesForACaretaker is the other direction,
// and it is what stops the fix above from becoming "drop everything": a backend
// that CANNOT place files or bind endpoints must still route them to a caretaker
// rather than silently discard them.
func TestSplitWithoutCapabilityKeepsDeliveriesForACaretaker(t *testing.T) {
	spec := specWith(
		api.CredentialDelivery{Kind: "file", Path: "/creds/db.json"},
		api.CredentialDelivery{Kind: "endpoint"},
	)
	if _, err := realizeCoLocatedCredentials(nil, spec, deploy.ServerDelivers{}); err == nil {
		t.Fatal("with no server capability the co-located path must REFUSE these deliveries; " +
			"succeeding would mean the workload comes up healthy with no credential at all")
	}
	creds, err := buildAttachCredentials(nil, spec, "sess", "http://relay", "img", deploy.ServerDelivers{})
	if err != nil {
		t.Fatalf("buildAttachCredentials: %v", err)
	}
	got := strings.Join(deploy.CredentialRuntimeKinds(creds), "/")
	if got != "endpoint/file" {
		t.Fatalf("with no server capability both deliveries must stay caretaker-bound, got %q", got)
	}
}
