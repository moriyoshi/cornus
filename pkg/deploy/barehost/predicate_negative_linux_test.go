package barehost

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// TestWarnUnsupportedHonoursTheCanonicalPredicates covers the NEGATIVE side of the
// warn prelude, which nothing did.
//
// Every existing coverage test asks "does setting field X produce a warning?".
// That is satisfied by a bare-nil check (`spec.Ingress != nil`) exactly as well as
// by the canonical predicate (`ingressroute.Enabled`), so reverting this backend to
// the hand-rolled version it used to carry left the whole suite green — measured,
// not assumed. What separates the two is the three specs below, where the field is
// PRESENT and the correct answer is silence:
//
//   - An empty ingress block. Compose's `x-cornus-ingress: {}` produces one, and it
//     asks for nothing.
//   - A CLIENT-EMULATED ingress. This is the expensive one: the ingress is real and
//     is being served, on the client, through the conduit. Warning "this backend
//     creates no cluster Ingress" tells the operator their working ingress was
//     dropped.
//   - An explicitly DISABLED healthcheck (`test: ["NONE"]`, the Compose spelling for
//     "do not health-check this"). Warning that the backend ignores a healthcheck
//     the user deliberately turned off is noise about a decision they already made.
//
// The rule these serve is in .agents/docs/LTM/deploy-backend-contract.md: warn
// per-field, never a silent drop. A warning for something that was not dropped is
// the same rule failing from the other side — it trains operators to ignore the
// warnings that do matter.
func TestWarnUnsupportedHonoursTheCanonicalPredicates(t *testing.T) {
	cases := []struct {
		name    string
		spec    api.DeploySpec
		quiet   string // substring that must NOT appear
		because string
	}{
		{
			name:    "empty ingress block asks for nothing",
			spec:    api.DeploySpec{Ingress: &api.IngressSpec{}},
			quiet:   "Ingress",
			because: "an empty x-cornus-ingress: {} requests no ingress; ingressroute.Enabled is false for it",
		},
		{
			name:  "client-emulated ingress is realized on the client",
			spec:  api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true, ClientEmulated: true}},
			quiet: "Ingress",
			because: "a client-emulated ingress IS being served, through the conduit; saying the backend " +
				"creates none reads as 'your ingress was dropped'",
		},
		{
			name:  "explicitly disabled healthcheck",
			spec:  api.DeploySpec{Healthcheck: &api.Healthcheck{Test: []string{"NONE"}}},
			quiet: "healthcheck",
			because: "test: [NONE] is the Compose spelling for 'do not health-check this'; there is nothing " +
				"left for the backend to ignore",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			t.Cleanup(func() { slog.SetDefault(prev) })
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			warnUnsupported(context.Background(), log, tc.spec)

			for _, line := range warnLines(buf.String()) {
				if strings.Contains(line, tc.quiet) {
					t.Errorf("warned about %q for a spec that does not ask for it: %s\n%s",
						tc.quiet, trunc(line), tc.because)
				}
			}
		})
	}
}

// TestWarnUnsupportedStillWarnsWhenItShould is the positive control for the test
// above. Without it, deleting the two branches entirely would satisfy every
// assertion here — silence is trivially achievable, and a negative-only test
// rewards removing the feature.
func TestWarnUnsupportedStillWarnsWhenItShould(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
		want string
	}{
		{"real ingress", api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true}}, "Ingress"},
		{"ingress by host", api.DeploySpec{Ingress: &api.IngressSpec{Hosts: []string{"app.example"}}}, "Ingress"},
		{"real healthcheck", api.DeploySpec{Healthcheck: &api.Healthcheck{Test: []string{"CMD", "true"}}}, "healthcheck"},
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
				t.Errorf("no warning mentioning %q for a spec that DOES ask for it; the branch is gone, "+
					"and the negative test above would pass on its absence", tc.want)
			}
		})
	}
}
