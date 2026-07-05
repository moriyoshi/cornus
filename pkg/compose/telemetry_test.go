package compose

import (
	"testing"
)

func TestTranslateTelemetry(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-telemetry:
      endpoint: otel-backend:4317
      protocol: http/protobuf
      insecure: true
      signals: [traces, metrics]
      service_name: my-web
      grpc_port: 5317
      http_port: 5318
      headers:
        authorization: Bearer tok
      resource_attributes:
        deployment.environment: staging
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ts := plans["web"].Spec.Telemetry
	if ts == nil {
		t.Fatal("Telemetry is nil")
	}
	if !ts.Enabled {
		t.Error("presence should enable telemetry")
	}
	if ts.Endpoint != "otel-backend:4317" || ts.Protocol != "http/protobuf" || !ts.Insecure {
		t.Errorf("scalars = %+v", ts)
	}
	if ts.ServiceName != "my-web" || ts.GRPCPort != 5317 || ts.HTTPPort != 5318 {
		t.Errorf("service/ports = %+v", ts)
	}
	if len(ts.Signals) != 2 || ts.Signals[0] != "traces" {
		t.Errorf("signals = %v", ts.Signals)
	}
	if ts.Headers["authorization"] != "Bearer tok" {
		t.Errorf("headers = %v", ts.Headers)
	}
	if ts.ResourceAttributes["deployment.environment"] != "staging" {
		t.Errorf("resource_attributes = %v", ts.ResourceAttributes)
	}
}

func TestTranslateNoTelemetry(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plans["web"].Spec.Telemetry != nil {
		t.Fatal("expected no Telemetry")
	}
}

func TestProjectTelemetryDefaultAndOverride(t *testing.T) {
	file := writeCompose(t, `
name: proj
x-cornus-telemetry:
  endpoint: shared-otel:4317
services:
  inherits:
    image: a:latest
  overrides:
    image: b:latest
    x-cornus-telemetry:
      endpoint: own-otel:4318
      protocol: http/protobuf
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// A service with no block inherits the project default (and is enabled).
	inh := plans["inherits"].Spec.Telemetry
	if inh == nil || !inh.Enabled || inh.Endpoint != "shared-otel:4317" {
		t.Fatalf("inherited telemetry = %+v", inh)
	}
	// A service with its own block fully overrides the default.
	ovr := plans["overrides"].Spec.Telemetry
	if ovr == nil || ovr.Endpoint != "own-otel:4318" || ovr.Protocol != "http/protobuf" {
		t.Fatalf("overriding telemetry = %+v", ovr)
	}
}

func TestProjectTelemetryDefaultValidated(t *testing.T) {
	// A malformed project-level default fails planning even if every service would
	// override it (validated once up front).
	file := writeCompose(t, `
name: proj
x-cornus-telemetry:
  protocol: grpc
services:
  web:
    image: a:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("proj"); err == nil {
		t.Fatal("expected planning to fail for a project telemetry default with no endpoint")
	}
}

// A telemetry block with no endpoint must fail planning (Validate), not silently
// produce a broken spec.
func TestTranslateTelemetryEndpointRequired(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-telemetry:
      protocol: grpc
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("proj"); err == nil {
		t.Fatal("expected planning to fail for telemetry with no endpoint")
	}
}
