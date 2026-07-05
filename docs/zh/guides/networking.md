# 网络与 conduit

以下是访问 workload 的面向任务方法: 每 port forward、SOCKS5 split-tunnel，以及在两者之间做出选择的 session conduit。若要通过 hosted tunnel 公开 workload，请参阅[隧道指南](/zh/guides/tunnels)；若要将 workload *相互*连接，请参阅[工作负载 Hub](/zh/guides/hub)。

## 会话通道: port-forward 与 SOCKS5

会话向调用方暴露工作负载的方式称为其**通道模式**。默认是逐端口转发 (每个已发布端口一个本地监听器，兼容 Compose)。可选替代方案是单个客户端侧的 **SOCKS5 分流隧道代理**: 服务主机后缀 (默认为 `.cornus.internal`) 下的主机名会按名称隧道至对应工作负载，其他所有目标则直接从你的机器拨号。一个代理即可按名称访问每个服务，不需要逐端口监听器。

```sh
# Make SOCKS5 the conduit for a profile, so compose up / deploy --server use it:
cornus config set-context demo --conduit-mode socks5
# Pin the shared proxy's bind address and suffix in one value:
cornus config set-context demo --conduit-mode 'socks5://127.0.0.1:1085?suffix=.demo.internal'

# Per-run override (flag > CORNUS_CONDUIT > profile > default port-forward):
cornus compose up --conduit socks5                    # join the shared proxy
cornus compose up --conduit 'socks5://'               # own proxy, ephemeral port
cornus deploy --server http://cornus.example:5000 --conduit socks5 -f deploy.yaml
```

**conduit 由其地址标识。** 请求一个尚无人提供的地址就会启动代理；请求一个已被提供的地址 — 无论来自另一个会话、后台 agent 还是任何一方 — 就会**加入**它，把你的工作负载名称注册到已在运行的那个代理上。这正是浏览器一个代理设置就能到达全部内容的原因: 你自己的 Compose 服务、其他会话的服务、`cornus daemon docker` 容器，以及 Web UI。

加入的判定是包含关系而非完全相同，因此绑定在 `0.0.0.0:1080` 的代理同样服务于对 `127.0.0.1:1080` 的请求。反过来则不成立: 仅绑定在 loopback 的代理无法事后扩大范围，因此在它占用该端口期间请求 `0.0.0.0` 会被拒绝，并指出占用它的进程。

不带端口的 `socks5://` URL (或显式的 `:0`) 是私有的: 没有可供他方查找的约定地址，因此不会有人加入。需要有意运行一个专属代理时就用它。SOCKS5 CONNECT 仅支持 TCP。独立的临时代理是 [`cornus socks5`](/zh/cli/socks5)。

`socks5://` 选择器中的 bind 地址默认仅限 loopback: conduit 代理没有认证，并会从你的机器拨号到任意目的地，因此暴露到主机之外就等于给所有能访问它的人开放了一个代理。若要有意接受这种暴露，请加上 `--allow-non-loopback`:

```sh
# 被拒绝: 没有 opt-in 的主机外 bind
cornus compose up --conduit 'socks5://0.0.0.0:10080'
# 被接受: 同一个代理，且已知悉暴露风险
cornus compose up --conduit 'socks5://0.0.0.0:10080' --allow-non-loopback
```

该 flag 与 `--conduit` 一同出现在 [`compose up`](/zh/cli/compose) 和 [`deploy`](/zh/cli/deploy) 上，也与 `--listen` 一同出现在独立的 [`cornus socks5`](/zh/cli/socks5) 上。不加该 flag 时，bind 会在任何部署开始之前被拒绝。

**另请参阅: **[连接配置](/zh/reference/connection-config)、[使用远程集群](/zh/guides/remote-clusters)

## 将 local port forward 至 workload

为每个 mapping bind local listener，并将每条 connection forward 至 deployment 的第一个 instance，可访问从未发布的 port。

```sh
cornus port-forward web 8080:80 5432:5432
```

- 每个 mapping 为 `LOCAL:REMOTE`(或 bare `PORT`)，可选 `/tcp` 或 `/udp` suffix，例如 `cornus port-forward dns 5353:53/udp`。
- `--address 0.0.0.0` bind 所有 interface；UDP 在 dockerhost/containerd/bare backend 工作，但 Kubernetes port-forward 仅 TCP。

在 dockerhost backend 上，服务端会拨号到工作负载的容器 IP，因此需要有到它的路由。有两种情况不会自动成立，两者都会报告原因而不是超时:

- **`macvlan` / `ipvlan` 网络。** macvlan 容器无法从它自己的主机访问 — 这是该驱动的设计，而非配置错误。如果工作负载同时也在某个网桥网络上，cornus 会走那一侧拨号; 如果它只在 macvlan 上，`port-forward` 会说明这一点，你的选择是发布端口、再加一个网桥网络，或者从另一台机器连接。
- **远程 `DOCKER_HOST`。** 另一台机器守护进程上的容器 IP 在本地没有意义。设置 `CORNUS_DOCKER_REMOTE=1` (以及 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`) ，让服务端通过每实例的 companion 访问工作负载。

cornus 服务端本身是容器时还有第三种情况，它会被自动处理 — 参见[容器中的服务端](/zh/guides/server-in-a-container#访问工作负载)。

其他主机后端对同一个问题给出不同的答案:

- **`containerd` 和 `bare` 不需要任何配置。** cornus 用 CNI 亲自构建它们的网络，而且是在 cornus 自己所处的网络命名空间里构建，因此工作负载总是位于 cornus 能访问到的地方。容器化的服务端在那里付出的代价是已发布端口，而不是 port-forward: 没有主机网络时，端口发布的 NAT 规则会建立在服务端自己的容器内部。在 containerd 上 cornus 能检测到这一点并拒绝启动; 参见[容器中的服务端](/zh/guides/server-in-a-container#containerd)。
- **`incus`** 实例位于 incusd 自己的网桥上，守护进程所在主机总能路由到它。与 incusd *并列*容器化的服务端则不能，也无法加入该网桥 — cornus 容器不是一个 Incus 实例，因此没有与 docker 自我接入相对应的做法。请给该容器主机网络命名空间，或者把服务端本身作为一个 incus 实例运行 (此时它与工作负载一同位于网桥上，cornus 会识别出这一点) ，或者设置 `CORNUS_INCUS_REMOTE=1` (以及 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`) 通过每实例的 companion 访问各个实例。`port-forward` 会指名其中缺失的那一项，而不是直接超时。参见[容器中的服务端](/zh/guides/server-in-a-container#incus)。

**另请参阅: **[cornus port-forward](/zh/cli/port-forward)

## 运行 SOCKS5 split-tunnel proxy，按名称访问 service

Bind local SOCKS5 proxy，将带 service suffix 的 host tunnel 进 cluster，其他目标直接 dial。

```sh
cornus socks5
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

- 以 `--service-host-suffix`(默认 `.cornus.internal`)结尾的任何 host 均被 tunnel 至匹配 service；剥离 suffix 得出 service name。
- `--resolve 'PATTERN=REPLACE'` 是高级形式(有序、首个匹配获胜、sed-style `\1` backreference)，替代 suffix 默认行为。

**另请参阅: **[cornus socks5](/zh/cli/socks5)

## 为 deploy 或 compose session 选择 conduit

选择 `--server` session 如何向您暴露 workload port: 每 port listener 或一个 SOCKS5 proxy。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --conduit socks5
cornus compose up --conduit port-forward
```

- 优先级为 `--conduit`、然后 `CORNUS_CONDUIT`、最后 profile mode；`--no-forward-ports` 完全禁用 conduit。
- Bare word 仅设置 mode；`socks5://host:port[?suffix=SUFFIX]` URL 还设置 bind address 和 service-host suffix。

**另请参阅: **[cornus deploy](/zh/cli/deploy)

## 通过一个浏览器代理访问整个 Compose 栈和 Web UI

以 SOCKS5 模式运行 Compose 栈，并将 `cornus web` UI 发布到同一个共享 conduit，即可用一个浏览器代理设置按名称访问每个服务和 UI。

```sh
# 1. 为此 connection 设置 socks5 conduit (每个 profile 一次) 。
cornus config set-context --conduit-mode socks5

# 2. Detached 启动 stack。socks5 mode 下，background agent 会 host 一个 shared
#    proxy，并在其中注册每个 service 的 short name。
cornus compose up -d

# 3. 将 web UI 发布到同一个 shared conduit (不 bind local port) 。
cornus web --publish-in-conduit
```

将浏览器的 SOCKS5 代理指向 agent 代理 (`cornus socks5` 或配置文件中的监听地址，默认为 `127.0.0.1:1080`) ，并使用**远程 DNS** (SOCKS5h) 。一个设置即可访问以下所有地址:

- `http://web.cornus.internal/` — 名为 `web` 的 Compose 服务 (其短名称由 socks5 模式的 `compose up` 注册) 。
- `http://db.cornus.internal:5432/` — 同样通过短名称访问的其他服务。
- `http://cornus.internal/` — `cornus web` UI。

其工作方式如下:

- 三者共享一个后台 agent、一个连接和一个 SOCKS5 代理。`compose up -d`、`cornus daemon docker` 和 `cornus web --publish-in-conduit` 都会加入由连接及其 socks5 设置标识的同一个共享 conduit。
- 只有工作负载会话以 **socks5** 模式运行时，Compose 的*短名称* (`web`，而不是部署名称 `demo-web`) 才能解析。使用默认 port-forward 模式时，UI 和完整部署名称 (`demo-web.cornus.internal`) 仍可解析，但短名称不可解析。
- Web UI 自身不绑定端口；它只能从代理可达的位置访问，因此继承代理的 loopback 边界，不会新增公开面。
- `cornus web --publish-in-conduit` 无需自带 conduit 设置: 它会**加入** agent 已经为该连接运行的共享 conduit。若在其 `--conduit` 中指定地址或后缀，则会固定这些设置，这也是你有意另起一个代理的方式。
- `compose up -d` 和 `cornus daemon docker` 仍以各自的设置作为标识，因此这两者之间必须保持一致: 使用相同的 `--conduit` URL，或都依赖配置文件。不同的 `listen` / `suffix` 值会使后启动命令的代理与第一个代理争用绑定地址 — 此时 agent 会指名已占用该地址的会话并拒绝，而不是直接把原始的绑定错误抛给你。

**另请参阅:** [cornus web](/zh/cli/web)、[cornus compose](/zh/cli/compose)、[cornus socks5](/zh/cli/socks5)

## 通过 conduit 访问 workload ingress host

可访问用 `x-cornus-ingress` 声明的 host name(例如 `web.example.com`)，无需真实 DNS: 在 SOCKS5 session 中启用 `--ingress-conduit`。

```sh
# native: 隧道到真实 cluster ingress controller(需要 Kubernetes 和 kube access)。
cornus compose up --conduit socks5 --ingress-conduit native

# emulate: 使用生成证书的客户端侧 reverse proxy(任何 backend)。
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate
curl --socks5-hostname 127.0.0.1:1080 \
  --cacert ~/.local/share/cornus/ingress-ca.pem https://web.example.com/
```

- **native** 将浏览器的 SNI / `Host` 原样交给真实 controller，由其使用集群证书进行路由并终止 TLS。**emulate** 按 `Host` / path 代理到工作负载，并在本地终止 TLS；已安装时由 [mkcert](https://github.com/FiloSottile/mkcert) CA 签名 (`mkcert -install` 后浏览器会自动信任) ，否则使用只需信任一次的 self-signed CA (`~/.local/share/cornus/ingress-ca.pem`) 。
- 优先级为 `--ingress-conduit` > `CORNUS_INGRESS_CONDUIT` > 配置文件 (`cornus config set-context --ingress-conduit`) ；`off` 会禁用它。`cornus setup` 会探测集群并选择默认值。浏览器应使用**远程 DNS** (socks5h) 。

**另请参阅: **[Ingress](/zh/guides/ingress)、[cornus config](/zh/cli/config)
