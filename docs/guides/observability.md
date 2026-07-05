# Observability

Cornus exposes OpenTelemetry traces, metrics, and logs, an optional Prometheus
scrape endpoint, and liveness/readiness probes. All telemetry is **opt-in and
zero-cost when off** — nothing installs and no exporter goroutines start until you
enable it, so instrumented call sites cost effectively nothing in the default
configuration.

For the design (what is instrumented and how spans propagate across the caretaker
rendezvous), see the [architecture overview](/architecture/). Every variable below is
in the [server env vars](/reference/server-env-vars) reference.

## Enable OpenTelemetry

Install trace, metric, and log providers driven entirely by the standard `OTEL_*`
environment — there is no Cornus-specific exporter config surface.

```sh
# Turn it on by pointing at a collector — any OTEL_* var enables it:
OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317 cornus serve

# Or force it on with the SDK defaults:
cornus serve --otel                       # equivalent to CORNUS_OTEL=1
```

- Telemetry installs only when `CORNUS_OTEL` is truthy or a standard `OTEL_*`
  variable is set, and never when `OTEL_SDK_DISABLED=true` wins. When disabled,
  setup is a no-op and the OpenTelemetry API stays at its no-op default.
- Configure exporters, sampling, and endpoints through the usual `OTEL_*` vars
  (`OTEL_EXPORTER_OTLP_*`, `OTEL_TRACES_EXPORTER`, `OTEL_TRACES_SAMPLER`, ...).
- The service identity is `cornus` for the server and `cornus-caretaker` for the
  per-pod sidecar. A caretaker connection span and the server-side attach span form
  a single end-to-end trace across the rendezvous.

## What gets instrumented

- **HTTP** — an `otelhttp` layer wraps the server mux with a server span and
  standard HTTP metrics per request. High-cardinality paths (digests, deployment
  names, upload UUIDs) collapse to route templates so series don't explode, and
  streaming / WebSocket endpoints keep working.
- **Build and deploy** — the build and deploy handlers add their own Cornus spans
  and metrics on top of the automatic HTTP layer.
- **Caretaker** — per-role instruments for mount sessions, proxy connections and
  bytes, and DNS queries; per-mount RX/TX bytes are metered at the 9P transport
  boundary.

## Scrape metrics with Prometheus

Add a pull-based Prometheus endpoint alongside the OTLP push pipeline. It registers
an auth-exempt `/metrics` route only when active, and is only effective when
OpenTelemetry is enabled.

```sh
CORNUS_METRICS_PROMETHEUS=1 cornus serve --otel
# then scrape http://<server>:5000/metrics
```

## Logs

All processes log through `log/slog`. The server and caretaker layer OTLP log
export on top, so a single `slog.Info` reaches both the console and the OTLP logs
pipeline when telemetry is on. Set the level with `CORNUS_LOG_LEVEL`.

```sh
CORNUS_LOG_LEVEL=debug cornus serve --otel
```

## Workload telemetry

Everything above instruments Cornus itself. To collect *your workload's* telemetry,
Cornus can run an embedded **OpenTelemetry Collector** inside the per-pod caretaker
(a companion container on the host backends) and auto-wire the app to it: the app
sends OTLP to `127.0.0.1`, the Collector batches and exports it to your backend, and
Cornus injects the `OTEL_*` env so an OpenTelemetry SDK needs zero configuration. It
works on every backend (Kubernetes, dockerhost, containerd, bare).

Turn it on per service in Compose:

```yaml
services:
  web:
    image: web:latest
    x-cornus-telemetry:
      endpoint: otel.example.com:4317   # your OTLP backend (required)
      # protocol: http/protobuf         # default grpc
      # insecure: true                  # plaintext / skip TLS verify
      # signals: [traces, metrics]      # default: all three
      # headers:                        # e.g. an auth token (projected via a
      #   authorization: Bearer <token> #   Secret on Kubernetes, not the pod spec)
```

Put the block at the **project level** to enable it for every service with one
endpoint (a per-service block overrides it):

```yaml
name: myproj
x-cornus-telemetry:
  endpoint: otel.example.com:4317
services:
  web: { image: web:latest }
  api: { image: api:latest }
```

Or from the command line, on `cornus deploy` and `cornus compose up`:

```sh
cornus compose up --telemetry-endpoint otel.example.com:4317
cornus deploy -f app.yaml --telemetry-endpoint https://otel.example.com \
  --telemetry-protocol http/protobuf --telemetry-header "authorization=Bearer $TOKEN"
```

The app container is auto-wired with `OTEL_EXPORTER_OTLP_ENDPOINT` (pointing at the
loopback receiver), `OTEL_EXPORTER_OTLP_PROTOCOL`, and — unless you set them —
`OTEL_SERVICE_NAME` (the deployment name) and `OTEL_RESOURCE_ATTRIBUTES`. Any
`OTEL_*` you set yourself is left untouched.

::: tip Requires the collector in the sidecar image
The embedded Collector is compiled into the released Cornus image. A Cornus you
built yourself needs the `otelcol` build tag (`go build -tags otelcol`), otherwise
the caretaker reports the collector as not compiled in and the workload's startup
probe fails. This is distinct from `CORNUS_OTEL` above, which controls Cornus's own
telemetry.
:::

## Health and readiness probes

The liveness and readiness endpoints stay open even under auth, so probes and load
balancers can reach them without a token.

```sh
# From a script or another host:
curl -fsS http://localhost:5000/healthz
curl -fsS http://localhost:5000/readyz

# In-image healthcheck with no extra tools (Dockerfile):
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```

- `cornus health` GETs `/healthz` (5s timeout) and exits non-zero unless the server
  returns `200 OK` — a container healthcheck that needs no `curl` in the image.
- The shipped Kubernetes manifest wires `/healthz` (liveness) and `/readyz`
  (readiness) directly.

**See also:** [server env vars](/reference/server-env-vars) · [cornus serve](/cli/serve) · [cornus health](/cli/version-health) · [installation](/introduction/installation) · [architecture](/architecture/)
