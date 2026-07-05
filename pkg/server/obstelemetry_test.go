package server

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
)

func telemetrySpec(t *api.TelemetrySpec) *api.DeploySpec {
	return &api.DeploySpec{Name: "web", Image: "web:latest", Telemetry: t}
}

// TestNormalizeTelemetryDefaultsToTheStore is the payoff of the whole endpoint
// change: `x-cornus-telemetry: {}` with no URL anywhere becomes a concrete export
// to this server, so a user gets traces and metrics without running a backend.
func TestNormalizeTelemetryDefaultsToTheStore(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")

	want := "https://cornus.internal:5000" + otlpReceiverPath
	if spec.Telemetry.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", spec.Telemetry.Endpoint, want)
	}
	// The receiver is HTTP-only, so the protocol must be pinned rather than left
	// at the spec default of grpc — which would wire the app to a port nothing
	// is listening on.
	if spec.Telemetry.Protocol != "http/protobuf" {
		t.Errorf("protocol = %q, want http/protobuf", spec.Telemetry.Protocol)
	}
	if spec.Telemetry.Insecure {
		t.Error("an https advertise URL must not be marked insecure")
	}
}

// TestNormalizeTelemetryMarksPlaintextInsecure: exporting to an http:// endpoint
// while claiming TLS produces a handshake failure in a sidecar nobody is watching.
func TestNormalizeTelemetryMarksPlaintextInsecure(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "http://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")

	if !spec.Telemetry.Insecure {
		t.Error("a plaintext advertise URL must set Insecure so the exporter does not attempt TLS")
	}
}

// TestNormalizeTelemetryLeavesAnExplicitEndpoint is the non-regression guard:
// users who already export to Grafana or Datadog must be entirely unaffected.
func TestNormalizeTelemetryLeavesAnExplicitEndpoint(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Endpoint: "otel.example.com:4317", Protocol: "grpc"})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")

	if spec.Telemetry.Endpoint != "otel.example.com:4317" {
		t.Errorf("endpoint was rewritten to %q", spec.Telemetry.Endpoint)
	}
	if spec.Telemetry.Protocol != "grpc" {
		t.Errorf("protocol was rewritten to %q", spec.Telemetry.Protocol)
	}
}

// TestNormalizeTelemetryWithoutAStoreLeavesItAlone: with nothing to default to,
// the spec must pass through untouched so BuildTelemetryWiring produces the one
// error message that names both remedies, rather than this layer inventing a
// second one that disagrees.
func TestNormalizeTelemetryWithoutAStoreLeavesItAlone(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{} // no store

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")

	if spec.Telemetry.Endpoint != "" {
		t.Errorf("endpoint = %q, want empty when there is no store", spec.Telemetry.Endpoint)
	}
}

// TestNormalizeTelemetryNeedsAnAdvertiseURL: a listen address is not an address
// a workload can reach, so guessing one would produce wiring that fails at
// runtime instead of at deploy time.
func TestNormalizeTelemetryNeedsAnAdvertiseURL(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")

	if spec.Telemetry.Endpoint != "" {
		t.Errorf("endpoint = %q, want empty with no advertise URL", spec.Telemetry.Endpoint)
	}
}

// TestNormalizeTelemetryIgnoresInactiveSpecs keeps the common path untouched.
func TestNormalizeTelemetryIgnoresInactiveSpecs(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(nil)
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")
	if spec.Telemetry != nil {
		t.Errorf("a nil telemetry spec was populated: %+v", spec.Telemetry)
	}

	spec = telemetrySpec(&api.TelemetrySpec{})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")
	if spec.Telemetry.Endpoint != "" {
		t.Errorf("an inactive telemetry spec was given an endpoint: %q", spec.Telemetry.Endpoint)
	}
}

// TestNormalizeTelemetryTrimsTrailingSlash keeps the composed OTLP path from
// becoming a double slash the exporter would send verbatim.
func TestNormalizeTelemetryTrimsTrailingSlash(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000/")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "dockerhost")

	if strings.Contains(spec.Telemetry.Endpoint, "//.cornus") {
		t.Errorf("endpoint has a doubled slash: %q", spec.Telemetry.Endpoint)
	}
}

// --- the mux default -------------------------------------------------------

func muxOf(t *api.TelemetrySpec) string {
	if t == nil || t.ViaMux == nil {
		return "undecided"
	}
	if *t.ViaMux {
		return "on"
	}
	return "off"
}

// TestMuxDefaultsOnWhenCornusIsTheDestination is the default itself: on
// kubernetes, with cornus as the destination and no user preference, telemetry
// rides the caretaker connection. That path needs no reachable URL, no route from
// the pod, and no credential, so it is strictly the better one whenever it
// applies.
func TestMuxDefaultsOnWhenCornusIsTheDestination(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")

	if got := muxOf(spec.Telemetry); got != "on" {
		t.Errorf("viaMux = %s, want on by default", got)
	}
}

// TestMuxDefaultOffWithoutTelemetry keeps the decision confined to workloads that
// actually asked for telemetry.
func TestMuxDefaultOffWithoutTelemetry(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}
	spec := telemetrySpec(nil)
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")
	if spec.Telemetry != nil {
		t.Errorf("a workload with no telemetry gained a spec: %+v", spec.Telemetry)
	}
}

// TestMuxDefaultRespectsAnExplicitChoice: the tri-state exists so that an
// explicit false survives the default. Collapsing it to a plain bool would make
// "I do not want this" indistinguishable from "I did not say".
func TestMuxDefaultRespectsAnExplicitChoice(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	off := false
	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true, ViaMux: &off})
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")
	if got := muxOf(spec.Telemetry); got != "off" {
		t.Errorf("viaMux = %s, want the explicit off to survive", got)
	}

	on := true
	spec = telemetrySpec(&api.TelemetrySpec{Enabled: true, ViaMux: &on})
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")
	if got := muxOf(spec.Telemetry); got != "on" {
		t.Errorf("viaMux = %s, want the explicit on to survive", got)
	}
}

// TestMuxDefaultOffForAThirdPartyEndpoint: there is no caretaker connection to
// Datadog, so riding the mux cannot deliver there. Defaulting it on would break
// every external-backend deploy.
func TestMuxDefaultOffForAThirdPartyEndpoint(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Endpoint: "otlp.datadoghq.com:4317"})
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")

	if got := muxOf(spec.Telemetry); got != "undecided" {
		t.Errorf("viaMux = %s for a third-party endpoint, want undecided (direct dial)", got)
	}
}

// TestMuxDefaultsOnForEveryTelemetryBackend: the mux is not a Kubernetes feature.
// Every backend that runs a telemetry caretaker can carry exports over it, and the
// problem it solves — the workload's network having no route to the server — shows
// up on a remote docker host and an isolated container network just as squarely.
func TestMuxDefaultsOnForEveryTelemetryBackend(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	for _, backend := range []string{"kubernetes", "dockerhost", "containerd", "bare"} {
		s := &Server{obs: &queryStore{}}
		spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
		s.normalizeTelemetry(context.Background(), spec, backend)
		if got := muxOf(spec.Telemetry); got != "on" {
			t.Errorf("%s: viaMux = %s, want on by default", backend, got)
		}
	}
}

// TestMuxDefaultOffWithoutAnAdvertiseURL: with no advertised URL the endpoint is
// never defaulted either, so there is no cornus destination to ride the mux to.
func TestMuxDefaultOffWithoutAnAdvertiseURL(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")

	if got := muxOf(spec.Telemetry); got != "undecided" {
		t.Errorf("viaMux = %s with no advertise URL, want undecided", got)
	}
}

// TestTelemetryTargetsSelf pins the comparison the default turns on. A near-miss
// that answered "yes" would wire the mux toward a backend it cannot reach.
func TestTelemetryTargetsSelf(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{}
	cases := map[string]bool{
		"https://cornus.internal:5000/.cornus/v1/otlp":  true,
		"https://cornus.internal:5000/.cornus/v1/otlp/": true,
		"https://cornus.internal:5000":                  false, // the base URL is not the receiver
		"https://elsewhere.example/.cornus/v1/otlp":     false,
		"otlp.datadoghq.com:4317":                       false,
		"":                                              false,
	}
	for endpoint, want := range cases {
		if got := s.telemetryTargetsSelf(endpoint); got != want {
			t.Errorf("telemetryTargetsSelf(%q) = %v, want %v", endpoint, got, want)
		}
	}
}

// TestMuxDefaultAppliesToADefaultedEndpoint is the end-to-end shape of the
// default: a bare `x-cornus-telemetry: {}` on kubernetes comes out with BOTH the
// endpoint filled in and the mux chosen, in one pass.
func TestMuxDefaultAppliesToADefaultedEndpoint(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "https://cornus.internal:5000")
	s := &Server{obs: &queryStore{}}

	spec := telemetrySpec(&api.TelemetrySpec{Enabled: true})
	s.normalizeTelemetry(context.Background(), spec, "kubernetes")

	if spec.Telemetry.Endpoint == "" {
		t.Fatal("endpoint was not defaulted")
	}
	if !spec.Telemetry.UsesMux() {
		t.Error("a bare telemetry block on kubernetes did not default to the mux")
	}
}
