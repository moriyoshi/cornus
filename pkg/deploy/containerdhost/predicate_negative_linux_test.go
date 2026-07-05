package containerdhost

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// TestWarnUnsupportedHonoursTheCanonicalPredicates is the containerd half of the
// same guard barehost carries; see that file for the full reasoning.
//
// The short version: every existing coverage test asks "does setting field X warn?",
// which a bare-nil check answers exactly as well as the canonical predicate does.
// The difference only shows on a spec where the field is PRESENT and silence is the
// right answer — an empty ingress block, a client-emulated ingress, or a healthcheck
// the user explicitly disabled. Reverting this backend to its hand-rolled predicates
// left the suite green.
func TestWarnUnsupportedHonoursTheCanonicalPredicates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    api.DeploySpec
		quiet   string
		because string
	}{
		{
			name:    "empty ingress block asks for nothing",
			spec:    api.DeploySpec{Ingress: &api.IngressSpec{}},
			quiet:   "Ingress",
			because: "an empty x-cornus-ingress: {} requests no ingress",
		},
		{
			name:  "client-emulated ingress is realized on the client",
			spec:  api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true, ClientEmulated: true}},
			quiet: "Ingress",
			because: "a client-emulated ingress IS being served through the conduit; warning that the " +
				"backend creates none reads as 'your ingress was dropped'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			warnUnsupported(context.Background(), log, tc.spec)
			for _, line := range warnLines(buf.String()) {
				if strings.Contains(line, tc.quiet) {
					t.Errorf("warned about %q for a spec that does not ask for it: %s\n%s",
						tc.quiet, line, tc.because)
				}
			}
		})
	}
}

// TestWarnUnsupportedStillWarnsWhenItShould is the positive control: silence is
// trivially achievable by deleting the branches, so a negative-only test would
// reward removing the feature it guards.
func TestWarnUnsupportedStillWarnsWhenItShould(t *testing.T) {
	// Healthcheck used to be here, on both sides. It is no longer a field this
	// backend cannot honor: cornus runs the probes itself (health_linux.go), so
	// there is no warning left to assert. The replacement contract — that the
	// healthcheck is RECORDED on the container — is pinned by
	// TestContainerLabelsRecordTheHealthcheck.
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
		want string
	}{
		{"real ingress", api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true}}, "Ingress"},
		{"ingress by host", api.DeploySpec{Ingress: &api.IngressSpec{Hosts: []string{"app.example"}}}, "Ingress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			warnUnsupported(context.Background(), log, tc.spec)
			var found bool
			for _, line := range warnLines(buf.String()) {
				if strings.Contains(line, tc.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("no warning mentioning %q for a spec that DOES ask for it; the branch is gone and "+
					"the negative test above would pass on its absence", tc.want)
			}
		})
	}
}
