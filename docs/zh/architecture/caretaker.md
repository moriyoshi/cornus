# Caretaker 和客户端侧功能

Cornus 的多项功能让运行在远程服务器或集群中的工作负载表现得仿佛就运行在开发者身旁：从调用方机器 bind mount 目录、让出站流量从调用方网络离开、使用调用方侧签发的 credential。在 Kubernetes 上，这些功能由每个 pod 中恰好**一个**注入 sidecar——**caretaker**——实现；host backend 则使用等价的 host-side 或 companion-container 机制。

## 客户端本地 bind mount

Stateless deploy 假定所有 mount source 都是 deploy host 上的路径。当调用方部署到*远程* server，却希望 bind mount *调用方*机器目录时，该假定失效。Cornus 复用 build transport：长期 WebSocket deploy-attach session 携带 deploy，调用方通过 **9P** 提供每个命名本地目录——调用方是 9P server，正如远程 build。使用 `cornus deploy --server <url> --local-mount SRC:DST[:ro]` 驱动。

各 backend 的服务器侧实现不同：

- **dockerhost** - server 在数据目录下以 kernel-9p 挂载每个 backing，并在调用 backend 前将 mount source 重写为该 mountpoint，使其像普通 host path 一样 bind。
- **kubernetes**——mount 在*pod 内*而非 node host 上实现，pod 可调度到任何位置。每个 mount，backend 注入共享 `emptyDir`、一个 privileged native-sidecar mount agent（以 `Bidirectional` propagation kernel-9p 挂载它），以及位于 target 的 app-container `volumeMount`。sidecar startup probe 会在 mount 存活前阻止 app container 启动。

Kernel 9P mount root 必须是目录。对于单个文件，dockerhost 会导出父目录，并通过 subpath bind basename。Kubernetes 的共享 sidecar mount 无法把单个文件投影到任意 rootfs target，因此会拒绝单文件源；目录 mount 仍受支持。containerd backend 当前不支持客户端本地 deploy mount。

由于 mount *由调用方提供*，deployment 的生命周期严格等于调用方连接存续期：session 断开时，handler 先移除 container，再 unmount 9P backing。这有意将功能限定于开发 / inner-loop，而非持久生产工作负载。读写 mount 使用可写的受限 export，因此 container 写入会回传调用方本地目录。

Pod 无法直接访问位于 NAT 后的调用方，因此**server 是 rendezvous**：它按 id 注册每个 attach session；pod caretaker 建立一个 pod-scoped connection，并为每个 mount 打开一个 stream，server 再将其 bridge 到调用方的 fresh stream。

## 一个 sidecar，多种 role

Caretaker 读取 Kubernetes backend 在 apply 时组装的单一 role config，并在一个 process 中运行 pod 所需的全部 role，因此一个工作负载永远不会携带多个 Cornus sidecar：

| Role | 与 server 通信？ | 功能 |
|------|:---:|---|
| `mount` | 是 | 经 server 将每个 9P mount relay 回调用方。 |
| `credential` | 是 | 通过 server relay 按需获取客户端签发 credential。 |
| `proxy` | 否 | 拦截 app-container egress，并按 compose network policy 转发到 peer Service。 |
| `egress` | 是 | 将 app 出站流量路由到客户端侧 vantage point（如下）。 |
| `dns` | 否 | 在 `127.0.0.1:53` 为 app container 提供 DNS；未知名称向上游转发。 |
| `hub` | 是 | 注册 hosted service，并经 [hub](/zh/architecture/networking#the-workload-to-workload-hub)访问 peer。 |
| `docker` | 是（client API） | 在 pod loopback 运行 Docker Engine API proxy，并通过 `DOCKER_HOST` 声明（如下）。 |

**Server-bound role 共用一个连接。**`mount`、`credential`、`egress` 和 `hub` 都搭乘同一个 pod-scoped、常驻的 server connection，并 multiplex 为 stream。session id 位于每条 stream 内，仍是不可猜测 capability。`proxy` 和 `dns` 是 app container 与集群之间自包含的 data-plane role，从不触及 server。

## Docker endpoint

`docker` role 让工作负载可**通过 loopback 访问 cornus 管理的 stack**：caretaker 在 pod-loopback endpoint 上运行与 `cornus daemon docker` 相同的 Docker Engine API proxy，backend 将 `DOCKER_HOST` 注入 app container。pod 内的标准 `docker` / `docker compose` 因此可驱动管理其自身 stack 的 server——部署 sibling workload、运行 compose、exec——pod 中无需真实 Docker daemon。通过 `DeploySpec.Docker` opt in（仅 Kubernetes）。

不同于使用 scoped caretaker credential 认证的其他 server-bound role，Docker proxy 使用完整**client** deploy API，该 API 按设计拒绝 caretaker scoped token。因此该 role 携带从 operator 提供的 dedicated Secret（`CORNUS_CLIENT_TOKEN_SECRET`）取得的**独立 client-scoped bearer token**。因这实质授予工作负载 deploy-engine access，只有配置该 token 时才启用；而且它不能与 enforcing proxy role 共存于一个 pod（后者会 redirect endpoint 自身的 dial）。

## 客户端侧 egress

工作负载出站流量通常从 runtime 所在处离开。对于**air-gapped cluster**（只有开发者机器能访问互联网）和 **VPN / corporate-proxy / SASE** 网络（合规 egress path 位于客户端侧），这会失效。客户端侧 egress 将远程 container 的出站流量经客户端侧 vantage point 路由，透明度从低到高分为三种模式：`env`（传播调用方 proxy variable）、`proxy`（caretaker 在 loopback 运行真实 HTTP + SOCKS5 proxy）和 `transparent`（nftables redirect 捕获所有 app TCP）。模式、route 和 PAC 用法参见[客户端侧 egress](/zh/topics/egress)；架构上重要的是 relay 形状及其保证：

- **Reverse relay。**Caretaker 依据 routing policy 分类每条连接目标；需 relay 的 route 在 pod-scoped server connection 上打开 stream，server 将其 bridge 到*客户端* deploy-attach session。客户端通过**自身**已解析的 proxy（enterprise HTTP/SOCKS proxy 或 SASE gateway，遵守 `NO_PROXY`）dial target——字节从客户端物理离开，与客户端自身流量完全相同。
- **每一跳均重新评估 policy。**Server 再次检查 policy（defense in depth：受损 pod 无法提升自己的路由），client 最后再次检查。目标默认走 `cluster` route，因此启用 egress 不会静默改道集群内流量；PAC script 在有 deadline 上限的 sandboxed JS engine 中执行，错误或超时时 fail closed 到 `deny`。
- **两个终点。**`client` route 需要 live session（inner loop）。`gateway` route 不需要 client：server 自身是 egress node，因此 `--detach` workload 即使无人连接也能继续 egress——由 operator opt-in `CORNUS_EGRESS_GATEWAY` 和可选 policy ceiling 约束，pod 的请求永远不能超出 operator 许可。

在 host backend 上，`proxy`/`transparent` mode 作为共享工作负载 network namespace 的**companion caretaker** container 运行。

## 相关页面

- [远程工作流](/zh/topics/remote-workflows)——实际中的 mount 和 session。
- [客户端侧 egress](/zh/topics/egress)和[egress 指南](/zh/guides/egress)。
- [凭据代理](/zh/topics/credentials)和[凭据指南](/zh/guides/credentials)。
