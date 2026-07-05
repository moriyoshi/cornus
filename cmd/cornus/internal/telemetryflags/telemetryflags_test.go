package telemetryflags

import (
	"testing"

	"cornus/pkg/api"
)

func TestApplyBuildsSpec(t *testing.T) {
	f := Flags{
		Endpoint:    "otel-backend:4317",
		Protocol:    "http/protobuf",
		Headers:     []string{"authorization=Bearer x", "x-tenant=acme"},
		Insecure:    true,
		Signals:     []string{"traces", "logs"},
		ServiceName: "web",
		Debug:       true,
	}
	var spec api.DeploySpec
	if err := f.Apply(&spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	tp := spec.Telemetry
	if tp == nil || !tp.Enabled {
		t.Fatalf("telemetry not enabled: %+v", tp)
	}
	if tp.Endpoint != "otel-backend:4317" || tp.Protocol != "http/protobuf" || !tp.Insecure || !tp.Debug {
		t.Errorf("scalars = %+v", tp)
	}
	if tp.ServiceName != "web" || len(tp.Signals) != 2 || tp.Signals[0] != "traces" {
		t.Errorf("service/signals = %+v", tp)
	}
	if tp.Headers["authorization"] != "Bearer x" || tp.Headers["x-tenant"] != "acme" {
		t.Errorf("headers = %v", tp.Headers)
	}
}

func TestApplyNoFlagsNoop(t *testing.T) {
	var spec api.DeploySpec
	if err := (Flags{}).Apply(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Telemetry != nil {
		t.Fatalf("expected no telemetry, got %+v", spec.Telemetry)
	}
}

func TestApplyFlagsOverrideSpecFile(t *testing.T) {
	spec := api.DeploySpec{Telemetry: &api.TelemetrySpec{Endpoint: "old:4317", Protocol: "grpc"}}
	f := Flags{Endpoint: "new:4317", Protocol: "http/protobuf"}
	if err := f.Apply(&spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if spec.Telemetry.Endpoint != "new:4317" || spec.Telemetry.Protocol != "http/protobuf" {
		t.Errorf("flags should override spec-file values: %+v", spec.Telemetry)
	}
}

func TestApplyRejectsEndpointless(t *testing.T) {
	// A telemetry flag with no endpoint anywhere must fail validation.
	f := Flags{Protocol: "grpc"}
	var spec api.DeploySpec
	if err := f.Apply(&spec); err == nil {
		t.Fatal("expected error: telemetry enabled with no endpoint")
	}
}

func TestApplyRejectsBadHeader(t *testing.T) {
	f := Flags{Endpoint: "otel:4317", Headers: []string{"noequals"}}
	var spec api.DeploySpec
	if err := f.Apply(&spec); err == nil {
		t.Fatal("expected error for malformed --telemetry-header")
	}
}
