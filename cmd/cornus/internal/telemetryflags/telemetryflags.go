// Package telemetryflags is the shared workload-telemetry CLI surface, embedded
// by the commands that build a DeploySpec (cornus deploy and cornus compose up).
// It turns the flags into an api.TelemetrySpec so a user can enable the embedded
// OpenTelemetry Collector from the command line, without a spec-file / compose
// `x-cornus-telemetry:` block.
package telemetryflags

import (
	"fmt"
	"strings"

	"cornus/pkg/api"
)

// Flags is the workload-telemetry CLI surface. The zero value means "no telemetry
// flags given" and leaves any spec-file / compose telemetry untouched.
type Flags struct {
	Endpoint    string   `kong:"name='telemetry-endpoint',help='Run an embedded OpenTelemetry Collector in the caretaker and export the workload telemetry to this OTLP endpoint (host:port for grpc, URL for http/protobuf). Setting it enables telemetry and auto-wires the workload OTEL_* env.'"`
	Protocol    string   `kong:"name='telemetry-protocol',help='OTLP exporter protocol: grpc (default) | http/protobuf.'"`
	Headers     []string `kong:"name='telemetry-header',sep='none',help='Static OTLP export header KEY=VALUE (e.g. an auth token). Repeatable.'"`
	Insecure    bool     `kong:"name='telemetry-insecure',help='Disable transport security to the OTLP backend (plaintext / no server-cert verification).'"`
	Signals     []string `kong:"name='telemetry-signal',sep='none',help='Restrict collector pipelines to these signals: traces|metrics|logs. Repeatable; default all.'"`
	ServiceName string   `kong:"name='telemetry-service-name',help='Override OTEL_SERVICE_NAME injected into the workload (default: the deployment name).'"`
	Debug       bool     `kong:"name='telemetry-debug',help='Also log collected telemetry to the collector stdout (troubleshooting).'"`
}

// Set reports whether any telemetry flag was provided.
func (f Flags) Set() bool {
	return f.Endpoint != "" || f.Protocol != "" || len(f.Headers) > 0 ||
		f.Insecure || len(f.Signals) > 0 || f.ServiceName != "" || f.Debug
}

// Apply merges the flags onto spec.Telemetry (creating it when any flag is set)
// and validates the result. Flags override scalar values already present from a
// spec file / compose block; headers merge (a flag wins on a key clash).
func (f Flags) Apply(spec *api.DeploySpec) error {
	if !f.Set() {
		return nil
	}
	if spec.Telemetry == nil {
		spec.Telemetry = &api.TelemetrySpec{}
	}
	t := spec.Telemetry
	t.Enabled = true
	if f.Endpoint != "" {
		t.Endpoint = f.Endpoint
	}
	if f.Protocol != "" {
		t.Protocol = f.Protocol
	}
	if f.Insecure {
		t.Insecure = true
	}
	if f.ServiceName != "" {
		t.ServiceName = f.ServiceName
	}
	if f.Debug {
		t.Debug = true
	}
	if len(f.Signals) > 0 {
		t.Signals = append([]string(nil), f.Signals...)
	}
	for _, h := range f.Headers {
		k, v, ok := strings.Cut(h, "=")
		if !ok || k == "" {
			return fmt.Errorf("--telemetry-header: want KEY=VALUE, got %q", h)
		}
		if t.Headers == nil {
			t.Headers = map[string]string{}
		}
		t.Headers[k] = v
	}
	return t.Validate()
}
