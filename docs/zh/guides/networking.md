# 网络与 conduit

以下是访问 workload 的面向任务方法: 每 port forward、SOCKS5 split-tunnel，以及在两者之间做出选择的 session conduit。若要通过 hosted tunnel 公开 workload，请参阅[隧道指南](/zh/guides/tunnels)；若要将 workload *相互*连接，请参阅[工作负载 Hub](/zh/guides/hub)。

## 会话通道: port-forward 与 SOCKS5

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

**另请参阅: **[连接配置](/zh/reference/connection-config)、[使用远程集群](/zh/guides/remote-clusters)

## 将 local port forward 至 workload

为每个 mapping bind local listener，并将每条 connection forward 至 deployment 的第一个 instance，可访问从未发布的 port。

```sh
cornus port-forward web 8080:80 5432:5432
```

- 每个 mapping 为 `LOCAL:REMOTE`(或 bare `PORT`)，可选 `/tcp` 或 `/udp` suffix，例如 `cornus port-forward dns 5353:53/udp`。
- `--address 0.0.0.0` bind 所有 interface；UDP 在 dockerhost/containerd/bare backend 工作，但 Kubernetes port-forward 仅 TCP。

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

## 通过一个浏览器代理访问整个 Compose stack 和 web UI

以 SOCKS5 mode 运行 Compose stack，并将 `cornus web` UI 发布到同一个 shared conduit。一个浏览器 proxy setting 就能按名称访问每个 service 和 UI。

```sh
# 1. 为此 connection 设置 socks5 conduit(每个 profile 一次)。
cornus config set-context --conduit-mode socks5

# 2. Detached 启动 stack。socks5 mode 下，background agent 会 host 一个 shared
#    proxy，并在其中注册每个 service 的 short name。
cornus compose up -d

# 3. 将 web UI 发布到同一个 shared conduit(不 bind local port)。
cornus web --publish-in-conduit
```

将浏览器 SOCKS5 proxy 指向 agent proxy，默认 `127.0.0.1:1080`，并使用**远程 DNS**(SOCKS5h)。一个设置即可访问 `web.cornus.internal` 的 Compose service、其他 service 的 short name，以及 `cornus.internal` 的 web UI。

- 三者共享同一个 background agent、connection 和 SOCKS5 proxy。
- 只有 workload session 以 **socks5** mode 运行时才会注册 Compose short name。默认 port-forward mode 下，service 仍以完整 deployment name 解析。
- 各命令的 conduit setting 必须一致: 使用相同的 `--conduit` URL，或全部使用 profile。

**另请参阅: **[cornus web](/zh/cli/web)、[cornus compose](/zh/cli/compose)、[cornus socks5](/zh/cli/socks5)

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

- **native** 将浏览器 SNI 和 `Host` 直接交给真实 controller。**emulate** 按 `Host`/path 代理到 workload，并在本地终止 TLS。安装并执行 `mkcert -install` 后会使用 mkcert CA；否则使用一次性信任的 self-signed CA。
- 优先级是 `--ingress-conduit`、`CORNUS_INGRESS_CONDUIT`、profile；`off` 禁用它。`cornus setup` 会探测 cluster 并选择默认值。浏览器使用**远程 DNS**(SOCKS5h)。

**另请参阅: **[Ingress](/zh/guides/ingress)、[cornus config](/zh/cli/config)
