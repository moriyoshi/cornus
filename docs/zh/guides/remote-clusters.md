# 使用远程集群

Cornus 指向远程服务器或集群内部署时，与在本地使用同样实用。构建引擎、镜像仓库和部署引擎均在服务器上运行，而你的源代码、密钥和绑定挂载仍留在本机，按需流式传输。本页涵盖让远程 Cornus 用起来如同本地的各项能力：端点标志、连接配置文件、自动转发端口到集群，以及利用 Kubernetes 访问凭据签发凭据。

远程主题的另外两部分在相邻页面：将构建上下文流式传输到远程构建器见[构建镜像](/zh/guides/building-images)，将客户端本地目录绑定挂载到远程工作负载见[部署工作负载](/zh/guides/deploying-workloads)。

若要交互式地构建集群配置文件 (自动检测集群内 Service、选择认证方式并生成 helm values 片段)，请运行 [`cornus setup`](/zh/cli/setup) 向导。

## 工作原理

### 将 CLI 指向服务器

每个与 Cornus 服务器通信的客户端命令都接受端点，并从环境中读取 bearer token：

| 设置 | 环境变量 | 使用方 |
| --- | --- | --- |
| `--server` | `CORNUS_SERVER` | `deploy`、`exec`、`port-forward`、`socks5`、`tunnel`、`compose`、`hub` 等 |
| `--builder` | `CORNUS_BUILDER` | `build` (远程构建 attach 端点) |
| `CORNUS_TOKEN` | `CORNUS_TOKEN` | 对 `/.cornus/v1/*`、归档 `PUT` 和 WebSocket attach 的 bearer 身份验证 |

命令中显式指定的端点优先；否则会从所选连接配置文件中解析 (见下文)。端点支持 `http(s)://` 或 `ws(s)://` 形式。

每次调用都重复输入端点和 token 很快会令人厌烦；连接配置文件可将它们完全移出命令行。

### 连接配置文件

`cornus config` 管理一个 kubeconfig 风格的文件 (默认位于平台用户配置目录，可用 `--config-file` / `CORNUS_CONFIG` 覆盖)，它一次存储命名连接，之后每个命令都可不在命令行中提供任何连接信息。

配置文件适用于 `deploy`、`exec`、`port-forward`、`socks5`、`tunnel`、`compose`、`daemon docker`、`build` 和 `hub`。端点优先级为显式标志、然后是所选 context 的 server；token 优先级为 `CORNUS_TOKEN`、然后是配置文件 token。可通过 `--context <name>` (环境变量 `CORNUS_CONTEXT`) 为单个命令选择配置文件。整个 context 可从 JSON/YAML 文件加载 (`--from-file` 作为基础层，`--from-file-override` 让文件优先)，并可导出以便往返使用 (`config view --export`)。完整字段集记录在[连接配置](/zh/reference/connection-config)中；命令本身是 [`cornus config`](/zh/cli/config)。

**另请参阅：**[连接配置](/zh/reference/connection-config)、[cornus config](/zh/cli/config)

## 将一次性 command 指向 remote server

无需创建 profile，就为单条 command 指定 server。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_SERVER=https://cornus.example.com CORNUS_TOKEN="$TOKEN" cornus exec -it web -- sh
```

- `--server` 优先于 `CORNUS_SERVER`，后者优先于 selected profile。Endpoint 接受 `http(s)://` 或 `ws(s)://`。
- Bearer token 从 `CORNUS_TOKEN` (或 profile) 读取；它从不是 command flag。

**另请参阅：**[cornus deploy](/zh/cli/deploy)

## 为 remote server 创建 connection profile

一次保存 server URL、token 和 TLS material，使 command 无需 command-line 参数。

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod
cornus deploy -f app.yaml
```

- `set-context` 默认替换命名 context；传入 `--merge` 可原地编辑并保留未设置 field。
- 分层顺序为 `--from-file` (base)、flag、`--from-file-override` (top)。

**另请参阅：**[cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)

## 通过 profile 自动 port-forward 至 in-cluster server

对于没有 Ingress 的集群内 Cornus，配置文件可指定 **Service** 而非 URL，CLI 会在每个命令的存续期间自行打开端口转发，即内嵌的 `kubectl port-forward` 等价物。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster
cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

- 保持 `--server` 未设置：带有 `port-forward` block 的空 `server` 会 dial in-cluster Service。无需后台运行 `kubectl port-forward svc/cornus 5000:5000 &`，转发会围绕每个命令建立和拆除。
- `--pf-kube-context` 选择 kubeconfig context；`--pf-service` 跳过 Service auto-detection。

访问已部署**工作负载**的端口是另一个自动处理的问题：会话通过任何 `ports:` 发布的端口都会隧道转发至 `127.0.0.1:<host>`，而 [`cornus port-forward`](/zh/cli/port-forward) 还可按需访问未发布的容器端口。对于集群配置文件，两者都会使用你的 kubeconfig 通过 SPDY 直接连接到工作负载 Pod，失败时回退到通过 Cornus 服务器的隧道。

**另请参阅：**[cornus config](/zh/cli/config)、[网络与 conduit](/zh/guides/networking)

## 从自己的 kube access 签发短期 credential

当 Cornus 在集群中运行且信任集群自身的 OIDC 签发者时，配置文件可从你的 Kubernetes 访问权限**签发 bearer token**，而非存储静态 token。CLI 会通过 Kubernetes TokenRequest API 请求一个短期且限定 audience 的 ServiceAccount token，并将其作为凭据发送；Cornus 会使用集群 JWKS 验证它，因此无需单独配置 Cornus token。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # mints a cluster token AND port-forwards -- no static token
```

- `--kube-auth-audience` 必须匹配 server 的 `CORNUS_JWT_AUDIENCE`。
- `--kube-auth-namespace` / `--kube-auth-kube-context` 默认使用 `--pf-*` 值；`--kube-auth-expiration-seconds` 默认 `3600`。

在服务器端，将集群内 Cornus 指向集群的 JWKS，并要求相同 audience：这是标准 JWKS 验证路径，不需要修改服务器代码。

**另请参阅：**[连接配置](/zh/reference/connection-config)、[安全与认证](/zh/guides/security)

## 切换、查看和删除 profile

以 kubeconfig 风格管理 connection profile 集合。

```sh
cornus config get-contexts          # list profiles (current marked *)
cornus config use-context staging   # make staging the default
cornus config current-context       # print the current context name
cornus config view                  # print the file (tokens redacted)
cornus config delete-context old    # remove a profile
```

- `view --show-tokens` 打印 bearer token；`view --export --context prod` 输出一个可 round-trip 至 `set-context --from-file` 的 bare Context object。
- `delete-context` 若删除的 context 是 current，则清除 current-context pointer。

**另请参阅：**[cornus config](/zh/cli/config)

## 为 profile 设置默认 namespace

记录 cornus install 的 namespace，使 cluster detection 和 kube-auth 默认使用它。

```sh
cornus config set-context staging -n cornus-system
```

- `-n`/`--namespace` 会自动检测 Service 与 port，除非设置 `--pf-service` 或 `--no-detect`；添加 `--no-detect` 可不联系 cluster 而保存 namespace。

**另请参阅：**[cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)

## 让 client-to-workload 流量经 server 路由

对于集群配置文件，日志和端口转发通常会使用你的 kubeconfig 直接连接到 Pod。如需强制改走服务器路由路径，请设置 `via-server`。

```sh
cornus config set-context cluster --merge --via-server
cornus port-forward --via-server web 8080:80    # per-command override
```

- 优先级为每 command 的 `--via-server` / `--no-via-server` flag (`--no-via-server` 强制直连)、`CORNUS_VIA_SERVER` (`1`/`0`)、profile field。
- 该设置只改变 transport；`kube-auth` profile 仍会签发 cluster token。

**另请参阅：**[cornus port-forward](/zh/cli/port-forward)

## 对 remote deployment tail log 和 exec

通过已解析的 server 或 profile stream workload log，并在其中运行 command。

```sh
cornus compose logs --follow --tail 100 web
cornus exec -it web -- sh
```

- Cluster profile 中，log 与 exec 使用 kubeconfig 直达 pod，必要时 fallback 到 server proxy；`--via-server` 强制 server-routed path。
- `exec` 中 `--` 后的所有内容原样传给 command；stdin 不是 terminal 时，`-t` 降级为 plain stream。

**另请参阅：**[cornus exec](/zh/cli/exec)、[cornus compose](/zh/cli/compose)
