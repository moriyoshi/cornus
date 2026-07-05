//go:build linux

package incushost

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// TestWarnUnsupportedUsesCanonicalPredicates pins the three predicates this
// prelude got wrong. It guarded on bare `spec.Healthcheck != nil`,
// `spec.Ingress != nil`, and `spec.Knative != nil`, so a spec that explicitly
// DISABLED a healthcheck, requested a CLIENT-EMULATED ingress, or carried a
// knative block that was not enabled drew a warning saying the backend would
// ignore something the caller had never asked it to do. The other host backends
// test `!hc.Disabled()`, ingressroute.Enabled, and `Knative.Enabled`; this now
// matches them.
//
// Warnings are the operator's only view of what a backend silently drops, so a
// false one is not free: it trains people to ignore the true ones.
func TestWarnUnsupportedUsesCanonicalPredicates(t *testing.T) {
	warn := func(t *testing.T, spec api.DeploySpec) string {
		t.Helper()
		buf := captureLogs(t)
		b := testBackend(newFakeConn())
		b.warnUnsupported(context.Background(), slog.Default(), spec)
		return buf.String()
	}

	base := api.DeploySpec{Name: "web", Image: "img"}

	t.Run("asks for nothing unsupported", func(t *testing.T) {
		spec := base
		spec.Healthcheck = &api.Healthcheck{Test: []string{"NONE"}} // compose `healthcheck.disable: true`
		spec.Ingress = &api.IngressSpec{Enabled: true, ClientEmulated: true}
		spec.Knative = &api.KnativeSpec{}
		out := warn(t, spec)
		for _, unwanted := range []string{"healthcheck", "Ingress", "knative"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("warned about %q for a spec that does not request it from this backend:\n%s", unwanted, out)
			}
		}
	})

	t.Run("does ask for them", func(t *testing.T) {
		spec := base
		spec.Healthcheck = &api.Healthcheck{Test: []string{"CMD", "true"}}
		spec.Ingress = &api.IngressSpec{Enabled: true}
		spec.Knative = &api.KnativeSpec{Enabled: true}
		out := warn(t, spec)
		for _, want := range []string{"healthcheck", "Ingress", "knative"} {
			if !strings.Contains(out, want) {
				t.Errorf("did NOT warn about %q, which this backend genuinely cannot honor:\n%s", want, out)
			}
		}
	})
}
