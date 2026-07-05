package caretaker

import (
	"context"
	"fmt"
	"net"
	"time"

	"cornus/pkg/logging"
	"cornus/pkg/otelcollector"
)

// OtelRole configures the embedded OpenTelemetry Collector the caretaker runs as
// a self-contained role (like proxy/dns/docker, it does NOT dial the cornus
// server): an OTLP receiver on pod-loopback that batches/limits the workload's
// telemetry and exports it to an external OTLP backend. It is the Collector's
// input shape; see pkg/otelcollector. Defined as an alias so the wire contract
// has a single source of truth (mirrored by the host-backend companions).
type OtelRole = otelcollector.Config

// runOtel runs the embedded Collector until ctx is cancelled. When the binary was
// built without the collector (the stub), it logs an actionable error and returns
// it, so the supervisor surfaces the misconfiguration and the readiness probe
// keeps failing (a visible crashloop) rather than silently dropping telemetry.
func runOtel(ctx context.Context, role OtelRole) error {
	log := logging.FromContext(ctx)
	if !otelcollector.Compiled() {
		log.ErrorContext(ctx, "caretaker otel: OpenTelemetry Collector not compiled into this build (rebuild the sidecar image with -tags otelcol); telemetry will not be collected")
		return otelcollector.ErrNotCompiled
	}
	log.InfoContext(ctx, "caretaker otel: starting collector",
		"grpc", role.GRPCEndpoint, "http", role.HTTPEndpoint,
		"exporter", role.ExporterEndpoint, "protocol", role.ExporterProtocol)
	return otelcollector.Run(ctx, role)
}

// otelReady reports nil once the Collector's configured OTLP receiver port(s)
// accept connections, so the app container's startup probe waits until telemetry
// ingestion is live. It observes only cross-process-visible state (a TCP dial),
// matching how Ready checks the other roles.
func otelReady(role OtelRole) error {
	for _, addr := range []string{role.GRPCEndpoint, role.HTTPEndpoint} {
		if addr == "" {
			continue
		}
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return fmt.Errorf("otel collector receiver %s not ready: %w", addr, err)
		}
		_ = c.Close()
	}
	return nil
}
