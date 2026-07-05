//go:build linux

package barehost

import (
	"encoding/json"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/otelcollector"
)

// TestCaretakerConfigWire guards the barehost-local caretaker config structs
// (companion_linux.go) against drift from cornus/pkg/caretaker's wire contract.
// barehost emits the companion's CORNUS_CARETAKER_CONFIG via its own structs
// instead of importing pkg/caretaker (whose runtime transitively links
// github.com/moby/buildkit, which barehost must not — see the package doc). This
// TEST may import caretaker because test-only deps do not count toward the binary
// (`go list -deps` excludes them), so the leanness invariant holds while the
// contract stays verified: if a caretaker.Config JSON tag changes, this fails.
func TestCaretakerConfigWire(t *testing.T) {
	rules := []api.EgressRule{{Pattern: "*.internal", Route: "cluster"}, {Pattern: "*", Route: "client"}}

	otel := otelcollector.Config{
		GRPCEndpoint: "127.0.0.1:4317", HTTPEndpoint: "127.0.0.1:4318",
		ExporterEndpoint: "otel-backend:4317", ExporterProtocol: "grpc",
		ExporterHeaders: map[string]string{"authorization": "Bearer x"},
		Signals:         []string{"traces", "metrics"},
	}
	local := caretakerConfig{
		Mounts: []caretakerMountRole{{Server: "https://srv", Session: "sess-1", Name: "m0", Target: "/cornus/mounts/0", ReadOnly: true}},
		Egress: &caretakerEgressRole{
			Server: "https://srv", Session: "sess-2", Mode: "transparent", ListenPort: 15002,
			Rules: rules, Script: "function FindProxyForURL(){}", Default: "deny", SetupRedirect: true,
		},
		Otel:  &otel,
		Mark:  15002,
		Token: "bearer-tok",
	}
	upstreamOtel := caretaker.OtelRole(otel)
	upstream := caretaker.Config{
		Mounts: []caretaker.MountRole{{Server: "https://srv", Session: "sess-1", Name: "m0", Target: "/cornus/mounts/0", ReadOnly: true}},
		Egress: &caretaker.EgressRole{
			Server: "https://srv", Session: "sess-2", Mode: "transparent", ListenPort: 15002,
			Rules: rules, Script: "function FindProxyForURL(){}", Default: "deny", SetupRedirect: true,
		},
		Otel:  &upstreamOtel,
		Mark:  15002,
		Token: "bearer-tok",
	}

	lb, err := json.Marshal(local)
	if err != nil {
		t.Fatalf("marshal local: %v", err)
	}
	ub, err := json.Marshal(upstream)
	if err != nil {
		t.Fatalf("marshal caretaker.Config: %v", err)
	}
	if string(lb) != string(ub) {
		t.Errorf("caretaker config wire drift:\n barehost-local = %s\n caretaker.Config = %s", lb, ub)
	}

	// The emitted JSON must also parse cleanly back into the real caretaker.Config
	// (the companion consumes it as caretaker.Config), preserving every value.
	var round caretaker.Config
	if err := json.Unmarshal(lb, &round); err != nil {
		t.Fatalf("caretaker.Config cannot parse barehost-emitted config: %v", err)
	}
	if round.Mark != local.Mark || round.Token != local.Token ||
		round.Egress == nil || round.Egress.Mode != local.Egress.Mode || !round.Egress.SetupRedirect ||
		len(round.Mounts) != 1 || round.Mounts[0].Target != local.Mounts[0].Target || !round.Mounts[0].ReadOnly {
		t.Errorf("round-trip lost fields: %+v", round)
	}
}
