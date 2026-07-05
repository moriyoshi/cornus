# Workload Telemetry Collector

## Summary

Cornus can run an embedded OpenTelemetry Collector inside the caretaker and wire a deployed workload to its loopback OTLP receiver. This is workload telemetry, distinct from Cornus's own `pkg/observability` instrumentation, and works on Kubernetes plus the dockerhost, containerdhost, and barehost backends.

## Key Facts

- `api.TelemetrySpec` on `DeploySpec` is active only when configured and validates the endpoint, protocol, signals, and headers.
- `pkg/otelcollector` is behind the `otelcol` build tag. The default build uses a stub returning `ErrNotCompiled`; `make test-otel` builds and smoke-tests the real Collector.
- Use `deploy.BuildTelemetryWiring(spec, name)` as the single backend-neutral resolver. It returns both the caretaker Otel role and app `OTEL_*` environment, never replacing a user-specified key.
- Collector-core is deliberately sufficient: OTLP receiver, memory limiter and batch processors, and OTLP gRPC/HTTP/debug exporters. Its component names are `otlp_grpc` and `otlp_http`.

## Details

The collector is configured from an in-memory `confmap` provider rather than a YAML file. It runs as a supervised caretaker role with listener readiness, so a ready telemetry workload proves that the receiver is bound. Resource attributes are injected through `OTEL_RESOURCE_ATTRIBUTES` rather than needing collector-contrib's resource processor.

Kubernetes folds the role into every caretaker-assembly variant and injects telemetry alone when no other caretaker capability is needed. Exporter headers are not retained in `CORNUS_CARETAKER_CONFIG`: Kubernetes creates a deployment-owned Secret and sets deterministic `secretKeyRef` environment variables named by `deploy.TelemetryHeaderEnvVar`. The role config contains only `ExporterHeaderEnv`. Host backends use a per-replica telemetry companion that joins the application netns and is reaped by normal companion lifecycle plumbing; it requires `CORNUS_AGENT_IMAGE` and is recreated by Apply after a host reboot.

Compose accepts `x-cornus-telemetry` both per service and as a project default. A service block fully overrides the project block. `cornus deploy` and `cornus compose up` share `cmd/cornus/internal/telemetryflags`, whose `--telemetry-*` flags override descriptor values; Compose applies them to each selected service.

Release images include `otelcol` by default through `BUILD_TAGS`, but a lean image can override it. The default Go gate remains light; tagged build and tests run in `make test-otel`. The kube E2E runner builds its sidecar with `BUILD_TAGS="netgo osusergo otelcol"` and runs `otel-collector.star` when `CORNUS_TEST_OTEL=1`.

## Files

- `pkg/otelcollector/` - tagged Collector implementation, stub, config, and smoke test.
- `pkg/api/` and `pkg/deploy/` - `TelemetrySpec`, caretaker Otel role, and `BuildTelemetryWiring`.
- `pkg/deploy/kubernetes/` - caretaker injection and Secret-projected exporter headers.
- `cmd/cornus/internal/telemetryflags/` and `pkg/compose/` - flags and service/project Compose extension.
- `e2e/scenarios/otel-collector.star`, Dockerfile, Makefile, and CI workflows - tagged verification.

## Test Coverage

Default tests cover tag-neutral config construction and deploy wiring. Tagged smoke coverage starts the actual Collector and cancels it. Kubernetes tests assert headers are absent from caretaker config/env literals and appear through a deployment-owned Secret. The kube E2E asserts the app becomes Running and receives the expected OTEL environment.

## Pitfalls

- Both the server and application image must contain the `otelcol` build-tagged binary; the default stub is intentional.
- Do not duplicate wiring in backend-specific code. `BuildTelemetryWiring` prevents cross-backend drift.
- Host companions cannot survive a reboot without a new Apply because their joined netns path is volatile.
