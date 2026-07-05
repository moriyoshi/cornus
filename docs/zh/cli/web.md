# cornus web

为 cornus 服务器管理的工作负载和 Compose 项目提供本地浏览器 UI。

## 概述

```sh
cornus web [flags]
```

## 说明

`cornus web` 会启动内嵌 SolidJS 应用和客户端侧 backend-for-frontend (BFF)。UI 可显示工作负载生命周期与详情、Compose 项目及其 `depends_on` 图、客户端本地挂载、隧道与转发、配置文件、流式日志，以及交互式 exec 终端。BFF 还向客户端公开工作负载统计流。

Compose 结构、本地文件源和存活的后台 agent session 不属于服务器扁平化的 workload API，因此 BFF 在客户端运行。它与其他客户端命令一样使用当前选择的连接配置。项目视图使用传给本命令的 Compose 文件；若既未发现也未显式指定文件，服务器工作负载视图仍可使用，但项目视图为空。

UI 没有身份验证。在默认模式下，它只监听 loopback: `--addr` 必须使用 `localhost` 或 loopback IP literal；通配地址和非 loopback 地址会被拒绝。使用 `--publish-in-conduit` 时，它完全不绑定 listener，只能通过 SOCKS5 conduit (见下文) 访问；该 conduit 本身位于 loopback，因此无论哪种方式，无身份验证边界都保持不变。

## 标志

| 标志 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | Loopback 监听地址。端口 `0` 会选择可用端口。与 `--publish-in-conduit` 互斥。 |
| `-H`, `--host` | `CORNUS_HOST` | 配置，然后是 `http://localhost:5000` | cornus 服务器 endpoint。 |
| `-f`, `--file` | — | Compose 自动发现 | Compose 文件，可重复指定。 |
| `--env-file` | — | `.env` 自动发现 | 用于 Compose 变量插值的 env 文件，可重复指定，并替代默认发现。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose 目录名 | 项目名。 |
| `--open` | — | `false` | 监听器启动后在默认浏览器中打开 UI。 |
| `--frontend` | `CORNUS_WEB_FRONTEND` | 内嵌资源 | 独立前端开发服务器 URL。非 BFF 请求会反向代理到该服务器，实际 BFF 保持在同一 origin。 |
| `--mcp` / `--no-mcp` | — | `true` | 在 `/.cornus/mcp` 共同托管供 agent 客户端使用的 MCP (Model Context Protocol) 服务器。`--no-mcp` 会禁用它。 |
| `--mcp-stdio` | — | `false` | 只通过 stdin/stdout 提供 MCP 服务器而不绑定 HTTP listener，供启动命令的 agent 客户端使用。不绑定任何端口。与 `--publish-in-conduit` 互斥。 |
| `--publish-in-conduit` | — | `false` | 在 background agent 内托管 UI 并将其发布到共享 SOCKS5 conduit，而不是绑定本地端口。 |
| `--publish-name` | — | suffix apex (例如 `cornus.internal`) | 在 conduit 中发布 UI 所用的主机名。隐含 `--publish-in-conduit`。 |
| `--publish-port` | — | `80` | 发布名称响应的 conduit 端口。 |
| `--conduit` | `CORNUS_CONDUIT` | profile | `--publish-in-conduit` 使用的 SOCKS5 conduit 选择器 (bare `socks5`，或 `socks5://host:port[?suffix=SUFFIX]`) 。 |

## UI 和工作负载共用一个浏览器 proxy 设置

当你通过 SOCKS5 conduit 访问 cornus 服务器的工作负载时——浏览器 proxy 设置为 `cornus socks5` (或 `cornus config set-context --conduit-mode socks5`) 并解析 `*.cornus.internal` 名称——`cornus web` UI 是单独的 `http://127.0.0.1:<port>`，需要自己的浏览器设置。`--publish-in-conduit` 消除了这种分离:

```sh
cornus web --publish-in-conduit
```

这会把 UI backend 交给 background agent；agent 在进程内 listener 上提供该 backend，并在**共享** conduit 中以 `cornus.internal` (service-host suffix apex) 发布。随后，UI 通过访问工作负载的同一个 proxy 在 `http://cornus.internal/` 响应——一个浏览器 proxy 设置即可访问两者。它不绑定本地端口，因此不会新增暴露；UI 的可达范围与 proxy 完全相同。

命令保持在前台运行，并在退出 (或被终止) 时撤销该名称。如果 agent 重启，它会自动重新发布。

注意:

- 浏览器必须通过 proxy 执行 **remote** DNS (SOCKS5h)，使 `cornus.internal` 由 proxy 而不是本地解析——这与现有 `*.cornus.internal` 工作负载名称的要求相同。
- 发布名称只提供 `http://`，不提供 `https://`。
- 工作负载 session 也应使用 **socks5** conduit。如果它们以默认 port-forward 模式运行，UI 仍可解析，工作负载也仍可通过完整部署名称解析，但 Compose 短名称 (例如以 `demo-web` 部署的服务对应的 `web.cornus.internal`) 无法解析——这些 alias 只由 socks5 模式的工作负载 session 注册。
- 此处传入的 conduit 设置必须与工作负载 session 使用的设置一致，否则两个 proxy 会在同一个绑定地址上冲突。

## 面向 agent 客户端的 MCP 端点

同一服务器还在 `/.cornus/mcp` 共同托管 [MCP](https://modelcontextprotocol.io) (Model Context Protocol) 服务器，使 agent 客户端——Zed 的 Agent panel、Claude Desktop 等——可以驱动 UI 所公开的同一组客户端侧能力: 列出并操作工作负载、读取依赖图和挂载、tail 日志、运行一次性命令，以及读取或写入 allow-list 中的 Compose/env/配置文件。它默认启用；传入 `--no-mcp` 可将其禁用。

MCP 工具只是 UI BFF 所用相同逻辑的轻量 adapter，因此两个界面不会产生偏差。流式传输仍仅限 UI: 交互式终端和实时日志/统计流不适合 MCP 的请求/响应模型，因此 MCP 提供有界的 `logs_tail` (最后 N 行) 和一次性的 `exec_run` (捕获 stdout/stderr/退出状态) 。

agent 还可以获取服务器的[飞行记录](/zh/cli/activity)，它回答的是事后“出了什么问题”，而不是“现在是什么状态”: `activity_read` 工具具有与 CLI 相同的 `since`/`kind`/`unfinished` 筛选器，另有 `cornus://activity/unfinished` **资源**，即服务器及其 caretaker 已开始但从未完成的事项集合。资源形式最为实用: 客户端可以像文件一样附加它，因此当 agent 被问到行为异常的部署时，它一开始就知道上一台服务器在执行中途停止。两者都会随记录携带 `liveInstance`；没有它，当前服务进程自身未结束的生命周期会被读成崩溃。跟随 (`cornus activity --follow`) 与日志流出于同一原因仍仅限 CLI。

MCP 完整继承 UI 的威胁模型: 相同的 loopback / 无身份验证边界和相同的 DNS rebinding Host 防护。使用 `--publish-in-conduit` 时，MCP 端点与 UI 发布在同一个 SOCKS5 conduit 中，这会像 UI 已经公开那样向 conduit 用户公开 `file_write` 和 `exec_run`——如果想缩小此处的影响范围，请使用 `--no-mcp`。

大多数 MCP 客户端通过 stdio 启动命令，而不是连接 HTTP URL。对于这类客户端，运行 `cornus web --mcp-stdio`；它通过 stdin/stdout 提供完全相同的工具界面，并且不绑定 HTTP listener。它复用与浏览器 UI 相同的连接配置和 Compose 标志；诊断信息发送到 stderr，因此不会破坏 stdout 上的 JSON-RPC 流。例如，在客户端中注册为:

```json
{
  "command": "cornus",
  "args": ["web", "--mcp-stdio", "-f", "compose.yaml"]
}
```

## 文件编辑和应用

编辑器仅限于解析后的 Compose 文件、env 文件和客户端配置文件。任意路径和路径穿越写法都会被拒绝。应用项目时会执行等效的 `cornus compose ... up -d`，因此标准 Compose 收敛和后台 agent 行为仍是正本。

## 示例

使用当前连接配置和自动发现的 Compose 文件，在自动选择的 loopback 端口启动。

```sh
cornus web --open
```

显式选择远程服务器和项目。

```sh
cornus web --host https://cornus.example.com:5000 \
  -f compose.yaml -p demo --addr 127.0.0.1:8080
```

单独运行 Vite 并使用热重载，同时让实际 BFF 保持在同一 origin。

```sh
cornus web --frontend http://localhost:5173
```

将 UI 发布到 SOCKS5 conduit，使一个浏览器 proxy 设置同时访问 UI 和工作负载:

```sh
cornus config set-context --conduit-mode socks5   # 工作负载 session 也使用 socks5
cornus socks5 &                                    # 浏览器指向的 proxy
cornus web --publish-in-conduit                    # UI 位于 http://cornus.internal/
```

另请参阅 [`cornus compose`](/zh/cli/compose)、[`cornus daemon`](/zh/cli/daemon)和[连接配置参考](/zh/reference/connection-config)。
