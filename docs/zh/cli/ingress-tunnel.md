# cornus ingress-tunnel

通过一个公共 URL 暴露整个项目的**入口 (ingress)**，而不是单个工作负载端口。

## 概要

```sh
cornus ingress-tunnel [flags] <deployment>
cornus ingress-tunnel [flags] --project <project>
```

## 说明

[`cornus tunnel`](/zh/cli/tunnel)暴露一个工作负载的一个端口。`cornus ingress-tunnel` 暴露的是**入口**本身: 服务通过 [`x-cornus-ingress`](/zh/guides/ingress)声明的每个主机名和路径，都可以通过单个 URL 到达，路由由 cornus 代为完成。

对于多服务项目而言，这个差别至关重要。使用 `cornus tunnel` 时，一个包含三个服务的 Compose 项目需要三条隧道和三个互不相关的 URL；使用 `cornus ingress-tunnel` 只需要一个，而且请求会像在生产环境中一样，按主机和路径抵达正确的服务。

隧道实际置于前端的对象取决于部署后端:

| 后端 | 置于前端的对象 |
| --- | --- |
| `kubernetes` (存在可发现的入口控制器时) | **真实的集群控制器**，因此适用集群自身的路由规则和 TLS 证书 |
| 其他所有后端，以及没有控制器的集群 | **服务器自身的入口路由**，提供同样的已声明主机和路径 |

目标部署必须已经声明了入口。如果没有，命令会明确报错，而不是发布一个无法应答的 URL。

::: warning 仅用于开发和预览
置于**服务器自身**入口路由前端的隧道，等于把一个开发用设施放到了公共地址上: 它不终止自己的 TLS，不做速率限制或访问控制，并且其错误页面会显示未能到达的内部工作负载名称和容器端口。这对于分享进行中的工作或接收 Webhook 是合适的，但不适合任何长期用途。

置于**真实集群入口控制器**前端的隧道则不存在上述任何问题——集群已经强制执行的路由、TLS 和策略会照常生效。命令输出的 `fronting:` 一行会告诉你当前属于哪一种。
:::

凭据处理与[`cornus tunnel`](/zh/cli/tunnel)完全一致: secret 被注入到服务器已通过认证的端点，`--authtoken-file` 和 `CORNUS_TUNNEL_AUTHTOKEN` 可使其不出现在 argv 和 shell 历史中；当服务器配置了默认凭据时可以完全省略。隧道会保持到 `Ctrl-C` 为止。

## Host 处理

隧道提供方会发放属于它自己的主机名 `abc123.ngrok.app`，而你的入口是以别的名字声明的，例如 `web.myapp.example.com`。`--host-mode` 控制的正是如何调和这两者。

| 模式 | 到达应用的内容 | 适用场景 |
| --- | --- | --- |
| `auto` (默认) | 向提供方请求入口主机名；无法获得时回退为 `alias` | 几乎总是选它 |
| `alias` | **隧道**主机名，路由通过它解析 | 应用根据收到的 `Host` 构造 URL 时 |
| `passthrough` | 原样不动 | 隧道主机名本身就是入口主机，或使用 raw TCP 隧道时 |
| `rewrite` | **入口**主机名 | 应用依赖其配置的主机名时 |

默认结果是 `alias`，这是有意为之。应用看到的是浏览器实际所在的主机名，因此它的重定向、`Domain=` Cookie 和 CORS 源都指向访问者能够到达的位置。`rewrite` 则相反: 应用看到自己配置的主机名，它生成的任何绝对 URL 都会指向访问者无法解析的名字。只有当应用拒绝为不认识的主机名提供服务时，才使用 `rewrite`。

在真实集群入口控制器前端无法使用 `rewrite`: 那条隧道是 raw 字节流，不存在可供改写的 HTTP 层。命令会明确告知，而不是默默忽略该 flag。

### 获得你真正声明的主机名

某些后端可以按你指定的主机名发布隧道，这会让 `auto` 解析为 `passthrough`——请求完全不做任何调整，TLS 也可以做到端到端。`ngrok` 通过账户下的保留域名或自定义域名支持这一点；`ssh` 后端则在中继按请求的 bind 主机路由时支持，例如 [sish](https://github.com/antoniomika/sish)。Cloudflare quick tunnel 和 Tailscale Funnel 不支持，因此在那里 `auto` 会回退为 `alias`。

## Flag

| Flag | 说明 |
| --- | --- |
| `--project <name>` | 通过一个 URL 暴露某个 Compose 项目的全部部署。与 deployment 参数互斥。 |
| `--host-mode <mode>` | `auto` (默认)、`passthrough`、`alias` 或 `rewrite`。参见上文。 |
| `--host <hostname>` | 当作用域提供多个主机时，指定隧道置于前端的已声明入口主机名。 |
| `--proto <http\|tcp>` | `http` (默认) 或 `tcp`。`tcp` 隧道是 raw 字节流，客户端的 TLS 和 `Host` 会原样抵达入口——这是获得端到端 TLS 的唯一方式，并且**需要真实的集群入口控制器**来终止该 TLS。自行路由入口的服务器只讲纯文本 HTTP，因此会拒绝 `tcp`，而不是发布一个在 https 下必然失败的 URL。 |
| `--authtoken-file <path>` | 从文件读取隧道凭据，使其不出现在 argv 和 shell 历史中。 |
| `--authtoken <token>` | 直接给出凭据。可通过 `ps` 看到，且常被写入 shell 历史；建议改用 `--authtoken-file`。 |
| `--forward-agent` | 为 `ssh` 后端把本地 `ssh-agent` 转发给服务器。与 `ssh -A` 一样，只对你信任的服务器使用。 |
| `--server <url>` | 远程 cornus 服务器 URL。回退到所选连接配置文件。 |

## 示例

发布一个 Compose 项目——所有服务，一个 URL:

```sh
cornus ingress-tunnel --project myapp
```

```
Ingress tunnel for project/myapp ready at https://abc123.ngrok.app
  serving: web.myapp.example.com, api.myapp.example.com
  fronting: the cluster ingress controller
  host: passed through untouched
```

发布单个部署的入口，并从文件读取凭据:

```sh
cornus ingress-tunnel --authtoken-file ~/.config/cornus/ngrok-token web
```

让应用看到它自己配置的主机名:

```sh
cornus ingress-tunnel --project myapp --host-mode rewrite --host web.myapp.example.com
```

## 在部署时自动发布

如果希望每次启动项目时都发布，可以在 Compose 中声明，而不必运行该命令:

```yaml
services:
  web:
    image: myapp:latest
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.myapp.example.com
      tunnel: true
```

此后 `cornus compose up` 会发布该项目的入口并打印 URL，并在会话结束时拆除隧道。凭据仍然来自客户端 (配置文件中的 `authtoken-file`，或 `CORNUS_TUNNEL_AUTHTOKEN`)，绝不会来自会被提交到仓库的 compose 文件。

对象形式接受与 flag 相同的选项:

```yaml
    x-cornus-ingress:
      tunnel:
        host_mode: rewrite
        host: web.myapp.example.com
```

## 参见

- [`cornus tunnel`](/zh/cli/tunnel) — 暴露单个工作负载端口
- [入口指南](/zh/guides/ingress) — 声明主机和路径
- [隧道指南](/zh/guides/tunnels) — 各后端的设置
