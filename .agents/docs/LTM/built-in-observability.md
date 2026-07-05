# Built-In Workload Observability

## Summary

Cornus can ingest, retain, query, and optionally re-export workload telemetry.
An IMBH-backed store provides durable logs, traces, metrics, SQL, and
Grafana-compatible APIs; a plain build can still act as an OTLP gateway without
the store. Managed workload logs are recorded automatically across all replicas,
while the embedded Collector defaults to exporting to Cornus over the caretaker
mux.

## Key Facts

- `pkg/obsstore` is real under the `imbh` build tag and has a stub otherwise.
- Receive routes require any sink (store or upstream); query routes require the
  store.
- The recorder sheds on overload, OTLP ingest returns 429 so the sender retries,
  and re-export drops after bounded queues so an upstream outage cannot block
  Cornus.
- `TelemetrySpec.ViaMux *bool` is tri-state; unset defaults on when Cornus is the
  destination, while explicit false survives normalization.
- The `'T'` caretaker stream uses one length-prefixed stream per export and a
  one-byte acknowledgement mapped to HTTP-style success/backpressure/failure.
- `LogOptions.Instance` is zero-based and deterministic on all five backends;
  out-of-range is `ErrNotFound`.

## Details

Two feeds converge on `acceptOTLP`: the server's zero-touch log recorder and the
embedded Collector. Re-export forced this common acceptance seam and explicit
validation in store-less gateway mode; otherwise invalid payloads could receive
200 and be rejected only after the sender discarded them.

The mux uses a loopback OTLP/HTTP shim rather than a custom Collector exporter.
The stock Collector therefore keeps its retry behavior, while the shim forwards
over the existing authenticated caretaker connection. The initial
Kubernetes-only restriction was removed after the existing host companion
connection plumbing was found; Docker, containerd, and bare telemetry companions
also use the mux when Cornus is the destination.

Read surfaces include `compose logs --from=auto|runtime|store`,
`cornus observe`, bounded MCP tools, `cornus://observe/errors`, and
Prometheus/Loki/Tempo-shaped endpoints. `observe_trace` assembles a tree for MCP
while the raw HTTP API remains flat.

## Files

- `pkg/obsstore` - IMBH adapter and query normalization.
- `pkg/server/obs*.go` - ingest, query, export, mux, and Grafana APIs.
- `pkg/server/logrecorder.go` - zero-touch multi-replica logging.
- `pkg/caretaker/telemetry.go` - loopback-to-mux relay.
- `cmd/cornus/observe.go` - CLI.

## Test Coverage

The Go gate, `make test-otel`, and `make test-imbh` cover unit and tagged paths.
Seven Docker E2E scenarios cover storage, re-export/gateway mode, Compose logs,
CLI, MCP, replica distinction, and the telemetry-mux scenario.

## Pitfalls

- A fake usually sits at the component boundary where wiring bugs occur; live E2E
  caught recorder bypass, store-less validation, and missing wire parameters.
- Replica ordinal is a record attribute, not a resource attribute.
- In imbh-go, a lower time bound without an upper bound matches nothing;
  `obsstore.window()` supplies the missing bound.
- The tagged Kubernetes runner now executes `observability-telemetry-mux.star`
  against a real Collector, and released serving artifacts include `imbh`.

## Resource Metrics and Live Coverage

Every managed workload can now emit CPU, memory, network, disk, and process
samples through `deploy.MetricsSampler`; the server records its own usage through
the same acceptance and re-export path. `api.ResourceSample` uses pointers and
nil maps so "not observable" is not rendered as a zero measurement.

The recorder polls on its own ticker and does not backfill. This differs
deliberately from log recording, which holds a stream and resumes from a
watermark. Backend sampling is kept outside OpenTelemetry observable callbacks so
a stalled runtime cannot block the whole meter collection.

The store normalizes metric names but not attribute keys. Workload series
therefore use Prometheus-compatible underscore attributes such as
`cornus_replica`; dotted keys cannot be selected by the store's PromQL subset.
Live queries, not encoder unit tests, establish this name contract.

The E2E runner originally linked no `imbh` store, so eight observability scenarios
self-skipped in every containerized leg. The cgo/tagged build path and
metrics-server installer now make those scenarios execute on Kubernetes. The
first real run exposed one-shot Jobs missing from backend `List`, absent versus
explicit `telemetry=""` being collapsed in the harness, and an omitted
`--no-obs` on the store-less gateway leg.
