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
The embedded Collector is compiled into every released Cornus binary and the
published image. A Cornus you built yourself needs the `otelcol` build tag
(`go build -tags otelcol`), otherwise the caretaker reports the collector as not
compiled in and the workload's startup probe fails. Check with
`cornus version --features`. This is distinct from `CORNUS_OTEL` above, which
controls Cornus's own telemetry.
:::

## The built-in store

Everything above sends your telemetry somewhere else, which only helps if you
already run Grafana, Datadog, or Honeycomb. Cornus can also **be** that
somewhere: start the server with `--obs` and it keeps a local observability
database of your workloads' logs, traces, and metrics.

```sh
cornus serve --obs
```

Two things start working, and they are worth separating because one of them
needs nothing from you at all.

### Logs, with no setup whatsoever

With the store on, Cornus records every managed workload's stdout and stderr.
Your app needs no OpenTelemetry SDK, no sidecar, and no configuration — Cornus
reads the same container output `compose logs` already shows and keeps it.

The difference is that it keeps it *after the container is gone*:

```sh
cornus compose up -d
cornus compose down

# The container no longer exists. Its output does.
cornus compose logs web --from=store --since 1h
```

You also get searching, which a live log stream fundamentally cannot do — it
hands over bytes, not records:

```sh
cornus compose logs web --match "connection refused"
cornus compose logs web --severity error
```

`--match` and `--severity` imply `--from=store`. The default, `--from=auto`,
reads the live runtime and falls back to the store only when the runtime has
nothing to say — so it never returns fewer lines than before.

Every replica is recorded, and each record carries its instance ordinal:

```sh
cornus observe logs --service web --replica 1   # just that instance
cornus compose logs web --all-replicas          # live, every instance, tagged
```

`compose logs` still shows a single instance by default, so nothing about the
familiar output changed; `--all-replicas` opts into the fan-out.

### Resource usage, also with no setup whatsoever

The same is true of CPU, memory, network, and disk. Cornus samples every managed
workload's resource usage on a timer and records it, so `docker stats` stops
being the only answer and you can ask what a workload was doing an hour ago:

```sh
# Memory, per replica, over the last six hours
cornus observe metrics 'container_memory_usage' --since 6h

# CPU as a rate, which is what you actually want to look at
cornus observe metrics 'rate(container_cpu_time[5m])' --since 6h

# Just one replica
cornus observe metrics 'container_memory_usage{cornus_replica="1"}'
```

Every replica is sampled separately and carries its ordinal as the
`cornus_replica` label, so you can compare instances rather than seeing one
number that hides an imbalance.

The metric names follow OpenTelemetry's container semantic conventions, so a
Grafana dashboard written against any OTel-instrumented system works here
unchanged. See [`cornus observe metrics`](/cli/observe#cornus-observe-metrics)
for the full list.

The server records **its own** usage the same way, under `process_*`, next to
the workloads it is running:

```sh
cornus observe metrics 'process_memory_usage'
cornus observe metrics 'go_goroutine_count'
```

If you would rather look than query, the same samples are charted on the
**Metrics** screen of [`cornus web`](/cli/web#metrics-dashboard) — CPU, memory,
network, disk, and process counts per replica, plus the server's own usage, with
the cumulative counters already differentiated into rates.

Sampling runs every 15 seconds by default. Turn the cadence up or the whole
thing off:

```sh
cornus serve --obs --obs-metrics-interval 5s
cornus serve --obs --no-obs-record-metrics
```

::: warning Kubernetes reports less
On the Kubernetes backend the numbers come from `metrics.k8s.io`, so
**metrics-server must be installed** and only CPU and memory are available —
there are no network or disk counters. The fuller set lives behind the kubelet's
Summary API, which needs a `nodes/proxy` grant that reaches every kubelet in the
cluster; that is not a reasonable trade for two more metric families.

Missing families are simply absent rather than reported as zero, because "this
container moved no bytes" and "Cornus cannot see whether it did" are different
claims. Run `cornus daemon preflight` to check the RBAC grant.
:::

### Traces and metrics, with one line

Stdout cannot carry a trace. For those, point your app's telemetry at Cornus —
by writing **nothing but an empty block**:

```yaml
services:
  web:
    image: web:latest
    x-cornus-telemetry: {}   # no endpoint: export to Cornus itself
```

Cornus fills in its own OTLP receiver as the endpoint, so the embedded Collector
ships your app's traces and metrics into the same store as its logs. They share
`service.name`, so a log line and a span for the same request join up.

Setting an `endpoint:` explicitly still works exactly as before, and wins.

::: tip The server needs an address workloads can reach
Defaulting the endpoint requires `CORNUS_ADVERTISE_URL` — the URL a workload can
reach the server at. Without it Cornus logs a warning and leaves the endpoint
empty rather than wiring an export that would fail silently inside the sidecar.

In practice this requirement rarely bites, because the telemetry
[travels over the Cornus connection](#it-travels-over-the-cornus-connection) by
default — the URL is still needed to name the destination and for the caretaker to
dial, but the *workload's own* network does not have to reach it.
:::

### Forward it on to a real backend

Cornus does not have to be the final destination. Point it at your organization's
OTLP backend and it forwards everything it receives, in addition to storing it:

```sh
cornus serve --obs \
  --obs-export-endpoint https://otlp.example.com \
  --obs-export-header "authorization=Bearer $TOKEN"
```

Your workloads then export **once, to Cornus**. They need no credential for the
upstream and no route to it — both live on the server, configured in one place
instead of in every deploy spec. Locally you keep a short-retention copy for
immediate debugging; upstream keeps the long-term record.

This works with or without the store. With `--obs-export-endpoint` and
`--no-obs`, Cornus is a pure telemetry **gateway** — which even a build with no
`imbh` tag can do, since nothing is stored.

`cornus observe status` reports the forwarder, and distinguishes two failures that
call for different responses:

- **dropped** — Cornus shed records because the forwarder fell behind. The
  upstream is slow; the queue is bounded on purpose so a wedged backend can never
  stall ingest.
- **failed** — the upstream rejected them or was unreachable. Something is broken
  over there.

### It travels over the Cornus connection

When Cornus is the destination, the telemetry does **not** go over the network to
the server's OTLP endpoint. It rides the connection the pod's caretaker already
holds — which needs no reachable URL, no route from the pod, and no credential of
its own, and cannot be broken by a NetworkPolicy the direct dial silently depends
on.

This is **on by default** on every backend, and there is nothing to configure.
The bare `x-cornus-telemetry: {}` above already gets it.

Combined with re-export, this is the useful shape for a restricted cluster: a
workload with **no egress at all** exports over its existing Cornus connection,
and Cornus forwards to a SaaS backend on its behalf.

Force the direct HTTP dial instead — for example to watch the export as ordinary
traffic — with:

```yaml
x-cornus-telemetry:
  via_mux: false
```

or `--no-telemetry-via-mux`. An explicit choice always wins over the default.

It is most valuable where the workload's network has no route back to the server,
and that is not a Kubernetes peculiarity — a
[remote docker host](/guides/remote-docker-hosts) and an isolated container
network hit it just as squarely.

::: info When the default does not apply
It turns itself on only when both hold, because otherwise it would be wrong
rather than merely unnecessary:

- **Cornus is the destination.** With an explicit third-party `endpoint:` there is
  no caretaker connection to that backend to ride, so the Collector dials it
  directly.
- **You did not decide.** `via_mux: false` is respected.

It needs `CORNUS_ADVERTISE_URL` — the URL the caretaker dials — and fails the
deploy with that message if it is unset, rather than falling back to a direct dial
you did not ask for.
:::

### Reading it back

```sh
cornus observe logs --service web --match timeout --since 2h
cornus observe traces --service web --min-duration 500ms
cornus observe trace <trace-id>          # a waterfall of the spans
cornus observe metrics 'rate(http_requests_total[5m])'
cornus observe query 'SELECT service, count(*) FROM logs GROUP BY service'
cornus observe status
```

Run `cornus observe status` before concluding from an empty result that nothing
happened — it reports how many records were **dropped** under load, which is the
difference between "your service was quiet" and "the evidence was shed".

See [cornus observe](/cli/observe) for the full command reference.

### Point Grafana at it

Cornus answers the Grafana datasource APIs directly, because the query languages
are already implemented. Add three datasources, no proxy or exporter in between:

| Datasource | URL |
|---|---|
| Prometheus | `http://<server>:5000/.cornus/v1/obs/prom` |
| Loki | `http://<server>:5000/.cornus/v1/obs/loki` |
| Tempo | `http://<server>:5000/.cornus/v1/obs/tempo` |

Enough of each API is served for a range query and a trace view. A query using a
construct Cornus does not support is rejected with a diagnostic rather than
approximated, so a panel either shows correct data or tells you why it cannot.

### Retention

| Flag | Env | Default | Description |
|---|---|---|---|
| `--obs` | `CORNUS_OBS` | `false` | Enable the store. |
| `--obs-dir` | `CORNUS_OBS_DIR` | `<data-dir>/observability` | Where the database lives. |
| `--obs-retention` | `CORNUS_OBS_RETENTION` | `168h` (7 days) | Drop records older than this. Rounded up to whole days. |
| `--obs-max-bytes` | `CORNUS_OBS_MAX_BYTES` | `536870912` (512 MiB) | On-disk size cap. |
| `--obs-record-logs` | `CORNUS_OBS_RECORD_LOGS` | `true` | Record workload stdout/stderr. `--no-obs-record-logs` turns it off. |
| `--obs-record-metrics` | `CORNUS_OBS_RECORD_METRICS` | `true` | Sample workload and server resource usage. `--no-obs-record-metrics` turns it off. |
| `--obs-metrics-interval` | `CORNUS_OBS_METRICS_INTERVAL` | `15s` | How often each replica is sampled. |

::: tip Included out of the box
The store ships in every released binary and in the published image, and `--obs`
defaults to **on** wherever it is compiled in — so a downloaded `cornus serve`
starts recording with no flags. Confirm with:

```sh
cornus version --features   # obsstore: yes
```

Turn it off with `--no-obs` (or `CORNUS_OBS=0`).

The store is an embedded Rust database reached over cgo, so a Cornus you build
yourself compiles a stub unless you ask for it:

```sh
eval "$(go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@v0.3.0 -print-env)"
CGO_ENABLED=1 go build -tags "netgo osusergo otelcol imbh sable_extern_lib" ./cmd/cornus
```

Such a build reports `obsstore: no`, leaves `--obs` off by default, and — if you
pass `--obs` explicitly — logs that the store is not compiled in rather than
silently recording nothing.
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
