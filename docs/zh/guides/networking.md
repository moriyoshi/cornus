# 网络操作

以下是访问 workload 的面向任务方法: 每 port forward、SOCKS5 split-tunnel 和 workload-to-workload hub。若要通过 hosted tunnel 公开 workload，请参阅[隧道指南](/zh/guides/tunnels)。这些操作背后的模型见[远程工作流](/zh/topics/remote-workflows)和[workload hub](/zh/topics/hub)。

## 将 local port forward 至 workload

为每个 mapping bind local listener，并将每条 connection forward 至 deployment 的第一个 instance，可访问从未发布的 port。

```sh
cornus port-forward web 8080:80 5432:5432
```

- 每个 mapping 为 `LOCAL:REMOTE`(或 bare `PORT`)，可选 `/tcp` 或 `/udp` suffix，例如 `cornus port-forward dns 5353:53/udp`。
- `--address 0.0.0.0` bind 所有 interface；UDP 在 dockerhost/containerd/bare backend 工作，但 Kubernetes port-forward 仅 TCP。

**另请参阅: **[cornus port-forward](/zh/cli/port-forward)、[远程工作流](/zh/topics/remote-workflows)

## 运行 SOCKS5 split-tunnel proxy，按名称访问 service

Bind local SOCKS5 proxy，将带 service suffix 的 host tunnel 进 cluster，其他目标直接 dial。

```sh
cornus socks5
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

- 以 `--service-host-suffix`(默认 `.cornus.internal`)结尾的任何 host 均被 tunnel 至匹配 service；剥离 suffix 得出 service name。
- `--resolve 'PATTERN=REPLACE'` 是高级形式(有序、首个匹配获胜、sed-style `\1` backreference)，替代 suffix 默认行为。

**另请参阅: **[cornus socks5](/zh/cli/socks5)、[远程工作流](/zh/topics/remote-workflows)

## 为 deploy 或 compose session 选择 conduit

选择 `--server` session 如何向您暴露 workload port: 每 port listener 或一个 SOCKS5 proxy。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --conduit socks5
cornus compose up --conduit port-forward
```

- 优先级为 `--conduit`、然后 `CORNUS_CONDUIT`、最后 profile mode；`--no-forward-ports` 完全禁用 conduit。
- Bare word 仅设置 mode；`socks5://host:port[?suffix=SUFFIX]` URL 还设置 bind address 和 service-host suffix。

**另请参阅: **[远程工作流](/zh/topics/remote-workflows)、[cornus deploy](/zh/cli/deploy)

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

**另请参阅: **[公共 ingress](/zh/topics/ingress)、[cornus config](/zh/cli/config)

## 作为 spoke 加入 workload-to-workload hub

将本 host 连接至 overlay，以提供 local service 和 / 或按名称访问 overlay service。

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```

- `--register name=host:port` 提供 local service(relay 至此 spoke，因此 NAT 后 host 仍可达)；`--reach name=listen_ip:port` bind local listener 至 overlay。至少需要一个。
- Server 从 `--server` 或 selected profile 解析；目前携带 client-TLS material 的 profile 被 `hub` 拒绝。

**另请参阅: **[cornus hub](/zh/cli/hub)、[workload hub](/zh/topics/hub)

## 通过 hub 在 workload 间 export 和 import service

对于 Kubernetes 上部署的 workload，在 deploy spec 中声明 hub membership，而不是使用 CLI。

```yaml
name: api
image: localhost:5000/api:v1
hub:
  identity: api                 # policy identity (defaults to the deployment name)
  export:
    - { name: api, port: 8080 }
    - { name: udpecho, port: 9000, protocol: udp, deliver: true }
  import:
    - { name: db, ports: [5432] }
```

- `export` 列出此 workload host 的 service；`import` 列出它访问的 service(每个 import 均配置 synthetic loopback IP 与 DNS record，因此普通 `dial(peer)` 会进入 hub)。
- 对 hub 无法直接访问的 export 设置 `deliver: true`；`importDynamic` opt in dynamic catalog discovery。`hub:` 仅 Kubernetes。

**另请参阅: **[Deploy spec](/zh/reference/deploy-spec)、[workload hub](/zh/topics/hub)
