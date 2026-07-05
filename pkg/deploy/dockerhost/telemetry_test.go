package dockerhost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
)

func TestApplyWithTelemetryCompanion(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	b.agentImage = "cornus:latest"
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:      "web",
		Image:     "nginx:alpine",
		Telemetry: &api.TelemetrySpec{Endpoint: "otel-backend:4317"},
	}
	st, err := b.Apply(ctx, spec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Status reports exactly one instance — the app, not the companion.
	if len(st.Instances) != 1 {
		t.Fatalf("Status instances = %d, want 1 (companion filtered out)", len(st.Instances))
	}

	var appBody, compBody *createBody
	for i := range f.created {
		c := &f.created[i]
		if c.Labels[labelRole] == roleTelemetryCaretaker {
			compBody = c
		} else if c.Labels[deploy.LabelApp] == "web" {
			appBody = c
		}
	}
	if appBody == nil || compBody == nil {
		t.Fatalf("want an app AND a telemetry companion; created=%d", len(f.created))
	}

	// The app container is auto-wired to the loopback OTLP receiver.
	env := strings.Join(appBody.Env, " ")
	if !strings.Contains(env, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317") {
		t.Errorf("app OTEL endpoint not injected: %v", appBody.Env)
	}
	if !strings.Contains(env, "OTEL_SERVICE_NAME=web") {
		t.Errorf("app OTEL_SERVICE_NAME not injected: %v", appBody.Env)
	}

	// The companion shares the app's netns and carries the Otel role.
	if !strings.HasPrefix(compBody.HostConfig.NetworkMode, "container:") {
		t.Errorf("companion NetworkMode = %q, want container:<app>", compBody.HostConfig.NetworkMode)
	}
	if compBody.Image != "cornus:latest" || strings.Join(compBody.Cmd, " ") != "caretaker" {
		t.Errorf("companion image/cmd = %q/%v", compBody.Image, compBody.Cmd)
	}
	var cfg caretaker.Config
	for _, e := range compBody.Env {
		if strings.HasPrefix(e, "CORNUS_CARETAKER_CONFIG=") {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(e, "CORNUS_CARETAKER_CONFIG=")), &cfg)
		}
	}
	if cfg.Otel == nil || cfg.Otel.ExporterEndpoint != "otel-backend:4317" {
		t.Fatalf("companion otel role = %+v", cfg.Otel)
	}
	if cfg.Otel.GRPCEndpoint != "127.0.0.1:4317" {
		t.Errorf("companion receiver = %q", cfg.Otel.GRPCEndpoint)
	}

	// Delete reaps BOTH the app and the companion.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("Delete left %d containers, want 0 (app + companion reaped)", len(f.containers))
	}
}

// Telemetry needs the cornus agent image to run the collector companion; a plain
// Apply with telemetry but no configured agent image must fail clearly.
func TestApplyTelemetryNeedsAgentImage(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f) // no agentImage
	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Telemetry: &api.TelemetrySpec{Endpoint: "otel-backend:4317"}}
	if _, err := b.Apply(context.Background(), spec); err == nil {
		t.Fatal("expected error when telemetry is enabled but no agent image is configured")
	}
}
