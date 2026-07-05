# 远程工作流

Cornus 指向远程服务器或集群内部署时，与在本地使用同样实用。构建引擎、镜像仓库和部署引擎均在服务器上运行，而你的源代码、密钥和绑定挂载仍留在本机，按需流式传输。本页串联了让远程 Cornus 用起来如同本地的各项能力：端点标志、远程构建、带客户端本地挂载的远程部署、连接配置文件、会话通道，以及利用 Kubernetes 访问凭据签发凭据。

## 将 CLI 指向服务器

每个与 Cornus 服务器通信的客户端命令都接受端点，并从环境中读取 bearer token：

| 设置 | 环境变量 | 使用方 |
| --- | --- | --- |
| `--server` | `CORNUS_SERVER` | `deploy`、`exec`、`port-forward`、`socks5`、`tunnel`、`compose`、`hub` 等 |
| `--builder` | `CORNUS_BUILDER` | `build` (远程构建 attach 端点) |
| `CORNUS_TOKEN` | `CORNUS_TOKEN` | 对 `/.cornus/v1/*`、归档 `PUT` 和 WebSocket attach 的 bearer 身份验证 |

命令中显式指定的端点优先；否则会从所选连接配置文件中解析 (见下文)。端点支持 `http(s)://` 或 `ws(s)://` 形式。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_TOKEN=<token> cornus exec -i -t web sh --server https://cornus.example.com
```

每次调用都重复输入端点和 token 很快会令人厌烦；连接配置文件可将它们完全移出命令行。

## 远程构建

`cornus build --builder` 会在 Cornus 服务器上运行构建，同时通过 **9P-on-WebSocket** 流式传输调用方的上下文、具名绑定目录、密钥和 SSH agent。构建保持 BuildKit 原生，缓存留在服务器上；主机无需 Docker，也不需要构建权限。

```sh
cornus build --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 \
  --build-context data=./data \
  --secret id=token,src=./token.txt \
  --ssh default ./context
```

在 Dockerfile 中，流式输入会作为普通 buildx 挂载出现：

```dockerfile
RUN --mount=type=bind,from=data ...
RUN --mount=type=secret,id=token ...
RUN --mount=type=ssh ...
```

调用方的 ssh-agent 会为 `type=ssh` 挂载转发，因此可获取私有依赖，同时密钥绝不会离开你的机器。

### 延迟上下文

默认情况下，具名上下文会被急切同步。使用 `--lazy` (或 `CORNUS_LAZY_BUILD`) 时，上下文改为按需提供，因此只有构建实际读取的字节才会穿过网络：一个大小为 20 MB、构建仅读取 11 字节的上下文，只传输 11 字节。`CORNUS_BUILD_WORKER=containerd` 不支持延迟上下文。

```sh
cornus build --lazy --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 --build-context data=./big-data ./context
```

带有 `server` 的配置文件会自行将构建路由至远程服务器 (显式的 `--builder` 仍优先)。以 `type=local` 指定的构建缓存使用名称而非文件系统路径，因此同一组 `--cache-to` / `--cache-from` 在本地和远程构建中表现完全一致。完整标志集请参阅 [`cornus build`](/zh/cli/build)。

## 带客户端本地绑定挂载的远程部署

`cornus deploy --server` 会在远程服务器上运行部署，同时通过 `--local-mount` (或 Compose `volumes:`) 使用经由 9P 流式传输的、位于*本机*的目录进行绑定挂载。部署会在命令保持连接期间持续存在，这正是它适用于内循环的原因：你在本地编辑文件，工作负载便能看到更新。

```sh
cornus deploy --server http://cornus.example:5000 \
  --local-mount ./config:/etc/app:ro --local-mount ./data:/data -f deploy.yaml
```

规范 `ports:` 中发布的端口会在会话期间自动转发到你机器上的 `127.0.0.1:<host>`，即使后端是 Kubernetes 集群也是如此，因此工作负载可在本地响应。可使用 `--no-forward-ports` 退出此行为。客户端本地挂载由服务器自身的 `<DataDir>/mounts` 区域提供，且始终允许使用，因此无需放宽主机权限策略。参阅 [`cornus deploy`](/zh/cli/deploy)。

素朴地 tunnel 9P 意味着每次读取都跨越网络，这对大型或写密集的挂载会造成困扰。两个后缀可启用服务器端文件缓存：`,cache`（隐含 `:ro`）是面向数据集、模型权重等 **immutable** 输入的 read-through 缓存，`,async` 是面向开发数据库等 **single-writer** 工作负载的可写、cache-coherent 挂载。两者都需要启用服务器的文件缓存；`,async` 挂载可通过在两端设置 `CORNUS_BLOCK_COHERENCE` / `CORNUS_BLOCK_READAHEAD` 针对数据库形态的随机 I/O 进行调优。缓存机制参阅[客户端本地绑定挂载](/zh/architecture/deploy-engine#客户端本地绑定挂载)，各项旋钮参阅[服务器环境变量](/zh/reference/server-env-vars#远程-9p-文件缓存和可写挂载)。

```sh
cornus deploy --server http://cornus.example:5000 \
  --local-mount ./models:/models:ro,cache \
  --local-mount ./db:/var/lib/app:async -f deploy.yaml
```

## 连接配置文件

`cornus config` 管理一个 kubeconfig 风格的文件 (默认位于平台用户配置目录，可用 `--config-file` / `CORNUS_CONFIG` 覆盖)，它一次存储命名连接，之后每个命令都可不在命令行中提供任何连接信息。

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod          # make it the default context

cornus config get-contexts              # list profiles (current is marked *)
cornus config view                      # print the file (bearer tokens redacted)

# Commands now need no --server / CORNUS_TOKEN:
cornus deploy -f app.yaml
cornus compose up
```

配置文件适用于 `deploy`、`exec`、`port-forward`、`socks5`、`tunnel`、`compose`、`daemon docker`、`build` 和 `hub`。端点优先级为显式标志、然后是所选 context 的 server；token 优先级为 `CORNUS_TOKEN`、然后是配置文件 token。可通过 `--context <name>` (环境变量 `CORNUS_CONTEXT`) 为单个命令选择配置文件。整个 context 可从 JSON/YAML 文件加载 (`--from-file` 作为基础层，`--from-file-override` 让文件优先)，并可导出以便往返使用 (`config view --export`)。完整字段集记录在[连接配置](/zh/reference/connection-config)中；命令本身是 [`cornus config`](/zh/cli/config)。

### 自动转发端口到集群

对于没有 Ingress 的集群内 Cornus，配置文件可指定 **Service** 而非 URL，CLI 会在每个命令的存续期间自行打开端口转发，即内嵌的 `kubectl port-forward` 等价物：

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster

cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

无需后台运行 `kubectl port-forward svc/cornus 5000:5000 &`，也无需 `--server`：转发会围绕每个命令建立和拆除。`--pf-kube-context` 选择 kubeconfig context。访问已部署**工作负载**的端口是另一个自动处理的问题：会话通过任何 `ports:` 发布的端口都会隧道转发至 `127.0.0.1:<host>`，而 [`cornus port-forward`](/zh/cli/port-forward) 还可按需访问未发布的容器端口。对于集群配置文件，两者都会使用你的 kubeconfig 通过 SPDY 直接连接到工作负载 Pod，失败时回退到通过 Cornus 服务器的隧道。

## 会话通道：port-forward 与 SOCKS5

会话向调用方暴露工作负载的方式称为其**通道模式**。默认是逐端口转发 (每个已发布端口一个本地监听器，兼容 Compose)。可选替代方案是单个客户端侧的 **SOCKS5 分流隧道代理**：服务主机后缀 (默认为 `.cornus.internal`) 下的主机名会按名称隧道至对应工作负载，其他所有目标则直接从你的机器拨号。一个代理即可按名称访问每个服务，不需要逐端口监听器。

```sh
# Make SOCKS5 the conduit for a profile, so compose up / deploy --server use it:
cornus config set-context demo --conduit-mode socks5
# Pin the shared proxy's bind address and suffix in one value:
cornus config set-context demo --conduit-mode 'socks5://.shared:1085?suffix=.demo.internal'

# Per-run override (flag > CORNUS_CONDUIT > profile > default port-forward):
cornus compose up --conduit socks5                    # join the shared proxy
cornus compose up --conduit 'socks5://'               # own proxy, ephemeral port
cornus deploy --server http://cornus.example:5000 --conduit socks5 -f deploy.yaml
```

裸单词 (或 `socks5://.shared`) 会加入配置文件的共享代理；带 authority 的 `socks5://` URL 会启动专用的会话本地代理，并可与之共存。在 SOCKS5 模式下，按服务器共享的代理还会覆盖 `cornus daemon docker` 容器，因此一个代理即可按名称访问 Docker 容器和 Compose 服务。SOCKS5 CONNECT 仅支持 TCP。独立的临时代理是 [`cornus socks5`](/zh/cli/socks5)。

### `--via-server`

对于集群配置文件，日志和端口转发通常会使用你的 kubeconfig 直接连接到 Pod。如需强制改走服务器路由路径，请设置 `via-server`；其优先级依次为：每个命令的 `--via-server` 标志 (`--no-via-server` 强制直连)、`CORNUS_VIA_SERVER` (`1`/`0`) 和配置文件字段。它只改变传输方式；`kube-auth` 配置文件仍会签发其集群 token。

## 从 Kubernetes 访问凭据签发短期凭据

当 Cornus 在集群中运行且信任集群自身的 OIDC 签发者时，配置文件可从你的 Kubernetes 访问权限**签发 bearer token**，而非存储静态 token。CLI 会通过 Kubernetes TokenRequest API 请求一个短期且限定 audience 的 ServiceAccount token，并将其作为凭据发送；Cornus 会使用集群 JWKS 验证它，因此无需单独配置 Cornus token。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # mints a cluster token AND port-forwards -- no static token
```

在服务器端，将集群内 Cornus 指向集群的 JWKS，并要求相同 audience：这是标准 JWKS 验证路径，不需要修改服务器代码。有关验证器配置，请参阅[身份验证和 TLS](/zh/topics/auth-and-tls)；有关 `kube-auth` 字段，请参阅[连接配置](/zh/reference/connection-config)。
