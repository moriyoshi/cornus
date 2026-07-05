# 可观测性

Cornus 提供 OpenTelemetry trace、metric 和 log、可选 Prometheus scrape endpoint，以及 liveness/readiness probe。所有 telemetry 都是**可选的，关闭时没有成本**: 在您启用前不会安装任何内容，也不会启动 exporter goroutine，因此默认配置下埋点调用点几乎没有成本。

设计细节 (哪些内容被埋点以及 span 如何跨 caretaker rendezvous 传播) 请参见[架构概览](/zh/architecture/)。下文所有变量均列在[服务器环境变量](/zh/reference/server-env-vars)参考中。

## 启用 OpenTelemetry

完全通过标准 `OTEL_*` 环境变量安装 trace、metric 和 log provider；没有 Cornus 特有的 exporter 配置接口。

```sh
# Turn it on by pointing at a collector — any OTEL_* var enables it:
OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317 cornus serve

# Or force it on with the SDK defaults:
cornus serve --otel                       # equivalent to CORNUS_OTEL=1
```

- 仅当 `CORNUS_OTEL` 为真或设置标准 `OTEL_*` 变量时才安装 telemetry；`OTEL_SDK_DISABLED=true` 优先时则永不安装。禁用时 setup 是 no-op，OpenTelemetry API 保持 no-op 默认值。
- 通过通常的 `OTEL_*` var (`OTEL_EXPORTER_OTLP_*`、`OTEL_TRACES_EXPORTER`、`OTEL_TRACES_SAMPLER` 等) 配置 exporter、sampling 和 endpoint。
- 服务 identity 对服务器是 `cornus`，对每 pod sidecar 是 `cornus-caretaker`。caretaker connection span 与服务器侧 attach span 会在 rendezvous 中组成一条端到端 trace。

## 哪些内容被埋点

- **HTTP**——`otelhttp` layer 以每个 request 的 server span 和标准 HTTP metric 包装 server mux。高基数 path (digest、deployment name、upload UUID) 会折叠为 route template，避免 series 膨胀；streaming / WebSocket endpoint 仍可正常工作。
- **Build 和 deploy**——build 和 deploy handler 在自动 HTTP layer 之上添加自己的 Cornus span 和 metric。
- **Caretaker**——按 role 对 mount session、proxy connection 和 byte、DNS query 埋点；每 mount 的 RX/TX byte 在 9P transport boundary 计量。

## 使用 Prometheus scrape metric

在 OTLP push pipeline 旁添加 pull-based Prometheus endpoint。它仅在激活时注册免认证 `/metrics` route，且仅在启用 OpenTelemetry 时生效。

```sh
CORNUS_METRICS_PROMETHEUS=1 cornus serve --otel
# then scrape http://<server>:5000/metrics
```

## 日志

所有进程通过 `log/slog` 记录。服务器和 caretaker 在其上叠加 OTLP log export，因此 telemetry 启用时，一条 `slog.Info` 同时到达 console 与 OTLP logs pipeline。使用 `CORNUS_LOG_LEVEL` 设置级别。

```sh
CORNUS_LOG_LEVEL=debug cornus serve --otel
```

## 工作负载遥测

以上内容都是对 Cornus 自身的埋点。要采集**你自己工作负载**的遥测数据，Cornus 可以在每个 Pod 的 caretaker (在主机类后端上是一个伴随容器) 内运行一个内嵌的 **OpenTelemetry Collector**，并自动把应用接到它上面: 应用把 OTLP 发送到 `127.0.0.1`，Collector 负责批处理并导出到你的后端，同时 Cornus 会注入 `OTEL_*` 环境变量，因此 OpenTelemetry SDK 无需任何配置。它在所有后端上都可用 (Kubernetes、dockerhost、containerd、bare)。

在 Compose 中按服务启用:

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

把该块放在**项目级别**，即可用一个 endpoint 为所有服务启用 (服务级别的块会覆盖它):

```yaml
name: myproj
x-cornus-telemetry:
  endpoint: otel.example.com:4317
services:
  web: { image: web:latest }
  api: { image: api:latest }
```

也可以在命令行上，通过 `cornus deploy` 和 `cornus compose up` 指定:

```sh
cornus compose up --telemetry-endpoint otel.example.com:4317
cornus deploy -f app.yaml --telemetry-endpoint https://otel.example.com \
  --telemetry-protocol http/protobuf --telemetry-header "authorization=Bearer $TOKEN"
```

应用容器会被自动注入 `OTEL_EXPORTER_OTLP_ENDPOINT` (指向 loopback 接收器) 和 `OTEL_EXPORTER_OTLP_PROTOCOL`；除非你自己设置，否则还会注入 `OTEL_SERVICE_NAME` (部署名称) 和 `OTEL_RESOURCE_ATTRIBUTES`。你自己设置的任何 `OTEL_*` 都会保持不变。

::: tip 需要 sidecar 镜像中包含 Collector
内嵌的 Collector 已编译进每个发布二进制文件和公开镜像。你自行构建的 Cornus 需要 `otelcol` 构建标签 (`go build -tags otelcol`)，否则 caretaker 会报告 Collector 未编译进来，工作负载的启动探针会失败。可用 `cornus version --features` 确认。这与上文控制 Cornus 自身遥测的 `CORNUS_OTEL` 是两回事。
:::

## 内置存储

上文的内容都是把遥测数据发送到别处，这只有在你已经运行 Grafana、Datadog 或 Honeycomb 时才有帮助。Cornus 也可以自己成为那个"别处": 以 `--obs` 启动服务器，它就会在本地的可观测性数据库中保存工作负载的日志、追踪和指标。

```sh
cornus serve --obs
```

这会启用两项能力。值得分开说明，因为其中一项完全不需要你做任何配置。

### 无需任何设置的日志

启用存储后，Cornus 会记录每个受管工作负载的 stdout 和 stderr。你的应用不需要 OpenTelemetry SDK、不需要 sidecar、也不需要配置 —— Cornus 读取的正是 `compose logs` 已经在显示的容器输出，只是把它保存下来。

区别在于，**容器消失之后它依然保留**:

```sh
cornus compose up -d
cornus compose down

# 容器已经不存在了，但它的输出还在。
cornus compose logs web --from=store --since 1h
```

你还可以搜索，这是实时日志流从根本上做不到的 —— 它交给你的是字节，而不是记录:

```sh
cornus compose logs web --match "connection refused"
cornus compose logs web --severity error
```

`--match` 和 `--severity` 隐含 `--from=store`。默认的 `--from=auto` 读取实时运行时，只有当运行时没有任何输出且失败时才回退到存储 —— 因此它返回的行数不会比以前更少。

每个副本都会被记录，并且每条记录都带有其实例序号:

```sh
cornus observe logs --service web --replica 1   # 只看该实例
cornus compose logs web --all-replicas          # 实时、全部实例、带标记
```

`compose logs` 默认仍然只显示单个实例，因此你熟悉的输出没有任何变化; `--all-replicas` 才会选择扇出。

### 同样无需任何设置的资源用量

CPU、内存、网络和磁盘也是如此。Cornus 会按固定间隔采样每个受管工作负载的资源用量并记录下来，于是 `docker stats` 不再是唯一的答案，你可以询问一个工作负载在一小时前在做什么:

```sh
# 最近六小时内，按副本划分的内存
cornus observe metrics 'container_memory_usage' --since 6h

# 你真正想看的是作为速率的 CPU
cornus observe metrics 'rate(container_cpu_time[5m])' --since 6h

# 只看某一个副本
cornus observe metrics 'container_memory_usage{cornus_replica="1"}'
```

每个副本都被单独采样，并以 `cornus_replica` 标签携带其序号，因此你可以比较各个实例，而不是只看到一个掩盖了不均衡的数字。

指标名称遵循 OpenTelemetry 的容器语义约定，因此针对任何 OpenTelemetry 埋点系统编写的 Grafana 仪表板都可以直接使用。完整列表见 [`cornus observe metrics`](/zh/cli/observe#cornus-observe-metrics)。

服务器也以同样的方式记录 **自身的** 用量，以 `process_*` 命名，与它所运行的工作负载并列:

```sh
cornus observe metrics 'process_memory_usage'
cornus observe metrics 'go_goroutine_count'
```

如果你更想看图而不是查询，同一批采样也绘制在 [`cornus web`](/zh/guides/web-ui#指标仪表板) 的 **Metrics** 页面上 —— 每个副本的 CPU、内存、网络、磁盘和进程数，加上服务器自身的用量，其中累计计数器已经微分为速率。

采样默认每 15 秒执行一次。你可以调高频率，也可以整个关掉:

```sh
cornus serve --obs --obs-metrics-interval 5s
cornus serve --obs --no-obs-record-metrics
```

::: warning Kubernetes 上报的内容更少
在 Kubernetes 后端上，这些数字来自 `metrics.k8s.io`，因此 **必须安装 metrics-server**，而且只有 CPU 和内存可用 —— 没有网络计数器、磁盘计数器或进程数，而且 CPU 是以瞬时速率 (`container_cpu_usage`) 而不是主机后端所记录的累积 `container_cpu_time` 的形式送达的。更完整的一组数据位于 kubelet 的 Summary API 之后，它需要一个能触及集群中每个 kubelet 的 `nodes/proxy` 授权; 为了多三个指标族而付出这样的代价并不划算。

内存*上限*会正常上报，因为它并不是一次测量: Cornus 是从自己写入的 Pod spec 中把它读回来的。

缺失的指标族只是不存在，而不会被报告为零，因为"这个容器没有传输任何字节"和"Cornus 无法看到它是否传输过"是两种不同的断言。`cornus observe status` 会在 `metrics.unsupported` 下列出它们，而 Web 仪表板会完全不显示这些面板。运行 `cornus daemon preflight` 可以检查 RBAC 授权。
:::

### 追踪和指标，只需一行

stdout 无法承载追踪。要获得追踪和指标，请把应用的遥测指向 Cornus —— 只需写一个**空块**:

```yaml
services:
  web:
    image: web:latest
    x-cornus-telemetry: {}   # 没有 endpoint: 导出到 Cornus 自身
```

Cornus 会用自己的 OTLP 接收端点填充 endpoint，于是内嵌的 Collector 会把应用的追踪和指标送进与日志相同的存储。两者共享 `service.name`，因此同一请求的日志行与 span 可以关联起来。

显式设置 `endpoint:` 仍然照旧生效，并且优先。

::: tip 服务器需要一个工作负载可达的地址
填充 endpoint 默认值需要 `CORNUS_ADVERTISE_URL` —— 工作负载访问服务器所用的 URL。没有它，Cornus 会记录一条警告并让 endpoint 保持为空，而不会写入一份将在 sidecar 内部悄无声息地失败的导出配置。

实际使用中，这项要求很少造成问题，因为遥测默认[通过 Cornus 连接传输](#它通过-cornus-连接传输) —— URL 仍用于标识目的地并供 caretaker 拨号，但无需从*工作负载自身*的网络访问它。
:::

### 转发到真正的后端

Cornus 不一定要是最终目的地。把它指向你所在组织的 OTLP 后端, 它就会在保存的同时把收到的一切转发出去:

```sh
cornus serve --obs \\
  --obs-export-endpoint https://otlp.example.com \\
  --obs-export-header "authorization=Bearer $TOKEN"
```

这样工作负载只需要**向 Cornus 导出一次**。它们既不需要上游的凭据, 也不需要通往上游的路由 —— 两者都在服务器上, 只在一处配置, 而不是散落在每份部署规范里。本地保留一份短保留期的副本用于即时排查, 长期记录交给上游。

无论是否启用存储, 这都能工作。设置 `--obs-export-endpoint` 和 `--no-obs` 时, Cornus 就是一个纯粹的遥测**网关** —— 由于什么都不保存, 没有 `imbh` 标签的构建也能胜任。

`cornus observe status` 会报告转发器的状态, 并区分两种需要不同应对的失败:

- **dropped** —— 转发跟不上, Cornus 丢弃了记录。上游很慢; 队列是有意设为有界的, 这样卡住的后端永远不会拖慢摄取。
- **failed** —— 上游拒绝了它们, 或者无法访问。那一侧出问题了。

### 它通过 Cornus 连接传输

当 Cornus 是目的地时, 遥测数据**不会**通过网络发往服务器的 OTLP 端点, 而是走 Pod 的 caretaker 已经持有的那条连接 —— 这条路径不需要可达的 URL、不需要从 Pod 出发的路由, 也不需要自己的凭据, 更不会被直连所暗中依赖的 NetworkPolicy 破坏。

在**所有后端上这都是默认行为**, 无需任何配置。上面那个空的 `x-cornus-telemetry: {}` 就已经走这条路径了。

与再导出结合, 就得到了受限集群中最有用的形态: 一个**完全没有 egress** 的工作负载通过其已有的 Cornus 连接导出, 再由 Cornus 代为转发到 SaaS 后端。

如果要强制使用直接的 HTTP 连接 —— 例如想把导出当作普通流量来观察 —— 可以这样写:

```yaml
x-cornus-telemetry:
  via_mux: false
```

或者使用 `--no-telemetry-via-mux`。显式的选择始终优先于默认值。

它在工作负载的网络没有回到服务器的路由时最有价值, 而这并非 Kubernetes 独有 —— [远程 docker 主机](/zh/guides/remote-docker-hosts)和隔离的容器网络同样会遇到。

::: info 默认不生效的情况
只有下面两个条件同时成立时它才会自动启用, 否则它就不只是多余, 而是错误的:

- **目的地是 Cornus。** 如果显式指定了第三方 `endpoint:`, 就不存在通往那个后端的 caretaker 连接可供借道, 因此 Collector 会直接连接它。
- **你没有做出选择。** `via_mux: false` 会被尊重。

它需要 `CORNUS_ADVERTISE_URL` —— caretaker 所拨打的 URL。若未设置, 部署会带着这条消息失败, 而不是悄悄回退到你没有要求的直连。
:::

### 读取数据

```sh
cornus observe logs --service web --match timeout --since 2h
cornus observe traces --service web --min-duration 500ms
cornus observe trace <trace-id>          # span 的瀑布图
cornus observe metrics 'rate(http_requests_total[5m])'
cornus observe query 'SELECT service, count(*) FROM logs GROUP BY service'
cornus observe status
```

在根据空结果断定"什么都没发生"之前，先运行 `cornus observe status` —— 它会报告负载压力下**丢弃**了多少条记录，这正是"你的服务很安静"与"证据被丢掉了"之间的区别。

完整的命令参考见 [cornus observe](/zh/cli/observe)。

### 把 Grafana 指过来

由于查询语言已经实现，Cornus 直接应答 Grafana 的数据源 API。添加三个数据源即可，中间不需要代理或 exporter:

| 数据源 | URL |
|---|---|
| Prometheus | `http://<server>:5000/.cornus/v1/obs/prom` |
| Loki | `http://<server>:5000/.cornus/v1/obs/loki` |
| Tempo | `http://<server>:5000/.cornus/v1/obs/tempo` |

每个 API 都提供了足以支持范围查询和追踪视图的部分。使用 Cornus 不支持的构造的查询会带着诊断信息被拒绝，而不是被近似处理，因此面板要么显示正确的数据，要么告诉你为什么显示不了。

### 保留策略

| 标志 | 环境变量 | 默认 | 说明 |
|---|---|---|---|
| `--obs` | `CORNUS_OBS` | `false` | 启用存储。 |
| `--obs-dir` | `CORNUS_OBS_DIR` | `<data-dir>/observability` | 数据库所在位置。 |
| `--obs-retention` | `CORNUS_OBS_RETENTION` | `168h` (7 天) | 丢弃早于此时长的记录。向上取整到整天。 |
| `--obs-max-bytes` | `CORNUS_OBS_MAX_BYTES` | `536870912` (512 MiB) | 磁盘占用上限。 |
| `--obs-record-logs` | `CORNUS_OBS_RECORD_LOGS` | `true` | 记录工作负载的 stdout/stderr。用 `--no-obs-record-logs` 关闭。 |
| `--obs-record-metrics` | `CORNUS_OBS_RECORD_METRICS` | `true` | 采样工作负载和服务器的资源用量。用 `--no-obs-record-metrics` 关闭。 |
| `--obs-metrics-interval` | `CORNUS_OBS_METRICS_INTERVAL` | `15s` | 每个副本的采样间隔。 |

::: tip 开箱即用
该存储已包含在每个发布二进制文件和公开镜像中，且在编译进来的环境下 `--obs` 默认为 **on**，因此下载得到的 `cornus serve` 无需任何参数即开始记录。可以这样确认:

```sh
cornus version --features   # obsstore: yes
```

用 `--no-obs` (或 `CORNUS_OBS=0`) 关闭它。

该存储是通过 cgo 使用的内嵌 Rust 数据库，因此你自行构建的 Cornus 除非显式指定，否则编译的是桩实现:

```sh
eval "$(go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@v0.3.0 -print-env)"
CGO_ENABLED=1 go build -tags "netgo osusergo otelcol imbh sable_extern_lib" ./cmd/cornus
```

这样的构建会报告 `obsstore: no`，`--obs` 默认保持关闭；如果你显式传入 `--obs`，它会记录一条日志说明存储未编译进来，而不是悄无声息地不做记录。
:::

## Health 和 readiness probe

即使启用 auth，liveness 和 readiness endpoint 仍保持开放，因此 probe 和 load balancer 无需 token 也能访问。

```sh
# From a script or another host:
curl -fsS http://localhost:5000/healthz
curl -fsS http://localhost:5000/readyz

# In-image healthcheck with no extra tools (Dockerfile):
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```

- `cornus health` 向 `/healthz` 发起 GET (5 秒 timeout) ，除非服务器返回 `200 OK` 否则以非零退出；这是不需要在镜像内安装 `curl` 的 container healthcheck。
- 随附 Kubernetes manifest 直接连接 `/healthz` (liveness) 和 `/readyz` (readiness) 。

**另请参阅:** [服务器环境变量](/zh/reference/server-env-vars) · [cornus serve](/zh/cli/serve) · [cornus health](/zh/cli/version-health) · [安装](/zh/introduction/installation) · [架构](/zh/architecture/)
