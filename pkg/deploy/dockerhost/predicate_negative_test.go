package dockerhost

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// warnLinesOf extracts the msg tail of every WARN record, as the host backends'
// warn-coverage tests do.
func warnLinesOf(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		if i := strings.Index(line, "msg="); i >= 0 {
			lines = append(lines, line[i:])
		}
	}
	return lines
}

// TestWarnUnsupportedHonoursTheCanonicalIngressPredicate is the dockerhost half of
// the cross-backend guard (see pkg/deploy/barehost for the full reasoning).
//
// Only the ingress predicate applies here: Docker has a native HEALTHCHECK and this
// backend maps healthcheck rather than dropping it, so there is no healthcheck
// warning to get wrong.
//
// The case that matters most is the client-emulated one. That ingress is real and
// is being served — on the client, through the conduit — so the warning would tell
// an operator whose ingress works that this backend created none. A bare
// `spec.Ingress != nil` check produces exactly that, and every existing test passes
// with it.
func TestWarnUnsupportedHonoursTheCanonicalIngressPredicate(t *testing.T) {
	b := &Backend{}
	for _, tc := range []struct {
		name    string
		spec    api.DeploySpec
		because string
	}{
		{
			name:    "empty ingress block asks for nothing",
			spec:    api.DeploySpec{Ingress: &api.IngressSpec{}},
			because: "an empty x-cornus-ingress: {} requests no ingress",
		},
		{
			name: "client-emulated ingress is realized on the client",
			spec: api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true, ClientEmulated: true}},
			because: "a client-emulated ingress IS being served through the conduit; warning that the " +
				"backend creates none reports a failure to do something nobody asked for",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			b.warnUnsupported(context.Background(), log, tc.spec)
			for _, line := range warnLinesOf(buf.String()) {
				if strings.Contains(line, "Ingress") {
					t.Errorf("warned about an ingress the spec does not request: %s\n%s", line, tc.because)
				}
			}
		})
	}
}

// TestWarnUnsupportedStillWarnsForARealIngress is the positive control for the
// test above: deleting the branch would otherwise satisfy it.
func TestWarnUnsupportedStillWarnsForARealIngress(t *testing.T) {
	b := &Backend{}
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
	}{
		{"enabled", api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true}}},
		{"by host", api.DeploySpec{Ingress: &api.IngressSpec{Hosts: []string{"app.example"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			b.warnUnsupported(context.Background(), log, tc.spec)
			var found bool
			for _, line := range warnLinesOf(buf.String()) {
				if strings.Contains(line, "Ingress") {
					found = true
				}
			}
			if !found {
				t.Error("no Ingress warning for a spec that requests one; the branch is gone and the " +
					"negative test above would pass on its absence")
			}
		})
	}
}
