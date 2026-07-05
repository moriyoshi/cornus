# 公网隧道

**公网隧道** (`cornus tunnel`) 通过托管中继将一个工作负载端口暴露到公网，无需集群原生的 Ingress 资源，也无需在网络路径上发布端口。可用它分享进行中的工作、接收 webhook，或在手机上测试。如果需要由真正的 Ingress 资源提供的持久主机名，而不是托管中继，请参阅 [Ingress](/zh/topics/ingress)。

`cornus tunnel <name> <port>` 会为工作负载端口返回一个**公网 https URL**，并持续运行直到 `Ctrl-C`。Cornus **服务器**托管隧道，并通过与 port-forward 相同的字节桥接将每个入站连接转接给工作负载，因此能在任何后端 (Docker host、containerd 或 Kubernetes) 上访问工作负载从未发布的端口。

```sh
cornus tunnel --server http://cornus.example:5000 \
  --authtoken "$NGROK_AUTHTOKEN" web 80
```

隧道凭据由客户端在已认证的请求中注入；服务器事先不会得知它。运维人员也可将 `CORNUS_TUNNEL_AUTHTOKEN` 设置在服务器上作为默认凭据，使调用方无需提供 `--authtoken`。使用 `--proto http` / `--proto tcp` 选择 HTTP 或原始 TCP。完整标志集请参阅 [`cornus tunnel`](/zh/cli/tunnel)。

## 后端

隧道后端通过服务器上的 `CORNUS_TUNNEL_BACKEND` 选择 (默认 `ngrok`)。具体后端可插拔，且仅选中的后端会处于活动状态。

| 后端 | 注入的凭据 | 说明 |
| --- | --- | --- |
| `ngrok` (默认) | ngrok authtoken (`NGROK_AUTHTOKEN`) | 进程内 ngrok agent，不启动子进程 |
| `ssh` | SSH 私钥 (PEM) 或密码 | SSH remote-forward 至可自行托管的隧道服务器 (sish、serveo、pinggy、localhost.run、启用 GatewayPorts 的普通 `sshd`)；复用内置 SSH 栈 |
| `cloudflare` | 无 (匿名) | 通过 `cloudflared` 二进制文件 (`CORNUS_TUNNEL_CLOUDFLARED_BIN`) 使用 Cloudflare quick tunnel |
| `tailscale` | 无 | 通过 `tailscale` 二进制文件使用 Tailscale Funnel；节点在带外加入 tailnet，因此每个节点一个 Funnel |

对于 `ssh` 后端，使用 `CORNUS_TUNNEL_SSH_ADDR` / `CORNUS_TUNNEL_SSH_USER` 配置端点，使用 `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` 或 `CORNUS_TUNNEL_SSH_HOSTKEY` 配置主机密钥验证 (故障闭合；`CORNUS_TUNNEL_SSH_INSECURE=1` 仅供开发使用)。

每个后端的分步设置说明见[隧道指南](/zh/guides/tunnels)。完整的环境变量参考见[服务器环境变量](/zh/reference/server-env-vars)。
