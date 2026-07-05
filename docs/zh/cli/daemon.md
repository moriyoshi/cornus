# cornus daemon

长期运行的辅助 daemon：客户端侧 Docker Engine API proxy、客户端侧 background agent 的状态 / 停止控制，以及面向 pod 的 sidecar。

## 概要

```sh
cornus daemon <subcommand> [flags]
```

## 说明

`cornus daemon` 将辅助进程分组。面向最终用户的 subcommand 是 Docker Engine API proxy（`docker`）和 background-agent control（`status`、`stop`）。其余 subcommand 是烘焙到生成 pod spec 中的 pod sidecar，不应手工运行。cornus 服务器本身为 [`cornus serve`](/zh/cli/serve)。

## cornus daemon docker

运行本地 daemon，在 unix socket 上提供 Docker Engine REST API 的一个子集，并将 container operation 转换为针对远程 cornus 服务器的 deploy。将 `DOCKER_HOST` 指向其 socket 后，标准 `docker` 就会在远程 cornus 上运行工作负载，同时调用方本地 bind-mount 目录经 9P 流式传输。

```sh
cornus daemon docker [flags]
```

frontend 由单个客户端侧 background agent 托管（按需启动）。前台运行会保持至 `Ctrl-C`，随后注销 frontend；`-d`/`--daemon` 则注册并返回，由 agent 继续托管。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--host` | `CORNUS_HOST` | `http://localhost:5000` | 远程 cornus 服务器 URL。依次回退到选定连接 profile 和默认值。 |
| `--socket` | `CORNUS_DOCKER_SOCK` | `$XDG_RUNTIME_DIR/cornus-docker.sock` | 要监听的 Unix socket。 |
| `-d`, `--daemon` | — | `false` | 在后台作为 daemon 运行（默认：在前台运行）。 |
| `--no-forward-ports` | — | `false` | 不在本地 listener 上发布 container port（`docker -p`）。 |

可借此将标准 `docker` / `docker compose` 指向远程 cornus 服务器；内置 Compose client 请使用 [`cornus compose`](/zh/cli/compose)，更完整的远程模型请参见[远程工作流](/zh/topics/remote-workflows)。

## cornus daemon status

显示正在运行的 cornus client agent inventory（server、project、docker frontend 和 conduit banner）。没有 agent 运行时会报告该状态。

```sh
cornus daemon status
```

## cornus daemon stop

停止正在运行的 cornus client agent。

```sh
cornus daemon stop
```

## Pod sidecar 和内部 subcommand

这些 subcommand 不面向最终用户；因为其拼写会被写入生成 pod spec，或由 client 启动：

- `caretaker`——运行配置 role（9P mount、hub 等）直至 teardown 的 pod sidecar。
- `caretaker-check`——sidecar readiness probe；所有 caretaker role 都存活时以 0 退出。
- `net-redirect`——将 app egress 通过 iptables 重定向到 caretaker proxy 的 init container。

隐藏的 `mounts` 和 `agent` subcommand 属于客户端侧 background agent 内部（由 `cornus compose up -d` 等 client 启动，不应手动运行）。

## 示例

在前台提供 Docker API proxy 并导出 `DOCKER_HOST`：

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

在自定义 socket 上分离运行 proxy：

```sh
cornus daemon docker -d --socket /run/cornus-docker.sock
```

检查并停止 background agent：

```sh
cornus daemon status
cornus daemon stop
```
