# 可观测性

Cornus 提供 OpenTelemetry trace、metric 和 log、可选 Prometheus scrape endpoint，以及 liveness/readiness probe。所有 telemetry 都是**可选的，关闭时没有成本**：在您启用前不会安装任何内容，也不会启动 exporter goroutine，因此默认配置下埋点调用点几乎没有成本。

设计细节（哪些内容被埋点以及 span 如何跨 caretaker rendezvous 传播）请参见[架构概览](/zh/architecture/)。下文所有变量均列在[服务器环境变量](/zh/reference/server-env-vars)参考中。

## 启用 OpenTelemetry

完全通过标准 `OTEL_*` 环境变量安装 trace、metric 和 log provider；没有 Cornus 特有的 exporter 配置接口。

```sh
# Turn it on by pointing at a collector — any OTEL_* var enables it:
OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317 cornus serve

# Or force it on with the SDK defaults:
cornus serve --otel                       # equivalent to CORNUS_OTEL=1
```

- 仅当 `CORNUS_OTEL` 为真或设置标准 `OTEL_*` 变量时才安装 telemetry；`OTEL_SDK_DISABLED=true` 优先时则永不安装。禁用时 setup 是 no-op，OpenTelemetry API 保持 no-op 默认值。
- 通过通常的 `OTEL_*` var（`OTEL_EXPORTER_OTLP_*`、`OTEL_TRACES_EXPORTER`、`OTEL_TRACES_SAMPLER` 等）配置 exporter、sampling 和 endpoint。
- 服务 identity 对服务器是 `cornus`，对每 pod sidecar 是 `cornus-caretaker`。caretaker connection span 与服务器侧 attach span 会在 rendezvous 中组成一条端到端 trace。

## 哪些内容被埋点

- **HTTP**——`otelhttp` layer 以每个 request 的 server span 和标准 HTTP metric 包装 server mux。高基数 path（digest、deployment name、upload UUID）会折叠为 route template，避免 series 膨胀；streaming / WebSocket endpoint 仍可正常工作。
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

## Health 和 readiness probe

即使启用 auth，liveness 和 readiness endpoint 仍保持开放，因此 probe 和 load balancer 无需 token 也能访问。

```sh
# From a script or another host:
curl -fsS http://localhost:5000/healthz
curl -fsS http://localhost:5000/readyz

# In-image healthcheck with no extra tools (Dockerfile):
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```

- `cornus health` 向 `/healthz` 发起 GET（5 秒 timeout），除非服务器返回 `200 OK` 否则以非零退出；这是不需要在镜像内安装 `curl` 的 container healthcheck。
- 随附 Kubernetes manifest 直接连接 `/healthz`（liveness）和 `/readyz`（readiness）。

**另请参阅：**[服务器环境变量](/zh/reference/server-env-vars) · [cornus serve](/zh/cli/serve) · [cornus health](/zh/cli/version-health) · [安装](/zh/introduction/installation) · [架构](/zh/architecture/)
