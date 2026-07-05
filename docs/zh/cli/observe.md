# cornus observe

从服务器的内置可观测性存储中查询**工作负载**已记录的遥测数据 (日志、追踪、指标)。

## 用法

```sh
cornus observe logs    [flags]
cornus observe traces  [flags]
cornus observe trace   <trace-id> [flags]
cornus observe metrics <promql> [flags]
cornus observe query   <sql> [flags]
cornus observe status  [flags]
```

## 说明

有三个命令回答"发生了什么", 值得先说清楚它们各自的分工:

| 命令 | 关注谁的行为 | 范围 |
|---|---|---|
| [`cornus activity`](/zh/cli/activity) | **Cornus 自身** —— 服务器及其 caretaker | 单台服务器的飞行记录 |
| [`cornus compose logs`](/zh/cli/compose) | 你的工作负载 | 单个项目的服务, 以 tail 形式 |
| `cornus observe` | 你的工作负载 | 存储中的全部内容, 外加追踪和指标 |

`cornus observe` 横跨服务器记录过的每一个工作负载读取数据, 并提供日志 tail 从根本上无法承载的两样东西: 分布式追踪和指标序列。

它需要以 `--obs` 启动的服务器 (参见[可观测性](/zh/guides/observability#内置存储))。没有这样的服务器时, 每个子命令都会以指明补救方法的消息失败, 而不是返回空结果 —— 空结果会被读作"什么都没发生", 那是另一个且更具误导性的答案。

## 命令

### `cornus observe logs`

跨所有工作负载搜索已记录的日志。与实时 tail 不同, 这些记录比产生它们的容器存活得更久, 并且可以搜索。

```sh
# checkout 在过去两小时说了什么?
cornus observe logs --service checkout --since 2h

# 在任意工作负载中查找失败
cornus observe logs --match "connection refused"

# 只看错误
cornus observe logs --severity error

# 属于同一次请求的所有日志行
cornus observe logs --trace 4bf92f3577b34da6a3ce929d0e0e4736
```

| 标志 | 说明 |
|---|---|
| `--service` | 只看该工作负载 (部署名称)。 |
| `--match` | 只看正文包含该文本的记录 (全文检索)。 |
| `--severity` | 只看不低于 `debug`、`info`、`warn`、`error`、`fatal` 的记录。 |
| `--stream` | 只看 `stdout` 或 `stderr`。 |
| `--trace` | 只看关联到该 trace id 的记录。 |
| `--since` / `--until` | 时间范围: RFC3339、Unix 秒, 或 `2h` 这样的时长。 |
| `--limit` | 最大记录数 (默认 200)。 |
| `--oldest` | 返回最早的匹配记录而非最新的。 |

记录按由旧到新打印。除非指定 `--oldest`, 否则 `--limit` 保留**最新的**匹配项。

### `cornus observe traces`

在追问原因之前, 先找出*哪些*请求慢了或失败了。

```sh
# 慢的
cornus observe traces --service checkout --min-duration 500ms

# 坏的
cornus observe traces --status error --since 1h
```

| 标志 | 说明 |
|---|---|
| `--service` | 只看包含该工作负载 span 的追踪。 |
| `--name` | 只看包含该名称 span 的追踪, 例如 `GET /checkout`。 |
| `--status` | 只看具有该 span 状态的追踪, 例如 `error`。 |
| `--kind` | `server`、`client`、`producer`、`consumer` 或 `internal`。 |
| `--min-duration` / `--max-duration` | 时长范围, 例如 `500ms`。 |
| `--since` / `--until` | 追踪开始时间的范围。 |
| `--limit` | 最大追踪数 (默认 50)。 |

### `cornus observe trace`

以瀑布图展示单条追踪, 从而看清时间花在哪里、哪个服务最先失败。

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

父 span 未被记录的 span 仍会作为根显示。追踪只被部分采集的时候, 正是有人在读它的时候, 因此不会丢掉任何 span。

### `cornus observe metrics`

对工作负载导出的指标执行 PromQL 范围查询。

```sh
cornus observe metrics 'rate(http_requests_total[5m])' --since 6h --step 1m
```

| 标志 | 默认 | 说明 |
|---|---|---|
| `--since` | `1h` | 范围起点。 |
| `--until` | 当前时间 | 范围终点。 |
| `--step` | `1m` | 返回序列的分辨率。 |

OpenTelemetry 的指标名会映射到 Prometheus 的写法: 点号变为下划线, 因此 `http.server.duration` 要写作 `http_server_duration` 来查询。**没有单位后缀** —— 是 `container_cpu_time`, 而不是 `container_cpu_time_seconds_total`。超出所支持 PromQL profile 的构造会带着诊断信息被拒绝, 而不是被近似处理。

#### Cornus 自动为你记录的指标

启用 `--obs` 后, 即使工作负载不导出任何东西, 这些指标也会存在。下面的名称是 PromQL 写法, 标签按查询时的写法列出。

| 指标 | 单位 | 标签 | 含义 |
|---|---|---|---|
| `container_cpu_time` | 秒 | `cornus_replica`, `cpu_mode` | 累积 CPU 时间。请配合 `rate()` 使用。Kubernetes 上不可用, 那里记录的是下面那个速率。 |
| `container_cpu_usage` | 核 | `cornus_replica` | 瞬时 CPU。仅限没有累积数据来源的 Kubernetes。 |
| `container_memory_usage` | 字节 | `cornus_replica` | 使用中的内存, 不含可回收的页缓存 —— 与 `docker stats` 显示的数值相同。 |
| `container_network_io` | 字节 | `cornus_replica`, `network_io_direction`, `network_interface_name` | 累积流量。Kubernetes 上不可用。 |
| `container_disk_io` | 字节 | `cornus_replica`, `disk_io_direction` | 累积块 I/O。Kubernetes 与 Incus 上不可用。 |
| `cornus_container_memory_limit` | 字节 | `cornus_replica` | 存在限制时所强制的上限。在 Kubernetes 上, 它来自 Pod spec, 而不是 metrics-server。 |
| `cornus_container_pids` | 个数 | `cornus_replica` | 进程与线程数。Kubernetes 上不可用。 |

每个指标都带有 `service`, 即部署名称。

在你的后端上被标注为"不可用"的指标, 产生的是**完全没有序列**, 而不是一串 0: `container_network_io` 在 Kubernetes 上保持沉默, 是因为 Cornus 无法观测该工作负载是否传输过字节, 这与"它没有传输任何字节"是不同的主张。`cornus observe status` 会在 `metrics.unsupported` 下列出它们, 而 [`cornus web`](/zh/guides/web-ui#指标仪表板) 的仪表板会隐藏这些面板, 而不是把它们画成永远空白的图。

服务器自身的用量也记录在一起: `process_cpu_time`、`process_memory_usage`、`process_memory_virtual`、`process_thread_count`、`process_open_file_descriptor_count`、`process_disk_io`, 以及 Go 运行时指标 (`go_goroutine_count`、`go_memory_used` 等) 和 Cornus 自身的计数器 (`cornus_builds`、`cornus_deploys`)。

两个计数器都带有 `outcome` 标签 (`ok` / `error`)，`cornus_deploys` 还带有 `action`。`cornus_deploys` 只统计会**改变**部署的操作: `apply`、`delete`、`volume-delete`、`start`、`stop`、`restart`。只读请求 (`list`、`status`) 会被追踪但不计数，因为它们正是客户端会轮询的操作: 统计它们会让这个数字反映有多少个面板开着，而不是部署了多少东西。

`cornus_builds` 统计服务器受理的每一次构建，覆盖全部四条路径: tar 上传、`cornus build` 会话，以及把工作交给[容器化 builder](/zh/reference/server-env-vars#delegating-builds-to-a-builder) 的两条路径。具体是哪一种由 `delegated` 标签说明。当 `delegated="false"` 时，`outcome` 就是构建自身的结果；当 `delegated="true"` 时则不是: 构建结果位于服务器原样转发、并不解析的流中，因此此时的 `outcome` 表示调用方是否成功到达了 builder。`cornus_build_duration` 是与之对应的直方图，遵循同样的规则。

`cornus_server_network_io` 的范围是网络命名空间而非单个进程: 在容器中它是服务器的流量, 但在主机安装上它是整台主机的流量。它使用 `cornus_` 前缀而不是语义约定中的 `process.network.io`, 正是为了不宣称自己是那个名称所承诺的按进程统计的数值。

::: tip 标签名使用下划线, 而不是点号
是 `cornus_replica`, 不是 `cornus.replica`。Prometheus 的数据模型中标签名无法容纳点号, 存储的 PromQL 也无法表达它, 因此 Cornus 输出下划线写法。使用点号的匹配器会返回 **零个序列且不报错**, 这是最令人困惑的一种失败方式 —— 如果某个过滤条件悄无声息地什么都匹配不到, 请先检查这一点。
:::

::: warning 直方图需要用 SQL 查询
直方图指标 (`http.server.request.duration` 之类) 会被记录, 但存储的 PromQL profile 无法按名称选中它们。请改用 [`cornus observe query`](#cornus-observe-query) 读取:

```sh
cornus observe query "SELECT metric, count, sum FROM metrics_histogram ORDER BY time DESC LIMIT 10"
```
:::

### `cornus observe query`

面向类型化命令覆盖不到的问题的原始 SQL。

```sh
cornus observe query 'SELECT service, count(*) AS n FROM logs GROUP BY service'
```

表: `logs`、`spans`、`metrics_gauge`、`metrics_sum`、`metrics_histogram`、`metrics_exp_histogram`、`metrics_summary`。可用的 UDF 包括 `histogram_quantile`、`matches` (全文检索) 和 `json_get_str`。

### `cornus observe status`

报告存储保存了什么, 以及是否正在丢失数据。

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

在根据空的搜索结果断定什么都没发生之前, 先看看这里。`dropped` 不为零意味着存储在压力下丢弃了记录, 因此证据可能是丢失了, 而不是从未存在。

`metrics` 这一块用于区分三种在查询返回空结果时看起来完全相同的失败: `sampling 0 replica(s)` 表示没有部署任何东西; `FAILED` 不为零表示后端拒绝了 (请检查 `cornus daemon preflight` —— 在 Kubernetes 上这通常是缺少 `metrics.k8s.io` 授权); `DROPPED` 不为零表示读数已经取到, 但随后在压力下被丢弃了。

`repeated` 这一行不是失败。它统计后端原样重复返回的读数, 记录器会跳过它们而不是写入两次。在 Kubernetes 上这在预期之内, 而且通常数值很大: 数据源是 metrics-server, 其抓取窗口 (15-30 秒) 比采样间隔更粗, 因此多次轮询会观测到同一个读数。它也诚实地解释了为什么序列会比 `--obs-metrics-interval` 所暗示的更稀疏 —— 分辨率取决于数据源发布的内容, 所以 `repeated` 很大而 `recorded` 很小, 意味着放宽间隔不会损失任何东西。

## 通用标志

每个子命令都接受 `--server` (`CORNUS_SERVER`) 来显式指定服务器; 否则使用所选的[连接配置文件](/zh/cli/config)。

`--output json` 直接输出记录本身: `logs`、`traces`、`trace`、`metrics`、`query` 输出数组, `status` 输出对象, 因此结果可以直接管道给 `jq`。

## 另请参阅

**另请参阅:** [可观测性](/zh/guides/observability) · [cornus activity](/zh/cli/activity) · [cornus compose](/zh/cli/compose) · [cornus serve](/zh/cli/serve)
