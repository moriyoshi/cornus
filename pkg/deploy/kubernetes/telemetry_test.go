package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
)

// appEnv returns the app container's env var value for key (and whether present).
func otelAppEnv(pod corev1.PodSpec, key string) (string, bool) {
	for _, e := range pod.Containers[0].Env {
		if e.Name == key {
			return e.Value, true
		}
	}
	return "", false
}

// caretakerConfigOf extracts the caretaker Config from the sole caretaker
// container's CORNUS_CARETAKER_CONFIG env.
func caretakerConfigOf(t *testing.T, ctr *corev1.Container) caretaker.Config {
	t.Helper()
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			var cfg caretaker.Config
			if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
				t.Fatalf("unmarshal caretaker config: %v", err)
			}
			return cfg
		}
	}
	t.Fatal("no CORNUS_CARETAKER_CONFIG on caretaker container")
	return caretaker.Config{}
}

func applyAndGetPod(t *testing.T, spec api.DeploySpec) corev1.PodSpec {
	t.Helper()
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(context.Background(), spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return dep.Spec.Template.Spec
}

// TestTelemetryAloneGetsCaretakerWithOtelRole proves a telemetry-only spec gets
// its own caretaker carrying just the Otel role, and the app container is
// auto-wired to the loopback receiver via OTEL_* env.
func TestTelemetryAloneGetsCaretakerWithOtelRole(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	spec := api.DeploySpec{
		Name:      "web",
		Image:     "img",
		Telemetry: &api.TelemetrySpec{Endpoint: "otel-backend:4317"},
	}
	pod := applyAndGetPod(t, spec)

	ctr := caretakerContainer(t, pod)
	cfg := caretakerConfigOf(t, ctr)
	if cfg.Otel == nil {
		t.Fatal("caretaker config has no Otel role")
	}
	if cfg.Otel.ExporterEndpoint != "otel-backend:4317" {
		t.Errorf("exporter endpoint = %q, want otel-backend:4317", cfg.Otel.ExporterEndpoint)
	}
	if cfg.Otel.GRPCEndpoint == "" || cfg.Otel.HTTPEndpoint == "" {
		t.Errorf("receiver endpoints not set: %+v", cfg.Otel)
	}

	if v, ok := otelAppEnv(pod, "OTEL_EXPORTER_OTLP_ENDPOINT"); !ok || v != "http://127.0.0.1:4317" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q (present=%v), want http://127.0.0.1:4317", v, ok)
	}
	if v, ok := otelAppEnv(pod, "OTEL_EXPORTER_OTLP_PROTOCOL"); !ok || v != "grpc" {
		t.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL = %q (present=%v), want grpc", v, ok)
	}
	if v, ok := otelAppEnv(pod, "OTEL_SERVICE_NAME"); !ok || v != "web" {
		t.Errorf("OTEL_SERVICE_NAME = %q (present=%v), want web", v, ok)
	}
}

// TestTelemetryFoldsIntoDockerCaretaker proves telemetry + docker share ONE
// caretaker (both roles), not two sidecars.
func TestTelemetryFoldsIntoDockerCaretaker(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	spec := api.DeploySpec{
		Name:      "web",
		Image:     "img",
		Docker:    &api.DockerSpec{},
		Telemetry: &api.TelemetrySpec{Endpoint: "otel-backend:4317", Protocol: "http/protobuf"},
	}
	pod := applyAndGetPod(t, spec)

	ctr := caretakerContainer(t, pod) // fails if not exactly one
	cfg := caretakerConfigOf(t, ctr)
	if cfg.Otel == nil || cfg.Docker == nil {
		t.Fatalf("expected both Otel and Docker roles on one caretaker: otel=%v docker=%v", cfg.Otel != nil, cfg.Docker != nil)
	}
	// http protocol advertises the http receiver port.
	if v, _ := otelAppEnv(pod, "OTEL_EXPORTER_OTLP_ENDPOINT"); v != "http://127.0.0.1:4318" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want http://127.0.0.1:4318", v)
	}
	if v, _ := otelAppEnv(pod, "OTEL_EXPORTER_OTLP_PROTOCOL"); v != "http/protobuf" {
		t.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL = %q, want http/protobuf", v)
	}
}

// TestTelemetryHeadersProjectedViaSecret proves a sensitive exporter header is
// NOT embedded as a literal in the caretaker config: its value moves to a
// Deployment-owned Secret, referenced by a secretKeyRef env on the caretaker
// container, and the config carries only the env-var name (ExporterHeaderEnv).
func TestTelemetryHeadersProjectedViaSecret(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()
	spec := api.DeploySpec{
		Name:  "web",
		Image: "img",
		Telemetry: &api.TelemetrySpec{
			Endpoint: "otel-backend:4317",
			Headers:  map[string]string{"authorization": "Bearer sekret"},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec
	ctr := caretakerContainer(t, pod)
	cfg := caretakerConfigOf(t, ctr)

	// The header VALUE must not appear anywhere in the caretaker container env.
	for _, e := range ctr.Env {
		if strings.Contains(e.Value, "sekret") {
			t.Fatalf("secret header value leaked into caretaker env %q", e.Name)
		}
	}
	if cfg.Otel == nil || len(cfg.Otel.ExporterHeaders) != 0 {
		t.Fatalf("ExporterHeaders should be cleared, got %+v", cfg.Otel)
	}
	envVar := cfg.Otel.ExporterHeaderEnv["authorization"]
	if envVar == "" {
		t.Fatalf("ExporterHeaderEnv not set: %+v", cfg.Otel)
	}
	// A secretKeyRef env with that name references the header Secret.
	var ref *corev1.SecretKeySelector
	for _, e := range ctr.Env {
		if e.Name == envVar && e.ValueFrom != nil {
			ref = e.ValueFrom.SecretKeyRef
		}
	}
	if ref == nil || ref.Name != "cornus-otel-hdr-web" || ref.Key != envVar {
		t.Fatalf("secretKeyRef = %+v, want cornus-otel-hdr-web/%s", ref, envVar)
	}
	// The Secret was created with the value, owned by the Deployment.
	sec, err := cs.CoreV1().Secrets("default").Get(ctx, "cornus-otel-hdr-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get header secret: %v", err)
	}
	if got := string(sec.Data[envVar]); got != "Bearer sekret" {
		// StringData is surfaced as Data by the fake once applied; accept either.
		if sec.StringData[envVar] != "Bearer sekret" {
			t.Errorf("secret value = %q / %q, want Bearer sekret", got, sec.StringData[envVar])
		}
	}
	if len(sec.OwnerReferences) == 0 || sec.OwnerReferences[0].Name != "web" {
		t.Errorf("secret owner = %+v, want the web Deployment", sec.OwnerReferences)
	}
}

// TestTelemetryDoesNotOverrideUserEnv proves a user-set OTEL_SERVICE_NAME in the
// spec env is preserved (not clobbered by the auto-wiring).
func TestTelemetryDoesNotOverrideUserEnv(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	spec := api.DeploySpec{
		Name:      "web",
		Image:     "img",
		Env:       map[string]string{"OTEL_SERVICE_NAME": "custom"},
		Telemetry: &api.TelemetrySpec{Endpoint: "otel-backend:4317"},
	}
	pod := applyAndGetPod(t, spec)

	// The app container should carry exactly one OTEL_SERVICE_NAME, the user's.
	count, val := 0, ""
	for _, e := range pod.Containers[0].Env {
		if e.Name == "OTEL_SERVICE_NAME" {
			count++
			val = e.Value
		}
	}
	if count != 1 || val != "custom" {
		t.Errorf("OTEL_SERVICE_NAME count=%d val=%q, want exactly one 'custom'", count, val)
	}
}
