# 兼容 Docker 的客户端和连接配置文件

三种客户端界面汇入同一 server API，并共享一条 translation pipeline：`cornus compose`（docker-compose 的同类实现）、`cornus daemon docker`（用于标准 `docker` CLI 的 Docker Engine API proxy）和原生 Dev Container 支持。它们都是单一 `cornus` binary 的 subcommand。

## Docker API proxy

`cornus daemon docker` 在 unix socket 上提供 Docker Engine REST API 的子集，并将 container operation 转换为针对 remote server 的 Cornus deploy。将 `DOCKER_HOST` 指向该 socket 后，标准 `docker` CLI 会在远程 Cornus 上运行 workload，调用方的本地 bind-mount 目录经 9P 流式传输。

Docker 的 create/start 分割通过**buffering**映射到 Cornus 的 atomic apply：`docker create` 转换 request 并保存 record，但不联系 server；`docker start` 打开长期 deploy-attach session，在 container 存续期间保持连接（使 9P mount 持续可用）。`docker ps`/`inspect` 从 buffered record 合成 Docker 形状的 response，并回显 create-time label，从而支持 label filter。

`stop`/`start` 在 **record level** 往返：`stop` 取消 session（销毁 workload），但保留标记为 `exited` 的 record，因此 `docker ps -a` 仍会列出它；`start` 重新打开 session 并重新部署。这有意不是 container-level pause——客户端提供的 9P mount 无法在调用方 session 之外存活，因此 workload 被 recreate 而非 pause，与 Cornus 基于 recreate 的 deploy model 一致。覆盖 `run`、`ps`、`inspect`、`stop`、`start`、`rm`、`logs`、`exec`、`attach`（含交互 `-it`）、`stats` 和 `cp`；标准 `docker compose up/ps/down` 也可工作。`/build` 不在范围内——构建属于 `cornus build`。

前台 `docker run` 能运行，是因为 proxy 复刻 dockerd 的精确 protocol 而不仅是 route：它在 session 存活前驻留 attach，以立即 header、仅在退出时 body 的方式回答 `wait?condition=next-exit`，并以 CLI 可识别的两种 encoding 发布 lifecycle event。这样的 fidelity 使 VS Code Dev Containers extension 所使用的官方 `@devcontainers/cli` 可不作修改地面向 proxy 工作。

## Compose client 和 Dev Container

`cornus compose` 是 client 而非 local driver：它解析 Compose 文件，将每个 service 转换为一个 `DeploySpec` 加可选 build plan，根据 `depends_on` 计算 dependency order，并驱动运行中的 server。`up` 构建（存在 `build:` section 时）、推送至 registry，再按依赖顺序部署；`down` 则反向执行。

因为 client-served mount 不能超出 client 生命周期，有本地 bind mount 的 service 需要 live session：不带 `-d` 的 `up` 会在 **foreground** 运行这些 service 至 Ctrl-C；`up -d` 将其交给下文的 unified client agent，由 agent 在后台持有 session 并让命令返回。

**Dev Container** 通过将 `.devcontainer/devcontainer.json` 转换为 Compose path 所用的*同一个* compose project model 来原生读取，因此每个 `up`/`down`/`ps`/`build` command 都无需变化。支持两种形式：single-container（`image` / `build.dockerfile`）和 compose-based（`dockerComposeFile` + `service`）。Lifecycle hook（`onCreate`、`postCreate`、`postStart` 等）在 service ready 后通过 server-side exec 在 container 内运行，因此无论谁持有 9P session，所有 up path 都可工作。

## 声明式 reconcile 与命令式 proxy

两种界面按设计位于 declarative/imperative 分界线的两侧。Compose file 是 desired-state description，因此 compose path 运行一个小型**声明式 reconcile engine**：调用方应用一组所需 service，它驱动 live resource——9P mount session 和 port-forward/SOCKS5 exposure——与之匹配；按维度 fingerprint 保证仅 exposure 变更不会 teardown 健康 mount。

Docker API 已是命令式的（`create`/`start`/`stop`/`rm` 是离散 edge event），其 container 也是 immutable，因此 proxy 不做 reconcile；每 container state machine 编码 Docker API contract。二者共用*下层*：每 workload 的 deploy-attach hold 和 conduit exposure primitive。

## Unified client agent

每个 background client-held session 都存在于**每用户一个**长期 process——`cornus daemon agent`——中，通过单一 control socket（`$XDG_RUNTIME_DIR/cornus/agent.sock`）访问。`cornus compose up -d` 和 `cornus daemon docker` 都是薄 client：它们 ping 以复用或启动 agent，并在 socket 上注册工作；`cornus daemon status` / `stop` 则检查和销毁它。

Client 会预先解析 connection identity 并随工作发送，因为 agent 的 process environment 在启动时冻结；agent 使用相同 profile logic 重新解析，因此 background compose session 可获得 profile 的 token、TLS 和 kube-auth。面向同一 server 的工作**共享一个 connection 和一个 conduit**，故单一 SOCKS5 proxy 可同时按名称跨 docker container *和* compose service 工作。

## 本地 Web UI

[`cornus web`](/zh/cli/web) 是客户端侧浏览器界面。其 BFF 将服务器 workload 状态与 Compose 项目结构和存活的后台 agent inventory 结合，因为这些客户端持有的信息不存在于服务器扁平化 API 中。内嵌 SPA 提供 workload 与项目视图、依赖图、挂载、隧道与转发、仅限允许文件的编辑、日志和 exec。

UI 没有身份验证，因此仅限 loopback。项目应用会复用 `cornus compose ... up -d`，所以 CLI 和浏览器操作遵循同一收敛引擎及后台 agent 生命周期规则。

## 连接配置文件和远程集群

以前，访问位于*集群内部*的 Cornus server 需要手动运行 `kubectl port-forward` 并手动提供 token。连接配置文件以纯 client-side、无需改变 server 的方式消除了该缺口：

- **Profile** 是由 `cornus config` 管理的 kubeconfig-style context：endpoint、TLS material、可选 port-forward target 和可选 kube-auth block。一个 resolver 将所选 context 注入每个 client command；endpoint 优先级为 explicit flag > context server > auto port-forward，credential 优先级为 `CORNUS_TOKEN` > kube-auth mint > static profile token。
- **Auto port-forward**：指定 in-cluster Service 的 profile 会在 command 生命周期内打开 `kubectl port-forward` 等价物，并将 client 指向本地 forwarded address。`cornus config set-context --namespace <ns>` 在配置时发现 client-facing cornus Service；零个或多个匹配均为 hard error，并列出 candidate。
- **Kube-auth**：profile 可以通过 Kubernetes TokenRequest API 签发短生命周期、audience-scoped ServiceAccount token；in-cluster server 经已有 JWKS verify path 验证它，因此开发者的 kube access 可作为 Cornus credential，无需 server 上的 minting endpoint。

Profile 的 TLS config 同时应用于 REST transport 和每个 WebSocket dial，因此 remote build 与 deploy-attach session 也会遵循 custom CA 或 mTLS client cert。注意两种 credential 不同：kube credential 认证 port-forward *setup*，Cornus credential 认证 tunnel *内部*——TokenRequest 是二者的桥梁。

## Pull-ref registry host 与 client endpoint 解耦

镜像 identity 是其 **repository path**；host 则是按 vantage point 决定的 rendezvous 细节。此点在 client、build engine 和 node 不再共用 loopback 时十分重要：build engine 从 pod *内部* push，而 node 的 containerd 从 host network 结合 node DNS pull；port-forward endpoint（`127.0.0.1:<ephemeral>`）无法被 node pull。

因此 deploy image host 独立于 control-plane endpoint 解析：explicit override（`--registry` / `CORNUS_REGISTRY` / profile field）优先，否则 server 的免认证 info endpoint 声明一个，再否则使用 client endpoint host。声明值来自 `CORNUS_ADVERTISE_REGISTRY` 或 Kubernetes backend 对自身 Service 的 introspection；只有 **NodePort / LoadBalancer 自动声明**，因为 node containerd 使用 host DNS 而非 cluster DNS，ClusterIP name 在 pull 时无法解析。随后 server 会将 target host 等于声明 host 的 build **push-redirect**到 colocated registry 的 loopback，同时保持 repository path 固定，使 push 与 pull 以不同寻址访问同一内容。

## 相关页面

- [Compose、devcontainer 与 docker](/zh/guides/compose-devcontainers-docker)——工作流。
- [使用远程集群](/zh/guides/remote-clusters)——profile 和 kube-auth 设置。
- [连接配置](/zh/reference/connection-config)——profile 文件格式。
- [镜像仓库指南](/zh/guides/registry)——向 cluster runtime 声明 registry。
- [cornus compose](/zh/cli/compose) · [cornus daemon](/zh/cli/daemon) · [cornus config](/zh/cli/config)
