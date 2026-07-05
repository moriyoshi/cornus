//go:build linux

package barehost

// Companion caretaker wiring: the env a companion is handed, and the guards that
// run before anything is created. Actually STARTING a companion needs a rootfs
// mount (root) and a pulled agent image (network), so that belongs to the E2E
// harness; the decisions above it do not.

import (
	"strings"
	"testing"

	"cornus/pkg/deploy"
	"cornus/pkg/otelcollector"
)

// TestCompanionEnvNeverMutatesTheCallersMap matters because the extra env is the
// APP's proxy environment: mutating it would leak the caretaker's own config
// into the workload's spec.
func TestCompanionEnvNeverMutatesTheCallersMap(t *testing.T) {
	extra := map[string]string{"HTTP_PROXY": "http://127.0.0.1:15002"}
	env := companionEnv(`{"mark":15002}`, extra)

	if env["CORNUS_CARETAKER_CONFIG"] != `{"mark":15002}` {
		t.Errorf("caretaker config = %q, want the marshalled instruction set", env["CORNUS_CARETAKER_CONFIG"])
	}
	if env["HTTP_PROXY"] != "http://127.0.0.1:15002" {
		t.Errorf("extra env not carried: %v", env)
	}
	if _, leaked := extra["CORNUS_CARETAKER_CONFIG"]; leaked {
		t.Error("companionEnv wrote the caretaker config back into the caller's map")
	}
	if len(extra) != 1 {
		t.Errorf("caller's map was mutated: %v", extra)
	}
	// With no extras it still carries the config alone.
	if got := companionEnv("{}", nil); len(got) != 1 || got["CORNUS_CARETAKER_CONFIG"] != "{}" {
		t.Errorf("companionEnv(nil extras) = %v", got)
	}
}

// TestStartCompanionRequiresTheAppNetns pins the precondition that makes a
// companion a companion: it JOINS the app instance's pinned netns. Without one
// it would silently get a private netns and every role it carries (egress
// redirect, loopback receiver) would act on the wrong network.
func TestStartCompanionRequiresTheAppNetns(t *testing.T) {
	b, _ := newTestBackend(t)
	err := b.startCompanion(t.Context(), companionSpec{
		appName: "web", compID: "cornus-web-egress-0", role: roleEgressCaretaker,
	})
	if err == nil {
		t.Fatal("a companion with no netns to join: want a refusal")
	}
	if !strings.Contains(err.Error(), "netns") {
		t.Errorf("error = %v, want it to name the missing netns", err)
	}
	// It must refuse before creating anything: no record may exist for it.
	if _, err := b.readRecord("cornus-web-egress-0"); err == nil {
		t.Error("a refused companion left a record behind")
	}
}

// TestPlanCompanionIdentityFollowsItsRoles pins the naming rule tooling and the
// E2E assertions match on: a companion carrying mount roles keeps the mount
// caretaker's identity, and an egress-only one the egress caretaker's. The
// refusal below names the id it was about to create, which is how the choice is
// observed without starting anything.
func TestPlanCompanionIdentityFollowsItsRoles(t *testing.T) {
	b, _ := newTestBackend(t)
	cases := []struct {
		name   string
		plan   companionPlan
		wantID string
	}{
		{"egress only", companionPlan{egress: &caretakerEgressRole{Mode: "proxy"}}, "cornus-web-egress-0"},
		{"carries mount roles", companionPlan{roles: []caretakerMountRole{{Name: "m0"}}}, "cornus-web-mount-0"},
		{"both roles keep the mount identity", companionPlan{
			roles:  []caretakerMountRole{{Name: "m0"}},
			egress: &caretakerEgressRole{Mode: "proxy"},
		}, "cornus-web-mount-0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// netnsPath is empty, so startCompanion refuses and names the id.
			err := b.startPlanCompanion(t.Context(), "web", 0, "", pulledImage{}, "cornus:latest", tc.plan)
			if err == nil {
				t.Fatal("want the no-netns refusal")
			}
			if !strings.Contains(err.Error(), tc.wantID) {
				t.Errorf("error = %v, want it to name the companion %q", err, tc.wantID)
			}
		})
	}
}

// TestTelemetryRelayNeedsAnAdvertisedServerURL covers the viaMux precondition:
// the companion dials cornus back to carry the exports, so without an advertised
// URL there is nowhere for it to connect and the deploy must fail here rather
// than leaving a collector exporting into the void.
func TestTelemetryRelayNeedsAnAdvertisedServerURL(t *testing.T) {
	b, _ := newTestBackend(t)
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	err := b.startTelemetryCompanion(t.Context(), "web", 0, "/run/cornus/netns/cornus-web-0",
		pulledImage{}, "cornus:latest",
		otelcollector.Config{GRPCEndpoint: "127.0.0.1:4317", ExporterEndpoint: "127.0.0.1:14317"},
		&deploy.TelemetryRelay{Listen: "127.0.0.1:14317"}, nil)
	if err == nil {
		t.Fatal("telemetry viaMux without an advertised URL: want a refusal")
	}
	if !strings.Contains(err.Error(), "CORNUS_ADVERTISE_URL") {
		t.Errorf("error = %v, want it to name the missing setting", err)
	}
}
