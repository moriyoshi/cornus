package deploy

import (
	"strings"
	"testing"

	"cornus/pkg/api"
)

func TestBuildTelemetryWiring_Inactive(t *testing.T) {
	w, err := BuildTelemetryWiring(api.DeploySpec{Name: "web"}, "web")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if w != nil {
		t.Fatalf("expected nil wiring for inactive telemetry, got %+v", w)
	}
}

func TestBuildTelemetryWiring_Defaults(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Telemetry: &api.TelemetrySpec{Endpoint: "backend:4317"}}
	w, err := BuildTelemetryWiring(spec, "web")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if w.Role.GRPCEndpoint != "127.0.0.1:4317" || w.Role.HTTPEndpoint != "127.0.0.1:4318" {
		t.Errorf("receiver endpoints = %q/%q", w.Role.GRPCEndpoint, w.Role.HTTPEndpoint)
	}
	if w.Role.ExporterEndpoint != "backend:4317" {
		t.Errorf("exporter endpoint = %q", w.Role.ExporterEndpoint)
	}
	if w.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:4317" {
		t.Errorf("app endpoint = %q", w.Env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
	if w.Env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "grpc" {
		t.Errorf("app protocol = %q", w.Env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	if w.Env["OTEL_SERVICE_NAME"] != "web" {
		t.Errorf("service name = %q", w.Env["OTEL_SERVICE_NAME"])
	}
	if ra := w.Env["OTEL_RESOURCE_ATTRIBUTES"]; !strings.Contains(ra, "service.name=web") || !strings.Contains(ra, "cornus.deployment=web") {
		t.Errorf("resource attributes = %q", ra)
	}
}

func TestBuildTelemetryWiring_HTTPAndCustomPorts(t *testing.T) {
	spec := api.DeploySpec{Name: "api", Telemetry: &api.TelemetrySpec{
		Endpoint: "https://backend", Protocol: "http/protobuf", HTTPPort: 5318, GRPCPort: 5317,
	}}
	w, err := BuildTelemetryWiring(spec, "api")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if w.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:5318" {
		t.Errorf("app endpoint = %q, want http://127.0.0.1:5318", w.Env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
	if w.Env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" {
		t.Errorf("app protocol = %q", w.Env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	if w.Role.HTTPEndpoint != "127.0.0.1:5318" || w.Role.GRPCEndpoint != "127.0.0.1:5317" {
		t.Errorf("receiver endpoints = %q/%q", w.Role.GRPCEndpoint, w.Role.HTTPEndpoint)
	}
}

func TestBuildTelemetryWiring_RespectsUserEnv(t *testing.T) {
	spec := api.DeploySpec{
		Name: "web",
		Env: map[string]string{
			"OTEL_SERVICE_NAME":           "user-svc",
			"OTEL_EXPORTER_OTLP_ENDPOINT": "http://user:4317",
		},
		Telemetry: &api.TelemetrySpec{Endpoint: "backend:4317"},
	}
	w, err := BuildTelemetryWiring(spec, "web")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := w.Env["OTEL_SERVICE_NAME"]; ok {
		t.Error("OTEL_SERVICE_NAME should be omitted (user set it)")
	}
	if _, ok := w.Env["OTEL_EXPORTER_OTLP_ENDPOINT"]; ok {
		t.Error("OTEL_EXPORTER_OTLP_ENDPOINT should be omitted (user set it)")
	}
	// Protocol was not user-set, so it is still injected.
	if w.Env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "grpc" {
		t.Errorf("protocol should still be injected, got %q", w.Env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
}

func TestBuildTelemetryWiring_InvalidEndpoint(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Telemetry: &api.TelemetrySpec{Enabled: true}}
	if _, err := BuildTelemetryWiring(spec, "web"); err == nil {
		t.Fatal("expected error for active telemetry with no endpoint")
	}
}
