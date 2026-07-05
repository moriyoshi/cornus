# CLI 参考

`cornus` 是单个二进制文件，集成了小型 OCI 镜像仓库、基于 BuildKit 的进程内构建引擎和命令式部署引擎。同一个二进制文件既运行服务器，也作为构建、推送、部署和访问工作负载的客户端。

```sh
cornus [global flags] <command> [command flags]
```

命令树由 [kong](https://github.com/alecthomas/kong) 解析。运行 `cornus --help` 或 `cornus <command> --help` 查看内置使用说明。

## 全局 flag

这些 flag 位于根命令，适用于每个子命令。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--data-dir` | `CORNUS_DATA` | 平台数据目录 | 持久化数据目录（镜像仓库 CAS + 构建缓存）。 |
| `--context` | `CORNUS_CONTEXT` | 当前 context | 要从 cornus 客户端配置中使用的连接配置文件（参见 [`cornus config`](/zh/cli/config)）。覆盖配置中的 current-context。 |
| `--config-file` | `CORNUS_CONFIG` | 平台用户配置目录 | cornus 客户端配置文件路径。默认为平台用户配置目录，遵循 `$XDG_CONFIG_HOME`。 |
| `--output` | `CORNUS_OUTPUT` | `auto` | 输出呈现：`auto`、`plain`、`fancy` 或 `json`。参见[输出模式](/zh/guides/output-modes)。 |
| `--context-file` | `CORNUS_CONTEXT_FILE` | 自动发现 | 显式指定项目 context 覆盖文件 (JSON、YAML 或 TOML 格式的 bare Context)。未指定时，Cornus 会向上搜索 `cornus-context.{json,yaml,toml}`。 |
| `--no-context-file` | — | `false` | 禁用项目 context 自动发现。不能与 `--context-file` 一起使用。 |
| `--trust-context-file` | `CORNUS_TRUST_CONTEXT_FILE` | `false` | 允许自动发现的项目 context 文件提供 endpoint、credential 和 TLS 字段。仅在信任工作树时使用。 |
| `--no-color` | — | `false` | 在 fancy 输出中禁用颜色（保留布局）。同样支持 `NO_COLOR` / `CLICOLOR=0`。 |

`--output` 的取值为：

- `auto` - 终端使用 fancy，其他情况使用 plain。
- `plain` - 确定性输出，无颜色。
- `fancy` - 颜色加布局。
- `json` - 机器可读的 NDJSON。

完整行为请参见[输出模式](/zh/guides/output-modes)。

## 命令

| 命令 | 说明 |
| --- | --- |
| [`cornus serve`](/zh/cli/serve) | 运行 cornus 服务器（镜像仓库 + 构建 + 部署）。 |
| [`cornus build`](/zh/cli/build) | 从 context 构建镜像并推送。 |
| [`cornus push`](/zh/cli/push) | 将本地镜像推送到镜像仓库。 |
| [`cornus deploy`](/zh/cli/deploy) | 应用部署 spec。 |
| [`cornus exec`](/zh/cli/exec) | 通过 cornus 服务器在部署中运行命令（docker exec）。 |
| [`cornus port-forward`](/zh/cli/port-forward) | 将本地 TCP 端口转发到部署容器端口。 |
| [`cornus socks5`](/zh/cli/socks5) | 运行本地 SOCKS5 split-tunnel 代理，按名称访问工作负载。 |
| [`cornus tunnel`](/zh/cli/tunnel) | 通过托管 tunnel 将部署端口暴露到互联网。 |
| [`cornus config`](/zh/cli/config) | 管理访问远程 cornus 服务器的连接配置文件（context）。 |
| [`cornus compose`](/zh/cli/compose) | 面向 Compose / devcontainer 项目的 Docker Compose 兼容客户端。 |
| [`cornus web`](/zh/cli/web) | 提供仅限 loopback 的浏览器 UI 和客户端侧 BFF。 |
| [`cornus daemon`](/zh/cli/daemon) | Docker API frontend 和统一后台 agent 控制。 |
| [`cornus hub`](/zh/cli/hub) | 作为 spoke 加入工作负载到工作负载的覆盖网络。 |
| [`cornus token`](/zh/cli/token) | 为启用 bearer auth 的服务器签发 JWT。 |
| [`cornus version` / `cornus health`](/zh/cli/version-health) | 打印 cornus 版本，或探测正在运行的服务器健康端点。 |

## 另请参阅

- [Cornus 是什么](/zh/introduction/what-is-cornus)
- [安装](/zh/introduction/installation)
- [快速开始](/zh/introduction/quick-start)
- [架构](/zh/architecture/)
