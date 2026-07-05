# cornus tunnel

通过服务器托管的 tunnel 将部署端口暴露到公网，使运行中的应用可从任何位置访问。

## 概要

```sh
cornus tunnel [flags] <name> <port>
```

## 说明

`cornus tunnel` 请求 cornus 服务器为部署端口托管公共 tunnel，适合共享进行中的工作或接收 webhook。服务器在进程内托管 tunnel 并将其桥接到工作负载，因此和 [`cornus port-forward`](/zh/cli/port-forward) 一样，tunnel 可在任意后端访问工作负载从未发布的端口；不同之处在于它提供 public URL 而非本地 listener。

Tunnel 后端在**服务器**上由 `CORNUS_TUNNEL_BACKEND` 选择（默认 `ngrok`）；其他后端为 `ssh`（SSH reverse-tunneling）、`cloudflare`（Cloudflare Tunnel）和 `tailscale`（Tailscale Funnel）。各后端所需条件参见[后端表格](/zh/guides/tunnels#后端)，分步设置说明参见[隧道指南](/zh/guides/tunnels)。

每条 tunnel 的 credential 会注入服务器已认证的端点（服务器无法预先知道它）。credential 的具体形式取决于后端：`ngrok` 使用 ngrok authtoken，`ssh` 使用 SSH private key（PEM）或 password，`cloudflare` / `tailscale` 无需任何内容（它们匿名或在带外加入）。请通过 `CORNUS_TUNNEL_AUTHTOKEN` 环境变量（会自动读入 `--authtoken`；`NGROK_AUTHTOKEN` 作为旧版别名也仍然可用）或 `--authtoken-file` 提供——两者都不会让 secret 出现在 argv 或 shell 历史中，这一点与直接使用 `--authtoken` 不同。最好的方式是在**服务器**已有默认 credential 时完全省略（同样通过 `CORNUS_TUNNEL_AUTHTOKEN` 设置，但这是服务器自身的环境变量——同一个变量名，在不同进程中读取时含义不同）。命令打印 public URL，并在 `Ctrl-C`（或 `SIGTERM`）前保持 tunnel；此操作会将其销毁。

端口必须位于 `1..65535`。连接由 `--server` 解析，否则使用选定连接配置文件（参见 [`cornus config`](/zh/cli/config)）。更完整的说明参见[隧道](/zh/guides/tunnels)。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | 远程 cornus 服务器 URL（`http(s)://` 或 `ws(s)://`）。回退到选定连接配置文件。 |
| `--authtoken` | `CORNUS_TUNNEL_AUTHTOKEN`（`NGROK_AUTHTOKEN` 也可作为旧版别名使用） | — | Tunnel 后端 credential（例如 ngrok authtoken）。会注入服务器；仅当服务器有默认 credential 时才可省略。会让 secret 出现在 argv 和 shell 历史中——优先使用环境变量（会自动读入此 flag，不出现在 argv 中）或 `--authtoken-file`。 |
| `--authtoken-file` | — | — | 从该文件读取 credential，而非使用 `--authtoken`，从而让 secret 不出现在 argv 或 shell 历史中。与 `--authtoken` 互斥。 |
| `--proto` | — | `http` | 暴露的协议：`http` 或 `tcp`。 |

位置参数：

- `<name>`——要暴露的部署名称（必需）。
- `<port>`——通过 tunnel 暴露的容器端口（必需）。

## 示例

通过 HTTP 暴露 `web` 部署的容器端口 80，authtoken 从环境变量读取，命令行中无需 `--authtoken`：

```sh
export CORNUS_TUNNEL_AUTHTOKEN=2ab3...
cornus tunnel web 80
```

暴露原始 TCP 端口，credential 从文件而非 argv 中读取：

```sh
cornus tunnel --proto tcp --authtoken-file ~/.config/cornus/ngrok-token db 5432
```

使用服务器的默认 credential（客户端完全不提供任何 credential）：

```sh
cornus tunnel web 8080
```
