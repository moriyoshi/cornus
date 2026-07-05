// Package otelcollector embeds a small, curated OpenTelemetry Collector into the
// cornus binary. It is the "otel agent" the caretaker runs as a self-contained
// role: an OTLP receiver on pod-loopback that batches/limits and exports the
// workload's telemetry onward to a configured OTLP backend.
//
// It is deliberately CURATED — only collector-core components are linked
// (otlpreceiver; memory_limiter + batch processors; otlp / otlphttp / debug
// exporters), so the dependency tree stays bounded, mirroring how the build
// engine walls off BuildKit. The heavy collector import graph lives ONLY in the
// build-tagged collector.go (`//go:build otelcol`); the default build compiles the
// stub.go no-op so `go build ./...` and dev binaries stay lean. The release image
// is built with `-tags otelcol`, so the shipped caretaker sidecar carries the
// Collector.
//
// This is distinct from pkg/observability, which instruments CORNUS ITSELF. This
// package collects a USER WORKLOAD's telemetry.
package otelcollector

import (
	"errors"
	"os"
)

// ErrNotCompiled is returned by Run when the binary was built without the
// `otelcol` build tag, so the Collector is not linked in. The caretaker surfaces
// it as an actionable startup/readiness error rather than silently no-op'ing.
var ErrNotCompiled = errors.New("otelcollector: the OpenTelemetry Collector is not compiled into this build (rebuild with -tags otelcol)")

// Config is the curated Collector's full instruction set, assembled by the deploy
// backend from a TelemetrySpec and carried to the caretaker as part of its role
// config. It is intentionally free of any collector types so it can ride the
// caretaker's JSON config and be referenced from the default (tagless) build.
type Config struct {
	// GRPCEndpoint / HTTPEndpoint are the OTLP receiver bind addresses inside the
	// pod netns (e.g. "127.0.0.1:4317" / "127.0.0.1:4318"). An empty value
	// disables that protocol; at least one must be set.
	GRPCEndpoint string `json:"grpcEndpoint,omitempty"`
	HTTPEndpoint string `json:"httpEndpoint,omitempty"`
	// ExporterEndpoint is the external OTLP backend the Collector forwards to.
	ExporterEndpoint string `json:"exporterEndpoint"`
	// ExporterProtocol selects the exporter: "grpc" (default) uses the otlp
	// exporter; "http/protobuf" (or "http") uses the otlphttp exporter.
	ExporterProtocol string `json:"exporterProtocol,omitempty"`
	// ExporterHeaders are static headers added to every export request (e.g. an
	// auth token) as literal values.
	ExporterHeaders map[string]string `json:"exporterHeaders,omitempty"`
	// ExporterHeaderEnv maps an export header name to the ENVIRONMENT VARIABLE
	// holding its value, read from the process env at run time and overlaid on
	// ExporterHeaders (env wins). It lets the kubernetes backend project a
	// sensitive header (auth token) from a Secret via a secretKeyRef env rather
	// than embedding the value in this config, which otherwise rides a pod-spec
	// env var visible to anyone with pod-read RBAC.
	ExporterHeaderEnv map[string]string `json:"exporterHeaderEnv,omitempty"`
	// ExporterInsecure disables transport security to the backend (plaintext /
	// no server-cert verification). Off by default.
	ExporterInsecure bool `json:"exporterInsecure,omitempty"`
	// Signals restricts which pipelines are built: any of "traces", "metrics",
	// "logs". Empty means all three.
	Signals []string `json:"signals,omitempty"`
	// Debug additionally wires the debug exporter (verbose stdout) into every
	// pipeline and raises the Collector's own log level, for troubleshooting.
	Debug bool `json:"debug,omitempty"`
	// Version stamps the Collector's BuildInfo (the cornus build version).
	Version string `json:"version,omitempty"`
}

// resolvedHeaders returns the effective export headers: the literal
// ExporterHeaders overlaid with values read from ExporterHeaderEnv (each value is
// an env-var NAME resolved from the process environment; env wins). Empty when no
// headers are configured. Called from buildConfigMap, so a Secret-projected header
// is materialized only inside the running collector, never in its serialized config.
func (c Config) resolvedHeaders() map[string]string {
	if len(c.ExporterHeaders) == 0 && len(c.ExporterHeaderEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.ExporterHeaders)+len(c.ExporterHeaderEnv))
	for k, v := range c.ExporterHeaders {
		out[k] = v
	}
	for k, envVar := range c.ExporterHeaderEnv {
		if v := os.Getenv(envVar); v != "" {
			out[k] = v
		}
	}
	return out
}

// allSignals is the pipeline set built when Config.Signals is empty.
var allSignals = []string{"traces", "metrics", "logs"}

// signalsOrAll returns the configured signals, or all three when none are set,
// filtered to the recognized names and de-duplicated in a stable order.
func signalsOrAll(signals []string) []string {
	if len(signals) == 0 {
		return allSignals
	}
	seen := map[string]bool{}
	var out []string
	for _, want := range allSignals { // stable order, ignore unknowns
		for _, s := range signals {
			if s == want && !seen[want] {
				seen[want] = true
				out = append(out, want)
			}
		}
	}
	if len(out) == 0 {
		return allSignals
	}
	return out
}

// buildConfigMap renders cfg into an otelcol config map (the same shape a
// collector YAML file would unmarshal to). It is pure and collector-free, so it
// is exercised by the default-build unit tests; collector.go feeds the result to
// the Collector through an in-memory confmap provider.
func buildConfigMap(cfg Config) map[string]any {
	protocols := map[string]any{}
	if cfg.GRPCEndpoint != "" {
		protocols["grpc"] = map[string]any{"endpoint": cfg.GRPCEndpoint}
	}
	if cfg.HTTPEndpoint != "" {
		protocols["http"] = map[string]any{"endpoint": cfg.HTTPEndpoint}
	}
	receivers := map[string]any{"otlp": map[string]any{"protocols": protocols}}

	// memory_limiter first (drops data under pressure before batching), then batch.
	processors := map[string]any{
		"memory_limiter": map[string]any{
			"check_interval":         "1s",
			"limit_percentage":       80,
			"spike_limit_percentage": 25,
		},
		"batch": map[string]any{},
	}
	procList := []any{"memory_limiter", "batch"}

	// Exporter component IDs as registered by the collector-core factories in this
	// version: the OTLP exporters are "otlp_grpc" / "otlp_http" (the classic bare
	// "otlp"/"otlphttp" names were renamed upstream). Keep these in lockstep with
	// curatedFactories() in collector.go.
	exporterName := "otlp_grpc"
	if p := cfg.ExporterProtocol; p == "http" || p == "http/protobuf" {
		exporterName = "otlp_http"
	}
	otlpExporter := map[string]any{
		"endpoint": cfg.ExporterEndpoint,
		"tls":      map[string]any{"insecure": cfg.ExporterInsecure},
	}
	if resolved := cfg.resolvedHeaders(); len(resolved) > 0 {
		headers := map[string]any{}
		for k, v := range resolved {
			headers[k] = v
		}
		otlpExporter["headers"] = headers
	}
	exporters := map[string]any{exporterName: otlpExporter}
	expList := []any{exporterName}
	if cfg.Debug {
		exporters["debug"] = map[string]any{"verbosity": "detailed"}
		expList = append(expList, "debug")
	}

	pipelines := map[string]any{}
	for _, sig := range signalsOrAll(cfg.Signals) {
		pipelines[sig] = map[string]any{
			"receivers":  []any{"otlp"},
			"processors": procList,
			"exporters":  expList,
		}
	}

	logLevel := "warn"
	if cfg.Debug {
		logLevel = "info"
	}
	return map[string]any{
		"receivers":  receivers,
		"processors": processors,
		"exporters":  exporters,
		"service": map[string]any{
			// Keep the Collector's OWN telemetry quiet: no internal metrics server
			// (would otherwise bind :8888 in the pod), warn-level logs to stderr.
			"telemetry": map[string]any{
				"metrics": map[string]any{"level": "none"},
				"logs":    map[string]any{"level": logLevel},
			},
			"pipelines": pipelines,
		},
	}
}
