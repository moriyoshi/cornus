# cornus web

为 cornus 服务器管理的工作负载和 Compose 项目提供本地浏览器 UI。

## 概述

```sh
cornus web [flags]
```

## 说明

`cornus web` 会启动内嵌 SolidJS 应用和客户端侧 backend-for-frontend (BFF)。UI 可显示工作负载生命周期与详情、Compose 项目及其 `depends_on` 图、客户端本地挂载、隧道与转发、请求两侧的 ingress 设置、配置文件、流式日志、并排放置文件浏览器与交互式 exec 终端的工作区，以及基于服务器内置可观测性存储的指标仪表板。它还共同托管供 agent 客户端使用的 MCP 服务器。

BFF 在客户端运行，并与其他客户端命令一样使用当前选择的连接配置。项目视图使用传给本命令的 Compose 文件；若既未发现也未显式指定文件，服务器工作负载视图仍可使用，但项目视图为空。

UI 没有身份验证。在默认模式下，它只监听 loopback: `--addr` 必须使用 `localhost` 或 loopback IP literal；除非传入 [`--allow-non-loopback`](/zh/guides/web-ui#绑定到主机之外)，否则通配地址和非 loopback 地址会被拒绝。使用 [`--publish-in-conduit`](/zh/guides/web-ui#ui-和工作负载共用一个浏览器-proxy-设置) 时，它完全不绑定 listener，只能通过 SOCKS5 conduit 访问；该 conduit 本身位于 loopback，因此无论哪种方式，无身份验证边界都保持不变。

各个界面的作用、文件浏览器与工作区的行为、指标仪表板、MCP 工具集，以及下列标志背后的具体做法，请参见[浏览器 UI](/zh/guides/web-ui)。

## 标志

| 标志 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | Loopback 监听地址。端口 `0` 会选择可用端口。与 `--publish-in-conduit` 互斥。 |
| `--allow-non-loopback` | — | `false` | 允许将 `--addr` 绑定到通配地址或非 loopback 地址。请参见[绑定到主机之外](/zh/guides/web-ui#绑定到主机之外)。 |
| `--allow-host` | — | loopback 名称 | 除 loopback 写法之外还要响应的 Host header 值，可重复指定。 |
| `-H`, `--host` | `CORNUS_HOST` | 配置，然后是 `http://localhost:5000` | cornus 服务器 endpoint。 |
| `-f`, `--file` | — | Compose 自动发现 | Compose 文件，可重复指定。 |
| `--env-file` | — | `.env` 自动发现 | 用于 Compose 变量插值的 env 文件，可重复指定，并替代默认发现。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose 目录名 | 项目名。 |
| `--open` | — | `false` | 监听器启动后在默认浏览器中打开 UI。 |
| `--local-root` | — | 项目 + bind mount 源 | 文件浏览器可以浏览的额外目录，格式为 `[LABEL=]DIR[:ro]`，可重复指定。请参见[浏览项目未提及的目录](/zh/guides/web-ui#浏览项目未提及的目录)。 |
| `--frontend` | `CORNUS_WEB_FRONTEND` | 内嵌资源 | 独立前端开发服务器 URL。非 BFF 请求会反向代理到该服务器，实际 BFF 保持在同一 origin。 |
| `--mcp` / `--no-mcp` | — | `true` | 在 `/.cornus/mcp` 共同托管供 agent 客户端使用的 MCP (Model Context Protocol) 服务器。`--no-mcp` 会禁用它。请参见[面向 agent 客户端的 MCP 端点](/zh/guides/web-ui#面向-agent-客户端的-mcp-端点)。 |
| `--mcp-stdio` | — | `false` | 只通过 stdin/stdout 提供 MCP 服务器而不绑定 HTTP listener，供启动命令的 agent 客户端使用。不绑定任何端口。与 `--publish-in-conduit` 互斥。 |
| `--publish-in-conduit` | — | `false` | 在 background agent 内托管 UI 并将其发布到共享 SOCKS5 conduit，而不是绑定本地端口。请参见 [UI 和工作负载共用一个浏览器 proxy 设置](/zh/guides/web-ui#ui-和工作负载共用一个浏览器-proxy-设置)。 |
| `--publish-name` | — | 所加入 conduit 的 suffix apex (例如 `cornus.internal`) | 在 conduit 中发布 UI 所用的主机名。隐含 `--publish-in-conduit`。 |
| `--publish-port` | — | `80` | 发布名称响应的 conduit 端口。 |
| `--conduit` | `CORNUS_CONDUIT` | 加入已有的 conduit | `--publish-in-conduit` 使用的 SOCKS5 conduit 选择器 (bare `socks5`，或 `socks5://host:port[?suffix=SUFFIX]`) 。指定地址或 suffix 会**固定**这些设置。 |

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

把项目未提及的目录交给文件浏览器。

```sh
cornus web --local-root ~/scratch --local-root notes=~/wiki:ro
```

只通过 stdio 提供 MCP 端点，供启动命令的 agent 客户端使用。

```sh
cornus web --mcp-stdio -f compose.yaml
```

在保持实际 BFF 位于同一 origin 的前提下，单独运行 Vite 以使用热重载。

```sh
cornus web --frontend http://localhost:5173
```

把 UI 发布到 SOCKS5 conduit，让一个浏览器 proxy 设置同时访问 UI 和工作负载。

```sh
cornus config set-context --conduit-mode socks5   # 工作负载 session 也使用 socks5
cornus socks5 &                                    # 浏览器指向的 proxy
cornus web --publish-in-conduit                    # UI 位于 http://cornus.internal/
```

另请参见[浏览器 UI](/zh/guides/web-ui)、[`cornus compose`](/zh/cli/compose)、[`cornus daemon`](/zh/cli/daemon)和[连接配置参考](/zh/reference/connection-config)。
