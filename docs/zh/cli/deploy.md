# cornus deploy

在本地或远程 cornus 服务器上应用(或删除)部署 spec。

## 概要

```sh
cornus deploy -f <spec> [flags]
```

## 说明

`cornus deploy` 读取并应用部署 spec(YAML 或 JSON)。未指定 `--server` 时，部署到此主机的 local backend；指定后，部署到远程 cornus 服务器。文件格式参见 [Deploy spec](/zh/reference/deploy-spec)。

local backend 由 `CORNUS_DEPLOY_BACKEND` 选择: `dockerhost`(默认)、`containerd` 或 `bare`。其他任何值——包括仅服务器支持的 `kubernetes`——均会警告后回退到 `dockerhost`。参见[部署后端](/zh/reference/deploy-backends)。
### Knative Serving 描述符

`-f` 还接受 `serving.knative.dev/v1`、Kind `Service` 的 Knative Serving Service manifest (ksvc)。它与 native spec、docker-compose 和 devcontainer 一样是一等描述符。`cornus deploy` 通过 `apiVersion`/`kind` 检测它，并转换为部署 (image、env、port、command/args、resource、exec probe，以及 `minScale`、`maxScale`、`target`、`class`、`metric`、`containerConcurrency`、`timeoutSeconds` 等 autoscaling 参数)。

安装 Knative Serving 的 Kubernetes 集群会将部署 round-trip 为 native `serving.knative.dev/v1` Service，因此 autoscaler 管理 replica 与 scale-to-zero，Route 提供 URL (显示在 deploy status 中)。其他目标，包括普通集群以及 `dockerhost` / `containerd` / `bare`，会将工作负载作为普通容器运行并警告未实现 autoscaling。设置 `CORNUS_KNATIVE_STRICT=true` 可在无法实现时失败。`cornus restart` 会创建新的 revision；scale-to-zero 服务不适用 `stop`/`start`。

```bash
cornus deploy -f service.yaml --server wss://cornus.example.com
```

当前仅支持 Serving (不支持 Eventing) 和单一 always-latest revision (不支持 traffic splitting)。将 mount、user network、volume 或 proxy/DNS/hub role 与 ksvc 组合会被拒绝，而不会部分应用。

使用 `--server` 时，默认是前台 deploy-attach session: 客户端本地 bind mount(包括 `--local-mount`)经 9P 流式传输，除非设置 `--no-forward-ports`，否则已发布端口会自动转发至本地 listener；`Ctrl-C`(或 `SIGTERM`)请求正常 teardown。使用 `--detach` 时，spec 只 POST 一次，命令退出但工作负载保持运行；之后使用 `cornus deploy -f <spec> --delete --server <url>` 清理。Detached deploy 拒绝客户端本地 mount 和客户端提供的 credential，已发布端口绑定服务器主机而不是自动转发。参见[使用远程集群](/zh/guides/remote-clusters)。

`--conduit` 选择 `--server` session 到达工作负载的方式: 每端口本地 listener(默认 `port-forward`)或按服务名访问的单一 SOCKS5 split-tunnel proxy(`socks5`)。它优先于 `CORNUS_CONDUIT` 和 profile mode；`--no-forward-ports` 完全禁用 conduit。

使用 `--conduit socks5` 时，`--ingress-conduit` 还可经 proxy 访问部署声明的 ingress host(`ingress:` / `x-cornus-ingress`): `native` 隧道到真实 cluster ingress controller，`emulate` 运行带生成证书的客户端侧 reverse proxy。优先级依次为 flag、`CORNUS_INGRESS_CONDUIT` 和 profile；`off` 会禁用它。参见[Ingress](/zh/guides/ingress)。

`--egress-*` flag 使容器 egress 经客户端侧网络路由。参见[Egress](/zh/guides/egress)。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-f`, `--file` | — | 必需 | 部署 spec 文件(YAML 或 JSON)。 |
| `--delete` | — | `false` | 删除指定部署而非应用它(本地和 `--server` 均可用)。 |
| `-d`, `--detach` | — | `false` | 无状态远程部署: POST spec 给 `--server`，打印状态后退出；工作负载无客户端 session 地持续运行。拒绝客户端本地 bind mount，且不自动转发已发布端口。本地部署时无作用。 |
| `--server` | — | — | 远程 cornus 服务器 URL(`http(s)://` 或 `ws(s)://`)。设置时对远程服务器部署。 |
| `--local-mount` | — | — | 通过 9P 提供给 `--server` 的客户端本地 bind mount `SRC:DST[:ro][,cache][,async]`。`cache` 为不变且只读；`async` 可写、保持缓存一致性，并且仅限单一 writer。可重复。 |
| `--no-forward-ports` | — | `false` | `--server` session 中不自动将已发布端口转发到本地 listener(也禁用 conduit)。 |
| `--conduit` | `CORNUS_CONDUIT` | profile mode | Session conduit mode: `port-forward`(默认)或 `socks5`。裸词仅设置 mode；`socks5://host:port[?suffix=SUFFIX]` URL 还会覆盖 bind address 与 service-host suffix(`socks5h://` 是同义词)。优先于 `CORNUS_CONDUIT` 和 profile mode。 |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | profile | 通过 SOCKS5 conduit 访问部署 ingress: `native`(隧道到真实 cluster ingress controller)、`emulate`(带生成证书的客户端侧 reverse proxy)或 `off`。需要 `--conduit socks5`。参见[Ingress](/zh/guides/ingress)。 |
| `--via-server`, `--no-via-server` | `CORNUS_VIA_SERVER` | profile | 经 cornus 服务器代理自动转发端口，而不是通过 kubeconfig 直接连接 pod(仅 cluster profile)。`--no-via-server` 强制直接路径。 |
| `--egress` | — | — | 让容器 egress 经客户端侧网络: `env`(传播 proxy var)、`proxy`(caretaker forward proxy)或 `transparent`(nftables + relay)。 |
| `--egress-route` | — | — | Egress route `PATTERN=ROUTE`(route: `client`、`gateway`、`cluster` 或 `deny`)，首个匹配获胜。可重复。 |
| `--egress-default` | — | `cluster` | 未匹配目标的 egress route: `cluster`(默认)、`client`、`gateway` 或 `deny`。 |
| `--egress-pac` | — | — | 决定 egress route 的 PAC 风格 JS 文件(`FindProxyForURL`)路径；优先于 `--egress-route`。 |
| `--telemetry-endpoint` | — | — | 启用内置 Collector，并将工作负载 telemetry 导出到该 OTLP endpoint。 |
| `--telemetry-protocol` | — | `grpc` | exporter protocol：`grpc` 或 `http/protobuf`。 |
| `--telemetry-header` | — | — | 静态 OTLP export header `KEY=VALUE`。可重复。 |
| `--telemetry-insecure` | — | `false` | 禁用到 OTLP endpoint 的传输安全。 |
| `--telemetry-signal` | — | 全部 | 将 pipeline 限制为 `traces`、`metrics` 或 `logs`。可重复。 |
| `--telemetry-service-name` | — | deployment name | 覆盖注入的 `OTEL_SERVICE_NAME`。 |
| `--telemetry-debug` | — | `false` | 同时将收集的 telemetry 输出到 Collector stdout。 |

`CORNUS_DEPLOY_BACKEND` 选择 local backend(默认 `dockerhost`、`containerd` 或 `bare`)。

## 示例

将 spec 应用于本地 Docker 主机:

```sh
cornus deploy -f app.yaml
```

针对远程服务器部署并保持前台运行:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

Detached deploy，随后清理:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --detach
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

将本地目录流式传输到工作负载，并通过 SOCKS5 访问服务:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --local-mount ./data:/data:ro \
  --conduit socks5
```

使用 routing rule 让 egress 经客户端:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route 'api.internal=client' \
  --egress-default deny
```

删除本地部署:

```sh
cornus deploy -f app.yaml --delete
```

## 另请参阅

- [Deploy spec](/zh/reference/deploy-spec)
- [部署后端](/zh/reference/deploy-backends)
- [使用远程集群](/zh/guides/remote-clusters)
- [Egress](/zh/guides/egress)
- [凭据](/zh/guides/credentials)
- [`cornus exec`](/zh/cli/exec)
- [`cornus port-forward`](/zh/cli/port-forward)
