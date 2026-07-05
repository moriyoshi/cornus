# Observability and Logging

## Summary

Cornus has first-class OpenTelemetry (traces + metrics + logs) in its two long-lived processes — the unified HTTP server (`cornus serve`) and the per-pod caretaker sidecar — wired through a single setup seam, `pkg/observability`. All process logging goes through `log/slog`, owned by the dependency-light `pkg/logging` package; when telemetry is enabled, slog records fan out to both stderr and the OTLP logs pipeline via `otelslog`.

## Key Facts

- `pkg/observability` is the single OTel setup seam: `Setup(ctx, Options) (*Providers, error)` installs global trace/metric/log providers plus the W3C tracecontext + baggage propagator, all configured through standard `OTEL_*` env vars via `contrib/exporters/autoexport` (no parallel config surface).
- Telemetry is opt-in and zero-cost when off. `Enabled()` returns true only when `CORNUS_OTEL` is set, `serve --otel` is passed (the flag `os.Setenv`s `CORNUS_OTEL` so the env-driven gate agrees), or any standard `OTEL_*` exporter/endpoint var is set — and always false under `OTEL_SDK_DISABLED`. Disabled means the OTel API stays no-op: no exporter goroutines or connections start, instruments are no-ops.
- `Providers.Shutdown` joins reverse-order shutdowns; a partial `Setup` failure tears down what it already installed.
- `pkg/logging` (no OTel dependency) owns the process `slog.Default`. `Init()` installs a stderr `TextHandler` at `CORNUS_LOG_LEVEL` (default info); `InitWith(extra ...slog.Handler)` fans the stderr handler out to additional handlers via a small `fanout` `slog.Handler` (Clone-per-dispatch).
- Every `main` (`cornus`, `cornus-e2e`) calls `logging.Init()`, so light CLI clients get consistent logging without linking the OTel tree.
- On a successful enabled `observability.Setup`, the OTel bridge calls `logging.InitWith(otelslog.NewHandler(ScopeName))`, so a single `slog.Info(...)` reaches both stderr and OTLP logs. `observability.Logger()` returns `slog.Default()`.
- No `log.Printf` or `fmt.Fprint*(os.Stderr, ...)` remain outside tests; program *results* on stdout (`version`, `ps`/preflight tables, `deployed ...` lines) intentionally stay as `fmt` — they are output, not logging.
- Server-proxied streaming ops (port-forward, exec, attach, logs, stats) log backend faults server-side via `logStreamHandlerErr` instead of discarding them, and the CLI surfaces both fallback attempts (direct-to-pod then server proxy) when both fail. See [[kubernetes-backend]].

## Details

### OTel setup seam (pkg/observability)

- The OTel SDK + OTLP/otelhttp/otelgrpc packages were already transitive dependencies (via BuildKit); adding observability promoted them to direct, bumped otel core to v1.44.0, and added `contrib/exporters/autoexport`, `contrib/bridges/otelslog`, `contrib/instrumentation/runtime`, and `otel/{log,sdk/log}` plus the otlplog exporters.
- Configuration is entirely via standard `OTEL_*` env vars handled by `autoexport` (e.g. `OTEL_TRACES_EXPORTER`, `OTEL_LOGS_EXPORTER`); `OTEL_SDK_DISABLED` force-disables.

### Server instrumentation (pkg/server)

- `Server.Handler()` wraps the mux in `otelhttp`; `httpsnoop` preserves `Hijacker`/`Flusher` so the `/.cornus/v1/*/attach` WebSocket and streaming endpoints keep working.
- A `routePattern` span-name formatter collapses digests, deployment names, and upload UUIDs to route templates, bounding span-name cardinality.
- `handleBuild` and the deploy handlers add Cornus spans/metrics: `cornus.build{,s,.duration}` and `cornus.deploy.<action>` / `cornus.deploys`, via a `traceDeploy` helper. Instruments are built from the global meter in `New`.

### Caretaker instrumentation (pkg/caretaker)

- The cmd entrypoints (`caretaker` and the deprecated `mount-agent`) call `Setup` with `service.name=cornus-caretaker`.
- Per-role instruments: mount (`caretaker.mount` span, `caretaker.mounts.active`, a setup-duration histogram, failures), proxy (`caretaker.proxy.connections` / `caretaker.proxy.bytes` — `spliceBidir` returns the two directional byte counts), DNS (`caretaker.dns.queries`).
- The mount role injects trace context into its relay dial via `wire.DialConnControlHeader` (adds an `http.Header` to the WebSocket handshake), so the server-side `/.cornus/v1/deploy/mount/` request span (extracted by `otelhttp`) becomes a child of the caretaker mount span — end-to-end trace continuity across the rendezvous.

### Per-mount trace linking (mount relay)

- The server emits a `cornus.mount.relay` span per relayed mount stream at the caretaker-facing edge, carrying: the session DIGEST (never the raw session id, which is a capability), the mount name, the transport (`local|forwarded`), rx/tx byte counts, and error status.
- Parenting: the `cornus.mount.relay` span is a child of the attach connection's otelhttp span, which in turn links to the caretaker's `caretaker.conn` span — so a single trace walks caller attach -> server relay -> pod caretaker.
- The caretaker side stamps rx/tx byte counts onto its existing `caretaker.mount` span.
- Zero-cost when telemetry is off: a `span.IsRecording()` gate returns the original conn untouched (no wrapping, no counters).
- Cross-replica linking: `dialForward` takes a ctx and injects the W3C traceparent into the forward dial, so the owner replica's `/.cornus/v1/mount/forward` span links to the forwarding replica's relay span (the hub forward inherits the same propagation).

### slog unification (pkg/logging)

- Replaced the caretaker's 7 `log.Printf`s (`dns.go`, `proxy.go`) and all diagnostic/status/warning `fmt.Fprintf(os.Stderr, ...)` prints: the server banner (`serve.go`), the build 9P debug line (`solve_linux.go`), the k8s netdriver `warnf` seam, and the `cornus` / `cornus compose` / `cornus-e2e` CLIs.
- Structured call sites use key/value attrs, e.g. `slog.Warn("caretaker proxy: deny", "dst", dst, "allowed", ...)`, `slog.Info("building", "service", svc, "tag", tag)`. The netdriver `warnf` printf seam is preserved but routes through `slog.Warn(fmt.Sprintf(...))`.
- The `otelslog` fan-out supersedes an earlier standalone otel-only `Logger()` bridge.

### Context-bound structured logging

Production logging uses `logging.FromContext(ctx, attrs...)` and the `*Context` slog emit methods.
`WithAttrs` stores one accumulated `[]slog.Attr` under a single context key, and
`LogAttrsProvider` lets domain values contribute scoped attributes. `FromContext` expands ordinary
slog arguments, `[]slog.Attr`, and providers without passing a provider to slog as `!BADKEY`.

Call sites hoist `log := logging.FromContext(ctx)` above loops. Component prefixes moved from
message text into closed groups such as `slog.Group("kubernetes", "deployment", name)`, keeping
per-call `error` attributes top-level; identity-less sites use `slog.String("component", name)`
because an empty group renders nothing. Error keys are uniformly `error`. Injectable printf seams
(`warnf` and `logf`) retain their signatures and only route their default implementation through
the context logger.

`SetContextAttrs` installs a visitor-style `ContextAttrHook` without importing OTel into
`pkg/logging`. The observability package appends `trace_id` and `span_id` for a valid active span.
`FromContext` installs an already-typed attribute slice with
`slog.New(logger.Handler().WithAttrs(attrs))`, avoiding `logger.With(...any)` boxing. With no
attributes, call-site attrs, or active hook output, it returns `slog.Default()` unchanged.

### Client-to-server distributed tracing

The CLI calls `observability.Setup` as service `cornus-cli`, creates one root span named from the
resolved Kong command path, and flushes providers before fatal exit. `pkg/client` wraps REST with
`otelhttp.NewTransport`; Build, DeployAttach, ExecStart, Attach, and PortForward open explicit
`cornus.client.*` spans and inject W3C headers into their WebSocket upgrades. `InjectHTTP(ctx)` is
the shared propagator used by client and caretaker dials. Disabled telemetry retains no-op behavior
and emits no headers.

The invocation context carries W3C baggage member `cornus.command`. It is created with
`baggage.NewMemberRaw`, because command paths containing spaces require encoding during injection.
`composecli.SetBaseContext` lets foreground Compose operations share the invocation root without
importing package `main`. `daemon docker` remains a separate long-lived process and deliberately
does not inherit a one-shot invocation root.

Client and server parenting is proven by the E2E OTLP/HTTP collector in
`pkg/e2e/otlp_collector.go`. `otlp_collector()` and `otlp_spans()` decode exported protobuf spans;
the scenario waits specifically for `service="cornus"` before asserting that a server span and a
`cornus-cli` span share a trace and have the expected parent relationship. Waiting for a raw count
is racy because the CLI can export two spans before the server batch arrives.

The persistent caretaker connection remains a distinct trace root: it is pod-scoped and serves many
client invocations. Unifying it with an invocation requires relaying context at Apply time or adding
a link at the mount/egress relay boundary; this is tracked as follow-up work rather than assigning a
false parent.

### Streaming/tunnel handler error logging (2026-07-08)

Server-proxied streaming operations used to swallow backend errors, making RBAC / no-pod / dial failures invisible: the WebSocket tunnel handlers discarded the result outright (`_ = backend.ForwardPort/ExecStart/Attach`), and the logs/stats handlers reported the error to the client but logged nothing server-side. For a raw TCP port-forward the tunnel is a passthrough with no post-preamble error channel, so a setup failure only manifested as the tunnel silently closing — with no record anywhere. See [[kubernetes-backend]] for the RBAC/backend context.

- Server (`pkg/server/deploy_exec.go`, `pkg/server/deploy.go`): `logStreamHandlerErr(r, op, name, err)` logs at WARN for a genuine fault, at DEBUG when the request context is cancelled (routine client Ctrl-C / closed forward, so no warning spam). Wired into port-forward, exec-start, attach, logs, and stats; the UDP-reject path also logs its ack rejection. Logs/stats still return the pre-output error to the client as before — the server log is the additional record, and the *only* record for a mid-stream or raw-tunnel failure.
- Invariant: a TCP forward cannot report a mid-connection setup error back to the client, so the server log is the sole reporting channel. This is another reason the CLI prefers the direct-to-pod path for cluster profiles.
- Client fallback used to hide the direct attempt, so a cluster user whose own kubeconfig read failed silently saw only a puzzling server-ServiceAccount error. `streamServiceLogs` now WARNs on the direct-read failure and, when the proxy fallback ALSO fails, returns a combined error naming both attempts; `kubefwd.Fallback` does the same for port-forward.

### Follow-ups (completed 2026-07-05, per TODO.md)

- Backend-client spans: the kubernetes client-go transport (`rest.Config.WrapTransport`) and the dockerhost Docker-socket `http.Client` transport are wrapped with `otelhttp.NewTransport`, gated on `observability.Enabled()` (no wrap when off).
- Opt-in Prometheus pull `/metrics`: `CORNUS_METRICS_PROMETHEUS` (requires telemetry enabled) adds a Prometheus exporter as an additional metric reader over its own registry (OTLP push untouched); `/metrics` is registered on the mux only when active and is auth-exempt. Zero-cost when off (no reader/handler/route/goroutine). Deps: `prometheus/client_golang`, `otel/exporters/prometheus`.

## Files

- `pkg/observability/observability.go` — `Setup`/`Providers`/`Enabled`/`Logger`, propagator and provider installation, otelslog bridge hookup. (The journal calls this `internal/observability`; the actual path is `pkg/observability`.)
- `pkg/logging/logging.go` — `Init`, `InitWith`, `fanout` handler, `CORNUS_LOG_LEVEL` handling.
- `cmd/cornus/main.go`, `pkg/client/client.go` — CLI root span, command baggage, instrumented REST
  transport, and traced WebSocket upgrades.
- `pkg/e2e/otlp_collector.go`, `e2e/scenarios/observability-trace*.star` — header injection and
  exported client/server parentage coverage.
- `pkg/server/observability.go` — `otelhttp` wrapping, `routePattern` formatter, `traceDeploy`, Cornus instruments; `cornus.mount.relay` span per relayed mount stream (session digest, transport local|forwarded, rx/tx, `span.IsRecording()` fast path); `dialForward` traceparent injection for cross-replica `/.cornus/v1/mount/forward` linking.
- `pkg/caretaker/observability.go` — caretaker per-role instruments; `caretaker.conn` connection span; rx/tx stamped onto `caretaker.mount` spans.
- `pkg/caretaker/dns.go`, `pkg/caretaker/proxy.go`, `pkg/caretaker/hub.go` — instrumented roles; former `log.Printf` sites.
- `pkg/wire/export.go` — `DialConnControlHeader` (WebSocket handshake header injection for trace propagation).
- `pkg/deploy/kubernetes/internal/netdriver/netdriver.go` — `warnf` seam routed to `slog.Warn`.
- `pkg/server/deploy_exec.go`, `pkg/server/deploy.go` — `logStreamHandlerErr` (WARN vs DEBUG-on-cancel) wired into port-forward/exec/attach/logs/stats and the UDP-reject ack path.
- CLI `streamServiceLogs` and `kubefwd.Fallback` — WARN on the direct-to-pod failure and return a combined error naming both attempts when the proxy fallback also fails.
- `cmd/cornus/serve.go` — `--otel` flag, server banner logging.
- `cmd/cornus/caretaker.go`, `cmd/cornus/mountagent.go` — caretaker/mount-agent `Setup` with `service.name=cornus-caretaker`.

## Test Coverage

All hermetic — no collector or network required:

- `pkg/observability/observability_test.go` — gate on/off, idempotent shutdown, enabled path with `none` exporters.
- `pkg/server/observability_test.go` — `routePattern` table test; span emission through the wrapped handler with a `tracetest.SpanRecorder`, asserting both the `POST /.cornus/v1/deploy` HTTP span and its child `cornus.deploy.apply` span.
- `pkg/caretaker/mount_metrics_test.go` — caretaker mount instruments.
- `pkg/logging/logging_test.go` — logging package behavior.
- `TestLogStreamHandlerErr` (WARN vs DEBUG-on-cancel vs nil), composecli `TestStreamLogsKubeAndProxyBothFail`, kubefwd `TestFallbackBothFail` — streaming/tunnel error logging and the two-attempt combined-error fallback.
- Live smoke (manual): `OTEL_TRACES_EXPORTER=console` showed route-templated HTTP spans and a `cornus.deploy.list` span correctly nested under `GET /.cornus/v1/deploy` (same TraceID, correct parent, error status propagated); the default no-env run emits nothing. `OTEL_LOGS_EXPORTER=console` confirmed the slog fan-out: one record on stderr and the same record as an OTel console log record.
- `pkg/logging` tests cover attribute expansion and context hooks; client and observability tests
  cover W3C traceparent plus baggage injection and the disabled path. The Starlark OTLP scenario
  proves real server-side parenting on backend-capable targets.

## Pitfalls

- `otelhttp` alone breaks handlers that need `http.Hijacker`/`http.Flusher` (WebSocket attach, streaming); `httpsnoop` must wrap so those interfaces are preserved.
- Span names must not embed digests, deployment names, or upload UUIDs — use the `routePattern` route-template formatter, or metric/span cardinality explodes.
- `CORNUS_OTEL` and any `OTEL_*` exporter/endpoint var both enable telemetry, but `OTEL_SDK_DISABLED` wins over everything. The `serve --otel` flag works by setting `CORNUS_OTEL` via `os.Setenv` so both gates agree — keep that invariant if adding new enable paths.
- Keep `pkg/logging` free of OTel imports; it is deliberately dependency-light so CLI clients don't link the OTel tree. The OTel handler is injected only from `observability.Setup` via `InitWith`.
- Distinguish logging from output: stdout program results (`version`, tables, `deployed ...`) stay `fmt`; only diagnostics go through slog. Don't "fix" them to slog.
- Span attributes must never carry the raw mount session id — it is an unguessable capability; record the sha256 digest only (the same identifier the mount routing records use).
- Per-stream instrumentation on hot relay paths must be gated on `span.IsRecording()` and return the ORIGINAL conn when off — wrapping unconditionally would tax every mount stream even with telemetry disabled.
- Never `_ =` a backend streaming result (`backend.ForwardPort/ExecStart/Attach`): a raw TCP forward has no post-preamble error channel, so a discarded setup error (RBAC, no pod, dial failure) becomes an invisible silent tunnel close. Route it through `logStreamHandlerErr` — WARN for a real fault, DEBUG when the request context is cancelled so routine client disconnects don't spam warnings.
- When the CLI falls back from direct-to-pod to the server proxy, surface BOTH attempts: a silent direct-path failure otherwise masks itself behind a confusing server-ServiceAccount error from the proxy path.
- Do not store a built `*slog.Logger` in context and expect to merge it later: slog does not expose
  its accumulated attributes. Store mergeable attributes and derive the logger at the emit scope.
- Use a closed `slog.Group(name, attrs...)` for component identity. `WithGroup(name)` also nests all
  later call attributes, including `error`, and an empty closed group disappears entirely.
