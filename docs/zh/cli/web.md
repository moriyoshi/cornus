# cornus web

为 cornus 服务器管理的工作负载和 Compose 项目提供本地浏览器 UI。

## 概述

```sh
cornus web [flags]
```

## 说明

`cornus web` 会启动内嵌 SolidJS 应用和客户端侧 backend-for-frontend (BFF)。UI 可显示工作负载生命周期与详情、Compose 项目及其 `depends_on` 图、客户端本地挂载、隧道与转发、配置文件、流式日志，以及交互式 exec 终端。BFF 还向客户端公开工作负载统计流。

Compose 结构、本地文件源和存活的后台 agent session 不属于服务器扁平化的 workload API，因此 BFF 在客户端运行。它与其他客户端命令一样使用当前选择的连接配置。项目视图使用传给本命令的 Compose 文件；若既未发现也未显式指定文件，服务器工作负载视图仍可使用，但项目视图为空。

UI 没有身份验证，因此只监听 loopback。`--addr` 必须使用 `localhost` 或 loopback IP literal；通配地址和非 loopback 地址会被拒绝。

## 标志

| 标志 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | Loopback 监听地址。端口 `0` 会选择可用端口。 |
| `-H`, `--host` | `CORNUS_HOST` | 配置，然后是 `http://localhost:5000` | cornus 服务器 endpoint。 |
| `-f`, `--file` | — | Compose 自动发现 | Compose 文件，可重复指定。 |
| `--env-file` | — | `.env` 自动发现 | 用于 Compose 变量插值的 env 文件，可重复指定，并替代默认发现。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose 目录名 | 项目名。 |
| `--open` | — | `false` | 监听器启动后在默认浏览器中打开 UI。 |
| `--frontend` | `CORNUS_WEB_FRONTEND` | 内嵌资源 | 独立前端开发服务器 URL。非 BFF 请求会反向代理到该服务器，实际 BFF 保持在同一 origin。 |

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

另请参阅 [`cornus compose`](/zh/cli/compose)、[`cornus daemon`](/zh/cli/daemon)和[连接配置参考](/zh/reference/connection-config)。
