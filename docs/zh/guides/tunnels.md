# 隧道

**公网隧道** (`cornus tunnel`) 通过托管中继将一个工作负载端口暴露到公网，无需集群原生的 Ingress 资源，也无需在网络路径上发布端口。可用它分享进行中的工作、接收 webhook，或在手机上测试。如果需要由真正的 Ingress 资源提供的持久主机名，而不是托管中继，请参阅 [Ingress](/zh/guides/ingress)。

## 工作原理

`cornus tunnel <name> <port>` 会为工作负载端口返回一个**公网 https URL**，并持续运行直到 `Ctrl-C`。Cornus **服务器**托管隧道，并通过与 port-forward 相同的字节桥接将每个入站连接转接给工作负载，因此能在任何后端 (Docker host、containerd 或 Kubernetes) 上访问工作负载从未发布的端口。

```sh
cornus tunnel [--authtoken TOKEN | --authtoken-file FILE] [--proto http|tcp] <name> <port>
```

```sh
cornus tunnel --server http://cornus.example:5000 \
  --authtoken "$NGROK_AUTHTOKEN" web 80
```

隧道凭据由客户端在已认证的请求中注入；服务器事先不会得知它。运维人员也可将 `CORNUS_TUNNEL_AUTHTOKEN` 设置在服务器上作为默认凭据，使调用方无需提供 `--authtoken`。使用 `--proto http` / `--proto tcp` 选择 HTTP 或原始 TCP。完整标志集请参阅 [`cornus tunnel`](/zh/cli/tunnel)。

## 后端

隧道后端通过服务器上的 `CORNUS_TUNNEL_BACKEND` 选择 (默认 `ngrok`)。具体后端可插拔，且仅选中的后端会处于活动状态。四种后端共享同一个客户端命令；仅服务器端的 `CORNUS_TUNNEL_BACKEND` 及其对应后端的环境变量有所不同。

| 后端 | 注入的凭据 | 说明 |
| --- | --- | --- |
| `ngrok` (默认) | ngrok authtoken (`NGROK_AUTHTOKEN`) | 进程内 ngrok agent，不启动子进程 |
| `ssh` | SSH 私钥 (PEM)、密码，或转发的 ssh-agent (`--forward-agent`) | SSH remote-forward 至可自行托管的隧道服务器 (sish、serveo、pinggy、localhost.run、启用 GatewayPorts 的普通 `sshd`)；复用内置 SSH 栈 |
| `cloudflare` | 无 (匿名) | 通过 `cloudflared` 二进制文件 (`CORNUS_TUNNEL_CLOUDFLARED_BIN`) 使用 Cloudflare quick tunnel |
| `tailscale` | 无 | 通过 `tailscale` 二进制文件使用 Tailscale Funnel；节点在带外加入 tailnet，因此每个节点一个 Funnel |

对于 `ssh` 后端，使用 `CORNUS_TUNNEL_SSH_ADDR` / `CORNUS_TUNNEL_SSH_USER` 配置端点，使用 `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` 或 `CORNUS_TUNNEL_SSH_HOSTKEY` 配置主机密钥验证 (故障闭合；`CORNUS_TUNNEL_SSH_INSECURE=1` 仅供开发使用)。完整的环境变量参考见[服务器环境变量](/zh/reference/server-env-vars)。

## 安全地传递凭据

`--authtoken TOKEN` 会将 secret 直接放入 argv，机器上的其他用户可以通过 `ps` 读取它，shell 也常会将其写入历史记录——除了快速的本地测试外都应避免这种方式。按优先顺序推荐：完全不提供凭据 (服务器已有默认值——见下文)、使用对应后端的环境变量 (kong 会自动将其读入 `--authtoken`，因此该值不会出现为命令行参数)、或使用 `--authtoken-file FILE` (从文件读取 secret，使其既不出现在 argv 中也不出现在历史记录中)。下面的每个步骤都采用环境变量或文件的形式。

## 使用 ngrok 暴露工作负载 (默认)

默认后端 — 无需安装额外的二进制文件，除了 authtoken 外也无需其他服务器端网络设置。

1. 在 [ngrok.com](https://ngrok.com) 登录，从控制台的 "Your Authtoken" 页面复制你的 authtoken。
2. 既可以在每次客户端调用时提供该 token，也可以将其设置为服务器端默认凭据：
   ```sh
   # 客户端侧：导出一次即可自动读取——设置后 cornus 无需在命令行上再传入 --authtoken。
   export CORNUS_TUNNEL_AUTHTOKEN=2ab3...
   cornus tunnel web 80
   ```
   或者将*同一个变量名*设置为服务器端默认值 (systemd unit、容器环境变量、Helm `values.yaml`，无论服务器进程从哪里获取环境变量)，使客户端调用完全无需提供凭据——这是两个不同进程各自环境中的同一个变量名，而非共享同一个值：
   ```
   CORNUS_TUNNEL_AUTHTOKEN=2ab3...
   ```
   在客户端一侧，`NGROK_AUTHTOKEN` 作为旧版别名仍然可用。
3. cornus 会打印公网 `https://<random>.ngrok-free.app` URL，并阻塞直到 `Ctrl-C`，此时隧道会被拆除。

- `CORNUS_TUNNEL_BACKEND` 默认已经是 `ngrok`，因此无需选择服务器端后端。
- ngrok agent 在服务器上以进程内方式运行，无需安装任何东西。
- 免费 ngrok 账号每次运行都会得到一个新的随机子域名；付费方案可固定一个稳定的子域名。

**另请参阅：** [cornus tunnel](/zh/cli/tunnel)

## 通过 SSH 反向转发暴露工作负载

复用 cornus 内置的 SSH 栈，对接任何接受 SSH remote-forward (`ssh -R`) 的端点 — 可以是自行托管的中继 (sish、启用了 `GatewayPorts yes` 的普通 `sshd`)，也可以是公共服务 (serveo.net、pinggy.io、localhost.run)。

1. 选择或搭建一个接受 `ssh -R` 的 SSH 隧道端点。
2. 在服务器的环境变量中设置以下内容，让服务器指向它 (systemd unit、容器环境变量、Helm `values.yaml`)：
   ```
   CORNUS_TUNNEL_BACKEND=ssh
   CORNUS_TUNNEL_SSH_ADDR=tunnel.example.com:22
   CORNUS_TUNNEL_SSH_USER=cornus
   ```
   `CORNUS_TUNNEL_SSH_USER` 未设置时默认为 `cornus`；`CORNUS_TUNNEL_SSH_BIND` 默认为 `0.0.0.0:0` (让远端自行选择端口)。
3. 配置主机密钥验证。该后端采用故障闭合策略 — 以下之一是必需的：
   ```sh
   CORNUS_TUNNEL_SSH_KNOWN_HOSTS=/etc/cornus/known_hosts
   # 或者固定单个密钥：
   CORNUS_TUNNEL_SSH_HOSTKEY="ssh-ed25519 AAAA... tunnel.example.com"
   # 仅供开发使用，完全跳过验证：
   CORNUS_TUNNEL_SSH_INSECURE=1
   ```
4. 告诉 cornus 如何推导公网 URL。如果中继会在 SSH session banner 中打印自己的 URL (sish、serveo、pinggy 都会这样做)，可以自动获取：
   ```sh
   CORNUS_TUNNEL_SSH_URL_FROM_SESSION=1
   ```
   否则可根据绑定的远端端口用模板生成：
   ```sh
   CORNUS_TUNNEL_SSH_URL_TEMPLATE='https://{port}.tunnel.example.com'
   ```
5. 通过以下两种方式之一提供 SSH 凭据。

   - **服务器端共用的身份**——未加密的私钥 PEM 或密码，取决于中继接受哪一种。由于中继的 SSH 握手发生在**服务器**端而非客户端，这通常是整个 cornus 服务器共用的一个服务身份，而非按调用者区分的凭据，因此建议将其设置一次作为服务器端默认值，让客户端完全无需提供凭据：
     ```
     CORNUS_TUNNEL_AUTHTOKEN=<PEM 内容，或一个密码>
     ```
     如果确实需要按调用者区分凭据，请在客户端从文件读取，而不是将其放入 argv：
     ```sh
     cornus tunnel --authtoken-file ~/.ssh/id_ed25519 web 80
     ```
   - **转发的 ssh-agent**——密钥材料完全不会离开客户端；服务器的 SSH 握手改为请求调用方本地的 `ssh-agent` 对挑战进行签名。
     ```sh
     cornus tunnel --forward-agent web 80
     ```
     这是使用受密码保护的密钥进行认证的唯一方式，因为持有已解密密钥的是 agent 而非 cornus。与 `ssh -A` 一样，只应对信任的 cornus 服务器使用 `--forward-agent`：在隧道启动期间，服务器可以请求转发的 agent 对任意挑战签名，而不仅限于来自中继的挑战。cornus 仅在 SSH 握手期间访问 agent，而不是在隧道的整个生命周期内。

- 不支持将受密码保护的私钥直接作为 `--authtoken` 传入 — 这种情况请使用 `--forward-agent`，或改用未加密的密钥或密码。
- 没有 known-hosts 文件、没有固定的主机密钥，也没有选择不安全模式时，连接会被拒绝，而不是信任未经验证的主机。

**另请参阅：** [cornus tunnel](/zh/cli/tunnel)、[服务器环境变量](/zh/reference/server-env-vars)

## 使用 Cloudflare Tunnel 暴露工作负载

匿名的 Cloudflare "quick tunnel" — 无需 Cloudflare 账号、API token 或 DNS zone。该后端通过调用外部 `cloudflared` 二进制文件实现，而官方发布的 cornus 镜像并未内置它 — 如果服务器以容器方式运行，需要在此基础上构建自定义镜像：

```dockerfile
FROM ghcr.io/moriyoshi/cornus:latest
RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && curl -fsSL -o /usr/local/bin/cloudflared \
         https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
    && chmod +x /usr/local/bin/cloudflared \
    && apt-get purge -y curl && rm -rf /var/lib/apt/lists/*
```

部署该镜像以取代官方镜像 (更新 Helm 的 `image.repository` / `image.tag` 值，或 k8s manifest)。若服务器直接运行在主机上而非容器中，则只需在该主机上安装 `cloudflared` 即可。

1. 安装 `cloudflared` — 通过上面的自定义镜像，或者在服务器未容器化时直接安装在主机上。
2. 如果它不在 `PATH` 中，在服务器的环境变量中让 cornus 指向它：
   ```
   CORNUS_TUNNEL_CLOUDFLARED_BIN=/usr/local/bin/cloudflared
   ```
3. 在服务器上选择该后端：
   ```
   CORNUS_TUNNEL_BACKEND=cloudflare
   ```
4. 运行隧道 — 无需 `--authtoken`，该后端是匿名的：
   ```sh
   cornus tunnel web 80
   ```
5. cornus 会打印一个 `https://<random-words>.trycloudflare.com` URL。

- Quick tunnel 是临时性的：主机名每次运行都会变化。目前尚不支持 named tunnel (通过 Cloudflare 账号 token 绑定到自有域名的稳定主机名)。

**另请参阅：** [cornus tunnel](/zh/cli/tunnel)

## 使用 Tailscale Funnel 暴露工作负载

通过一个已经加入你的 tailnet 的节点发布服务 — 完全不需要 cornus 管理的凭据；节点的 tailnet 成员身份即是授权。该后端通过调用外部 `tailscale` 二进制文件实现，而官方发布的 cornus 镜像并未内置它。`sudo tailscale up` 是一条面向长期运行主机的交互式命令 — 它不是可以对着一个短暂存在的 pod 手动运行的东西，因此两种部署形态需要不同的设置方式。

### 在 Kubernetes 上，通过 Helm chart

Chart 可以将 `tailscaled` 作为 sidecar 运行，使其以非交互方式加入 tailnet，并与 cornus 容器共享 `tailscale` CLI 二进制文件 — 无需自定义镜像，也无需手动执行 `tailscale up`。

1. 在 Tailscale 管理控制台中创建一个 tailnet auth key — **可复用**，并最好打上 **ephemeral** 标签，因为 sidecar 的状态不会在 pod 重启后持久化，而 ephemeral 节点会在断开连接时自动注销，不会在 tailnet 中不断累积：
   ```sh
   kubectl create secret generic cornus-tailscale-authkey \
     --from-literal=authkey=tskey-auth-...
   ```
2. 在 Tailscale 管理控制台中为该 tailnet 启用 HTTPS 证书 (**DNS → Enable HTTPS**)，并通过 tailnet ACL 策略为该节点授予 Funnel 属性 (在 `nodeAttrs` 中添加带 `funnel` 属性的条目 — 具体 ACL 写法请参阅 Tailscale 的 Funnel 文档)。
3. 在 `values.yaml` 中启用 sidecar (或使用 `--set`)：
   ```yaml
   tailscale:
     enabled: true
     authKeySecret: cornus-tailscale-authkey
   ```
   这会自动为 cornus 容器设置 `CORNUS_TUNNEL_BACKEND`、`CORNUS_TUNNEL_TAILSCALE_BIN` 和 `TS_SOCKET` — chart `values.yaml` 中的 "tailscale" 块列出了完整的可配置项 (hostname、image、额外的 `tailscale up` 参数)。
4. 运行隧道 — 无需 `--authtoken`：
   ```sh
   cornus tunnel web 80
   ```
5. cornus 会打印该节点的公网 `https://<node>.ts.net/` URL。

### 其他场景：纯主机部署，或 Helm chart 之外的容器

该后端通过调用外部 `tailscale` 二进制文件实现，而官方发布的 `ghcr.io/moriyoshi/cornus:latest` 镜像并未内置它。如果服务器以 Helm chart 之外的容器方式运行 (直接 `docker run`、手写的 k8s manifest、`docker compose`)，需要在此基础上构建自定义镜像：

```dockerfile
FROM ghcr.io/moriyoshi/cornus:latest
RUN apt-get update && apt-get install -y --no-install-recommends curl gnupg \
    && curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg \
         -o /usr/share/keyrings/tailscale-archive-keyring.gpg \
    && curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list \
         -o /etc/apt/sources.list.d/tailscale.list \
    && apt-get update && apt-get install -y --no-install-recommends tailscale \
    && apt-get purge -y curl gnupg && rm -rf /var/lib/apt/lists/*
```

在其旁边运行 `tailscaled` (作为共享 pod / 主机网络命名空间的 sidecar 容器，或同一容器内的第二个进程)，并部署该自定义镜像以取代官方镜像。若服务器直接运行在纯主机上，则无需自定义镜像，只需在该主机上安装 Tailscale 即可。

1. 安装 Tailscale — 通过上面的自定义镜像，或者在服务器未容器化时直接安装在主机上 — 并将其加入你的 tailnet：
   ```sh
   sudo tailscale up
   ```
2. 按照上面相同的 Tailscale 管理控制台步骤操作 (启用 HTTPS 证书、授予 Funnel 属性)。
3. 如果 `tailscale` 不在 `PATH` 中，在服务器的环境变量中让 cornus 指向它：
   ```
   CORNUS_TUNNEL_TAILSCALE_BIN=/usr/bin/tailscale
   ```
4. 在服务器上选择该后端：
   ```
   CORNUS_TUNNEL_BACKEND=tailscale
   ```
5. 运行隧道 — 无需 `--authtoken`：
   ```sh
   cornus tunnel web 80
   ```
6. cornus 会打印该节点的公网 `https://<node>.ts.net/` URL。

- 一个节点在同一时间只能在 443 端口上提供一个 Funnel，因此同一服务器主机上的并发隧道会相互冲突 — 这是 Tailscale Funnel 自身的限制，而非 cornus 的问题。
- 该 URL 默认可被互联网上的任何人访问；如果不希望如此，请通过 Tailscale ACL 加以限制。

**另请参阅：** [cornus tunnel](/zh/cli/tunnel)、[Helm chart 值](/zh/reference/helm-values)
