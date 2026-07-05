# Observability Synthesis

## Summary

Cornus has four related but distinct diagnostic planes. `pkg/observability`
instruments Cornus processes with OpenTelemetry and structured logging.
`TelemetrySpec` wires application telemetry through an embedded Collector.
Built-in observability accepts, stores, queries, and optionally re-exports
workload signals. The activity flight recorder persists local begin/end lifecycle
events so crash-interrupted work can be reconstructed even when telemetry was
never exported.

## Included Documents

| Document | Focus |
|----------|-------|
| [observability-and-logging.md](./observability-and-logging.md) | Cornus process instrumentation, structured logging, propagation, spans, and metrics |
| [workload-telemetry.md](./workload-telemetry.md) | Embedded Collector and backend-neutral workload wiring |
| [built-in-observability.md](./built-in-observability.md) | OTLP ingest, IMBH storage, automatic logs, query surfaces, mux, and re-export |
| [activity-flight-recorder.md](./activity-flight-recorder.md) | Durable local lifecycle records and unfinished-work recovery |

## Stable Knowledge

- `pkg/observability.Setup` is the single setup seam for Cornus's own traces,
  metrics, and logs. It uses standard `OTEL_*` configuration and installs W3C
  tracecontext plus baggage propagation. `pkg/logging` owns `slog.Default` and
  remains usable without OpenTelemetry.
- Client REST and WebSocket calls propagate trace context into server handlers.
  Streaming wrappers must preserve `http.Hijacker` and `http.Flusher`, and
  context-bound loggers carry span correlation without global mutable fields.
- Workload telemetry is opt-in through `api.TelemetrySpec`.
  `deploy.BuildTelemetryWiring` is the backend-neutral resolver for both
  caretaker configuration and application `OTEL_*` environment.
- The embedded Collector is behind `-tags otelcol`. Kubernetes injects exporter
  headers through a deployment-owned Secret; host backends use per-replica
  companions joined to the workload network namespace.
- Built-in observability has two independent capabilities: OTLP receive/re-export
  can work without storage, while query APIs require the `imbh`-tagged
  `pkg/obsstore`. Invalid ingest must be rejected before the sender discards it.
- Automatic runtime log recording and Collector exports converge on the same
  acceptance path. Replica identity is a deterministic record attribute, and
  `LogOptions.Instance` is zero-based across backends.
- When Cornus is the telemetry destination, `TelemetrySpec.ViaMux` defaults on
  but remains tri-state. The caretaker `'T'` stream carries one length-prefixed
  export and one acknowledgement per stream, preserving Collector retry
  semantics over the authenticated caretaker connection.
- Backpressure policies differ deliberately: automatic log recording sheds,
  direct OTLP ingest returns `429` for retry, and bounded re-export queues drop
  rather than blocking Cornus indefinitely.
- Activity records are append-only NDJSON begin/end pairs, not OTLP events.
  Missing end records identify interrupted work. History and follow share one
  tail cursor so no event is lost during the transition.

## Operational Guidance

- Decide which plane a new signal belongs to before wiring it. Cornus internals
  use `pkg/observability`; application signals use `TelemetrySpec`; durable
  queryable workload data uses `obsstore`; crash-forensic lifecycle uses
  `pkg/activity`.
- Keep `BuildTelemetryWiring` and the common OTLP acceptance seam authoritative.
  Backend-specific copies cause secret leakage, mux drift, or store-less gateway
  false success.
- Keep stdout pure for machine protocols and use context-bound `slog` for
  diagnostics. Cancellation during stream teardown belongs at debug level,
  while unexpected stream failure belongs at warning level.
- Treat tagged paths as separate release and test surfaces. The default Go gate
  proves stubs and neutral wiring; `make test-otel` and `make test-imbh` prove
  compiled implementations.

## Files

- `pkg/observability/` and `pkg/logging/` - Cornus process telemetry and logging.
- `pkg/otelcollector/`, `pkg/api/`, and `pkg/deploy/` - Collector and workload
  wiring.
- `pkg/obsstore/`, `pkg/server/obs*.go`, and
  `pkg/server/logrecorder.go` - ingest, storage, queries, re-export, and automatic
  logs.
- `pkg/caretaker/telemetry.go` - loopback OTLP to caretaker-mux relay.
- `pkg/activity/`, `pkg/server/activity_http.go`, and
  `cmd/cornus/activity.go` - flight recorder and read surfaces.

## Tests

- The default Go gate covers propagation, logging handlers, instrumentation,
  activity pairing/tailing, neutral telemetry wiring, and store interfaces.
- `make test-otel` and `make test-imbh` exercise the tagged Collector and IMBH
  implementations.
- Observability, telemetry-mux, CLI, MCP, multi-replica, and activity
  crash-recovery E2E scenarios cover the cross-process paths.

## Pitfalls

- `OTEL_SDK_DISABLED` overrides Cornus enable flags.
- A fake placed at the ingest or Collector boundary can hide the wiring failure
  under test; retain live tagged scenarios.
- Do not put exporter header values into caretaker configuration on Kubernetes.
- `--follow --unfinished` is invalid because unfinished activity requires a
  whole-stream pairing pass.
- Kubernetes caretaker activity still needs shipping to the server; host
  companion records already bind into server-owned storage.

## Metric Queryability Refresh

Successful recording does not prove queryability. Kubernetes workload samples require unique timestamps per series; duplicate timestamps allowed SQL inspection while poisoning every PromQL read. Regression coverage therefore executes the actual PromQL path used by the contextual web charts.

Histogram selection remains a distinct store-profile limitation where canonical bucket, sum, and count forms do not resolve. Use `cornus observe query` SQL to distinguish ingestion from query-shaping defects before changing Collector or deploy wiring.
