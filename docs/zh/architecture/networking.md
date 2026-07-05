# 网络：端口转发、tunnel、ingress 和 hub

四种机制向工作负载传递流量，分别面向不同方向：**端口转发**将工作负载端口带到调用方机器，**公共 tunnel**将端口暴露至互联网，**ingress**是集群原生前门，**hub**跨网络连接工作负载。它们底层共享 transport——除 ingress 外，均通过同样的 server-bridged byte tunnel。

## 端口转发

`cornus port-forward --server <url> <name> [LOCAL:]REMOTE ...` 将本地 TCP port 转发至 deployment 第一个 instance 的 container port。CLI 为每个 mapping bind 一个 local listener，并为每个 accepted connection 打开独立 WebSocket tunnel 到 server，再由 server bridge 到 backend；因此功能与 backend 无关，并可访问 workload 从未发布的 port：

- **dockerhost** inspect container IP 并直接 dial。假设 server 可 route 至 Docker bridge；server 在 Docker host 上或与之共处的正常部署满足此条件。
- **kubernetes** 经 API server 的 `pods/portforward` SPDY subresource 传输，因此可使用 out-of-cluster kubeconfig 工作，无需 sidecar 或 pod network route。Kubernetes port-forward 仅 TCP。
- **containerd** 直接 dial instance 记录的 CNI IP。

Cluster connection profile 上，client 默认**不**让 Kubernetes forward 经 server：server 自身 ServiceAccount 通常缺少 `pods/portforward` RBAC，server-proxied forward 会静默失败。Client 改用开发者自身 kubeconfig 直接 dial workload pod，且仅在直接尝试无法开启 tunnel 时将 server-proxied path 作为 pre-traffic fallback。`via-server` profile toggle 强制 server-routed path；同样的 direct-first、proxy-fallback 规则也适用于 workload log。

**UDP mapping**（`5353:53/udp`）在 dockerhost 和 containerd backend 工作：tunnel 携带 length-framed datagram，每个 client source address 一个 tunnel，并有 idle timeout。Server 在第一帧前 ack UDP tunnel，因此无法支持的 backend 或旧 server 会干净地拒绝 dial。

**已发布 port 自动转发。**每个 client session surface——`cornus deploy --server`、`cornus compose up`（foreground 或 `-d`）和 docker frontend——均通过同一引擎发布 `DeploySpec.Ports`，因此任意 `host:` port 在各 backend 上均意味着“在 client 的 `127.0.0.1:<host>` 可达”。各 surface 都有 `--no-forward-ports`；无法转发的 UDP mapping 或已被 bind 的本地 port 会 warning 并继续，而不会令 session 失败。工作流参见[网络指南](/zh/guides/networking)。

## 公共 tunnel

端口转发向调用方提供 local listener，而 `cornus tunnel <name> <port>` 返回**公共 URL**。Server 在进程内 host tunnel，并通过与 port-forward 相同的 byte-bridge 将每条 inbound connection bridge 到 workload；因此它在所有 backend 上均可访问未发布 port，tunnel 只是位于该 bridge 前方的 hosted relay。

Client 在已认证 request 中注入 tunnel credential；server 事先不知道 credential（operator 可设 server-side default）。Provider seam 有两种形态：对提供真实 listener、由 server accept 的 backend 使用 **listener model**（ngrok）；只能转发至 local URL 的 backend（cloudflared、tailscale）使用 **upstream model**，server 建立 loopback shim 并交给 backend 地址。随附四种 backend：ngrok（进程内、默认）、ssh（带 fail-closed host-key verification 的 remote-forward；可用 sish、serveo 或普通 sshd）、cloudflare 和 tailscale。各后端所需条件参见[后端表格](/zh/guides/tunnels)，分步设置说明参见[隧道指南](/zh/guides/tunnels)。

## 自动 ingress（仅 Kubernetes）

Tunnel 是 server 每次调用时运行的 hosted relay，**ingress**则是 cluster-native front door：`DeploySpec.Ingress` opt in 时，Kubernetes backend 与 ClusterIP Service 一并创建标准 Ingress，并通过 owner reference 关联 Deployment，以便 delete 时清理。其他 backend 记录 warning 并忽略字段，使 Compose 文件保持 portable。

其独有能力是**自动 host 派生**，面向 ephemeral preview environment。在 server 配置 base domain（`CORNUS_INGRESS_DOMAIN`，另有 `CORNUS_INGRESS_CLASS` 和 `CORNUS_INGRESS_TLS_ISSUER`）时，仅*启用* ingress 的 deploy 即可免费获得 public URL：Compose translator 派生 `<service>.<project>`，backend 再加上 domain 前缀——`web.pr-123.<domain>`——因此许多项目可共用一个 wildcard domain。每个默认值都可由 spec 中 client override；multi-tenant server 可使用 `CORNUS_INGRESS_ENFORCE_DOMAIN` 固定 domain，拒绝所有解析结果在该 domain 外的 host，防止 client 申请任意 hostname。完整字段参考参见[Ingress](/zh/guides/ingress)。

与 tunnel 的取舍是：ingress 是 cluster-native，能够跨 detached deploy 存活，但需要 ingress controller、wildcard DNS（HTTPS 还需 cert issuer）；`cornus tunnel` 不需要这些，在任意 backend 可工作，但只在其 command 存活期间存在。

## Session conduit：port-forward 或 SOCKS5

自动 forward 是 client session 向调用方暴露 workload 的默认 **conduit mode**。可 opt-in 的替代方案是用单个 client-side **SOCKS5 split-tunnel proxy** 替换每 port local listener：CONNECT target 会与 resolution rule 匹配；匹配时重写为 `service:port` 并通过同一 transport 向内 tunnel，未匹配 target 则由调用方 host 直接 dial——cluster name 向内，其余正常 egress。

日常默认 rule 去掉 service-host suffix：`web.cornus.internal:8080` 到达 `web` service，因此一个 proxy 无需预先声明 port 即可按名称访问所有 service。Session alias table 还将短 Compose service name 映射到 project-prefixed deployment；当其无歧义地命名 live service 时，裸 `web:8080` 也向内路由。Mode 按 session 解析（`--conduit` flag、`CORNUS_CONDUIT` 或 connection profile），共享 proxy 可跨同一 connection 上的 Compose service 与 Docker container；`cornus socks5` 以 standalone 方式运行同一 proxy。SOCKS5 CONNECT 仅 TCP。配置与优先级参见[网络与 conduit](/zh/guides/networking)。

## 工作负载到工作负载 hub

Server 同时是连接没有可路由网络的 workload——跨 node、跨 cluster 或 NAT 后笔记本电脑——的 **star hub**。每个 participant 是一个 **spoke**（pod caretaker 或 CLI 的 `cornus hub`）；spoke 注册其 host service，并按名称访问其他 spoke service。

### Relay 模型

Spoke 以两种 mode 注册每个 service：

- **dial-direct**——注册 hub 可达 address；hub 自己 dial。
- **delivery**——无 address 注册；hub 通过向 hosting spoke *反向*打开 ingress stream 到达服务，由 spoke dial 自己的 local target 并 splice。这让 NAT 后和跨 cluster target 可达——hub 无需通向它们的 route。

每 spoke 一个 connection 承载 control、egress stream 和 ingress stream；流量为 `app -> caretaker -> hub -> {dial | ingress to spoke}`。Relay 与 byte 无关，因此 **TCP 和 UDP**均可工作——datagram 在 stream 上 length-frame，并在两端转换回来。

### Discovery 和 policy

Import peer 确定性映射到一个 **synthetic `127.0.0.0/8` IP**。Kubernetes 上，caretaker DNS role 在与其 loopback listener bind 的同一 synthetic IP 上提供每 peer name，因此 app 的普通 `dial(peer)` 无需应用感知即可进入 hub。Import 可为**动态**：有 `watch` capability 的 spoke 在 control stream 接收 catalog update，并为每个 discovered service bind listener。

Policy 为两个可选 matrix，仅在配置时强制：**reach** matrix（caller identity 到允许 callee service，`CORNUS_HUB_POLICY`）和 **register** matrix（identity 到可 host name，`CORNUS_HUB_REGISTER_POLICY`）。mTLS 下 identity 来自 verified client certificate，因此 policy 基于 spoke 不能伪造的 credential。

### 运行多个 replica

Delivery target 持有到其 spoke 的 live session，只有持有 spoke connection 的 replica 可打开到它的 ingress stream。关键简化是：每 replica 是其所连 spoke 的*唯一 authority*，所以 replica 拥有**不相交 registry partition**——distributed registry 就是它们的 union，没有 write conflict 或 merge logic。dead replica 的 partition 消失；其 spoke 经 load balancer reconnect，并在新 owner 下重新注册。

随附两种 shared store：**Redis**（`CORNUS_HUB_REDIS`；liveness 是 TTL heartbeat key）和 **Kubernetes-native store**（`CORNUS_HUB_STORE=kube`；provider 为 custom resource，liveness 为 Lease，GC 使用 owner reference——无需外部基础设施）。Lookup 到达非 delivery owner 的 replica 时，会 forward 到 owner 并 splice，形成 two-hop path。准确表述是：hub 是 control-plane relay，单 replica 对大多数部署已足够；多 replica 以额外 hop 为代价提供 overlay HA 与 connection-count scale。

## 相关页面

- [网络与 conduit](/zh/guides/networking)——conduit mode，以及转发与访问 workload 的步骤。
- [隧道](/zh/guides/tunnels)——tunnel backend 与各后端设置。
- [工作负载 Hub](/zh/guides/hub)——覆盖网络的 relay 模型与用法。
- [Ingress](/zh/guides/ingress)——ingress 字段参考。
- [使用远程集群](/zh/guides/remote-clusters)——连接配置文件。
- [cornus port-forward](/zh/cli/port-forward) · [cornus tunnel](/zh/cli/tunnel) · [cornus socks5](/zh/cli/socks5) · [cornus hub](/zh/cli/hub)
