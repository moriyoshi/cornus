# cornus observe

Query a **workload's** recorded telemetry — logs, traces, and metrics — from the
server's built-in observability store.

## Synopsis

```sh
cornus observe logs    [flags]
cornus observe traces  [flags]
cornus observe trace   <trace-id> [flags]
cornus observe metrics <promql> [flags]
cornus observe query   <sql> [flags]
cornus observe status  [flags]
```

## Description

Three commands answer "what happened", and it is worth being clear about which
is which:

| Command | Whose behavior | Scope |
|---|---|---|
| [`cornus activity`](/cli/activity) | **Cornus's own** — the server and its caretakers | one server's flight records |
| [`cornus compose logs`](/cli/compose) | your workloads | one project's services, as a tail |
| `cornus observe` | your workloads | everything the store holds, plus traces and metrics |

`cornus observe` reads across every workload the server has recorded, and adds
the two things a log tail fundamentally cannot carry: distributed traces and
metric series.

It needs a server started with `--obs` (see
[Observability](/guides/observability#the-built-in-store)). Without one, every
subcommand fails with a message naming the remedy rather than returning an empty
result — an empty result would read as "nothing happened", which is a different
and much more misleading answer.

## Commands

### `cornus observe logs`

Search recorded log records across every workload. Unlike a live tail these
survive the container that produced them, and they can be searched.

```sh
# What did checkout say in the last two hours?
cornus observe logs --service checkout --since 2h

# Find a failure anywhere, in any workload
cornus observe logs --match "connection refused"

# Only errors
cornus observe logs --severity error

# Every log line belonging to one request
cornus observe logs --trace 4bf92f3577b34da6a3ce929d0e0e4736
```

| Flag | Description |
|---|---|
| `--service` | Only this workload (the deployment name). |
| `--match` | Only records whose body contains this text (full-text). |
| `--severity` | Only records at or above `debug`, `info`, `warn`, `error`, or `fatal`. |
| `--stream` | Only `stdout` or `stderr`. |
| `--trace` | Only records correlated to this trace id. |
| `--since` / `--until` | Time bounds: RFC3339, Unix seconds, or a duration like `2h`. |
| `--limit` | Maximum records (default 200). |
| `--oldest` | Return the oldest matching records instead of the most recent. |

Records are printed oldest-first. `--limit` keeps the most **recent** matches
unless `--oldest` is given.

### `cornus observe traces`

Find *which* requests were slow or broken, before asking why.

```sh
# The slow ones
cornus observe traces --service checkout --min-duration 500ms

# The broken ones
cornus observe traces --status error --since 1h
```

| Flag | Description |
|---|---|
| `--service` | Only traces with a span from this workload. |
| `--name` | Only traces with a span of this name, e.g. `GET /checkout`. |
| `--status` | Only traces with this span status, e.g. `error`. |
| `--kind` | `server`, `client`, `producer`, `consumer`, or `internal`. |
| `--min-duration` / `--max-duration` | Duration bounds, e.g. `500ms`. |
| `--since` / `--until` | Time bounds on trace start. |
| `--limit` | Maximum traces (default 50). |

### `cornus observe trace`

Show one trace as a waterfall, so you can see where the time went and which
service failed first.

```sh
cornus observe trace 4bf92f3577b34da6a3ce929d0e0e4736
```

```
trace 4bf92f3577b34da6a3ce929d0e0e4736 — 4 spans over 812.4ms

GET /checkout                              web              812.4ms  ████████████████████████████
  authorize                                auth             120.1ms      ████
  charge                                   payments         640.2ms          ██████████████████████  !Error
    POST /v1/charges                       payments         631.8ms           █████████████████████
```

A span whose parent was never recorded still appears, as a root. A partially
collected trace is exactly when someone is reading this, so no span is dropped.

### `cornus observe metrics`

Evaluate a PromQL range query over metrics your workloads exported.

```sh
cornus observe metrics 'rate(http_requests_total[5m])' --since 6h --step 1m
```

| Flag | Default | Description |
|---|---|---|
| `--since` | `1h` | Start of the range. |
| `--until` | now | End of the range. |
| `--step` | `1m` | Resolution of the returned series. |

OpenTelemetry metric names map to Prometheus spelling: dots become underscores,
so `http.server.duration` is queried as `http_server_duration`. There is **no
unit suffix** — it is `container_cpu_time`, not `container_cpu_time_seconds_total`.
A construct outside the supported PromQL profile is rejected with a diagnostic
rather than approximated.

#### Metrics Cornus records for you

With `--obs`, these exist without the workload exporting anything. Names below
are the PromQL spelling; labels are shown as they are queried.

| Metric | Unit | Labels | Meaning |
|---|---|---|---|
| `container_cpu_time` | seconds | `cornus_replica`, `cpu_mode` | Cumulative CPU time. Use `rate()`. Not available on Kubernetes, which reports the rate below instead. |
| `container_cpu_usage` | cores | `cornus_replica` | Instantaneous CPU. Kubernetes only, where no cumulative source exists. |
| `container_memory_usage` | bytes | `cornus_replica` | Memory in use, excluding reclaimable page cache — the same figure `docker stats` shows. |
| `container_network_io` | bytes | `cornus_replica`, `network_io_direction`, `network_interface_name` | Cumulative traffic. Not available on Kubernetes. |
| `container_disk_io` | bytes | `cornus_replica`, `disk_io_direction` | Cumulative block I/O. Not available on Kubernetes or Incus. |
| `cornus_container_memory_limit` | bytes | `cornus_replica` | The enforced limit, when there is one. On Kubernetes it comes from the pod spec, not from metrics-server. |
| `cornus_container_pids` | count | `cornus_replica` | Processes and threads. Not available on Kubernetes. |

Every metric carries `service`, the deployment name.

A metric marked "not available" on your backend produces **no series at all**,
not a series of zeros: `container_network_io` is silent on Kubernetes because
Cornus cannot see whether the workload moved bytes, which is a different claim
from it having moved none. `cornus observe status` names them under
`metrics.unsupported`, and the [`cornus web`](/guides/web-ui#metrics-dashboard)
dashboard hides those panels rather than drawing them permanently empty.

The server's own usage is recorded alongside them: `process_cpu_time`,
`process_memory_usage`, `process_memory_virtual`, `process_thread_count`,
`process_open_file_descriptor_count`, `process_disk_io`, plus the Go runtime
metrics (`go_goroutine_count`, `go_memory_used`, …) and Cornus's own counters
(`cornus_builds`, `cornus_deploys`).

Both counters carry an `outcome` label (`ok` / `error`), and `cornus_deploys` also
carries `action`. `cornus_deploys` counts only the actions that CHANGE a
deployment — `apply`, `delete`, `volume-delete`, `start`, `stop`, `restart`.
Read-only requests (`list`, `status`) are traced but not counted, because they are
what a client polls: counting them made the figure track how many dashboards were
open rather than how much was deployed.

`cornus_builds` counts every build the server served, on all four routes — the tar
upload, the `cornus build` session, and the two paths that hand the work to a
[containerized builder](/reference/server-env-vars#delegating-builds-to-a-builder). It carries a `delegated`
label saying which happened. For `delegated="false"` the `outcome` is the build's
own. For `delegated="true"` it is not: the compile result travels in-band in a
stream the server forwards without parsing, so `outcome` there says whether the
caller reached a builder at all. `cornus_build_duration` is the matching histogram
and follows the same rule.

`cornus_server_network_io` is namespace-scoped rather than per-process: in a
container it is the server's traffic, but on a host install it is the whole
host's. It carries a `cornus_` prefix instead of semconv's `process.network.io`
so it does not claim to be the per-process figure that name promises.

::: tip Label names use underscores, not dots
`cornus_replica`, not `cornus.replica`. Prometheus's data model has no place for
a dot in a label name and the store's PromQL cannot express one, so Cornus emits
the underscored spelling. A matcher using dots returns **zero series and no
error**, which is the most confusing possible failure — if a filter silently
matches nothing, check this first.
:::

::: warning Histograms need SQL
Histogram metrics (`http.server.request.duration` and friends) are recorded, but
the store's PromQL profile cannot select them by name. Read them with
[`cornus observe query`](#cornus-observe-query) instead:

```sh
cornus observe query "SELECT metric, count, sum FROM metrics_histogram ORDER BY time DESC LIMIT 10"
```
:::

### `cornus observe query`

Raw SQL, for questions the typed commands do not cover.

```sh
cornus observe query 'SELECT service, count(*) AS n FROM logs GROUP BY service'
```

Tables: `logs`, `spans`, `metrics_gauge`, `metrics_sum`, `metrics_histogram`,
`metrics_exp_histogram`, `metrics_summary`. UDFs including `histogram_quantile`,
`matches` (full-text), and `json_get_str` are available.

### `cornus observe status`

Report what the store is holding and whether it is losing anything.

```sh
cornus observe status
```

```
directory   /var/lib/cornus/observability
retention   168h0m0s
size cap    512.0 MiB
buffered    50.8 KiB

TABLE                      ROWS   SEGMENTS  OLDEST
logs                        1284          3  2026-07-19T04:11:02Z
spans                        412          1  2026-07-25T22:03:44Z
metrics_gauge               8640          2  2026-07-19T04:11:02Z
metrics_sum                17280          4  2026-07-19T04:11:02Z

metrics     sampling 3 replica(s) every 15s
  recorded  1728 readings

dropped     0
```

Check this before concluding from an empty search that nothing happened. A
non-zero `dropped` means the store shed records under load, so the evidence may
be missing rather than absent.

The `metrics` block distinguishes three failures that all look identical from a
query returning nothing: `sampling 0 replica(s)` means nothing is deployed, a
non-zero `FAILED` means the backend refused (check `cornus daemon preflight` —
on Kubernetes this is usually the missing `metrics.k8s.io` grant), and a non-zero
`DROPPED` means the readings were taken and then shed under load.

A `repeated` line is not a failure. It counts readings the backend re-served
unchanged, which the recorder skips rather than writing twice. On Kubernetes it
is expected and usually large: the source is metrics-server, whose scrape window
(15-30s) is coarser than the sampling interval, so several polls observe the same
reading. It is also the honest explanation for a series that is sparser than
`--obs-metrics-interval` suggests — resolution is bounded by what the source
publishes, so a high `repeated` count next to a low `recorded` one means the
interval could be relaxed at no cost.

## Global flags

Every subcommand accepts `--server` (`CORNUS_SERVER`) to name a server
explicitly; otherwise the selected [connection profile](/cli/config) is used.

`--output json` emits the records themselves as JSON — an array for `logs`,
`traces`, `trace`, `metrics`, and `query`; an object for `status` — so results
pipe straight into `jq`.

## See also

**See also:** [Observability](/guides/observability) · [cornus activity](/cli/activity) · [cornus compose](/cli/compose) · [cornus serve](/cli/serve)
