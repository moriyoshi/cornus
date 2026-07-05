package deploy

import (
	"fmt"
	"sort"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/otelcollector"
)

// Default OTLP receiver ports the embedded Collector binds inside the pod netns
// (the IANA-registered OTLP defaults), advertised to the app container.
const (
	DefaultOtelGRPCPort = 4317
	DefaultOtelHTTPPort = 4318
)

// TelemetryWiring is the resolved result of turning a workload's TelemetrySpec
// into (a) the caretaker's embedded-Collector role config and (b) the OTEL_*
// environment to inject into the app container so its SDK ships to the sidecar.
// Every backend consumes it the same way, so the auto-wiring cannot drift between
// backends.
type TelemetryWiring struct {
	// Role is the caretaker Otel role (receiver + exporter config). It is
	// otelcollector.Config, which caretaker.OtelRole aliases.
	Role otelcollector.Config
	// Env are the OTEL_* variables to add to the app container. It already
	// excludes any key the user set in spec.Env, so a backend appends them
	// unconditionally.
	Env map[string]string
}

// EnvSorted returns the wiring's env as sorted KEY=VALUE-friendly pairs, for
// backends (host) that apply env as an ordered slice and for deterministic tests.
func (w *TelemetryWiring) EnvSorted() [][2]string {
	keys := make([]string, 0, len(w.Env))
	for k := range w.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, w.Env[k]})
	}
	return out
}

// BuildTelemetryWiring resolves spec.Telemetry into the caretaker Collector role
// and the app-container OTEL_* env. name is the deployment name (the default
// service.name). It returns (nil, nil) when telemetry is inactive. A user-set
// OTEL_* key in spec.Env always wins: such keys are omitted from Env so the
// backend never overrides them.
func BuildTelemetryWiring(spec api.DeploySpec, name string) (*TelemetryWiring, error) {
	t := spec.Telemetry
	if !t.Active() {
		return nil, nil
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	grpcPort := t.GRPCPort
	if grpcPort == 0 {
		grpcPort = DefaultOtelGRPCPort
	}
	httpPort := t.HTTPPort
	if httpPort == 0 {
		httpPort = DefaultOtelHTTPPort
	}

	role := otelcollector.Config{
		GRPCEndpoint:     fmt.Sprintf("127.0.0.1:%d", grpcPort),
		HTTPEndpoint:     fmt.Sprintf("127.0.0.1:%d", httpPort),
		ExporterEndpoint: strings.TrimSpace(t.Endpoint),
		ExporterProtocol: t.Protocol,
		ExporterHeaders:  t.Headers,
		ExporterInsecure: t.Insecure,
		Signals:          t.Signals,
		Debug:            t.Debug,
	}

	// Advertise the receiver to the app on the protocol the user chose (the
	// receiver serves both, so either works); grpc is the default. Note the
	// gRPC OTLP endpoint uses an http(s) scheme with no path per the OTel spec.
	appProto := "grpc"
	appPort := grpcPort
	if p := t.Protocol; p == "http" || p == "http/protobuf" {
		appProto = "http/protobuf"
		appPort = httpPort
	}
	env := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": fmt.Sprintf("http://127.0.0.1:%d", appPort),
		"OTEL_EXPORTER_OTLP_PROTOCOL": appProto,
	}
	serviceName := t.ServiceName
	if serviceName == "" {
		serviceName = name
	}
	env["OTEL_SERVICE_NAME"] = serviceName
	if ra := resourceAttributes(name, t.ResourceAttributes); ra != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = ra
	}

	// Never override an OTEL_* the user set explicitly in the spec env.
	for k := range spec.Env {
		delete(env, k)
	}

	return &TelemetryWiring{Role: role, Env: env}, nil
}

// TelemetryHeaderEnvVar returns the deterministic environment-variable name a
// backend uses to project the export header `key` from a Secret. The same name is
// used for the secretKeyRef env var, the Secret data key, and the collector's
// ExporterHeaderEnv entry, so all three agree without threading state. It is
// uppercased with every non-alphanumeric byte replaced by '_' (a valid env-var /
// Secret-key name).
func TelemetryHeaderEnvVar(key string) string {
	var b strings.Builder
	b.WriteString("CORNUS_OTEL_HEADER_")
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// resourceAttributes renders the OTEL_RESOURCE_ATTRIBUTES value: cornus-derived
// defaults (the deployment name as service.name and a cornus marker) merged with
// the user's extra attributes, as a deterministic comma-separated k=v list.
func resourceAttributes(name string, extra map[string]string) string {
	attrs := map[string]string{
		"service.name":          name,
		"cornus.deployment":     name,
		"telemetry.distro.name": "cornus",
	}
	for k, v := range extra {
		attrs[k] = v
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(attrs[k])
	}
	return b.String()
}
