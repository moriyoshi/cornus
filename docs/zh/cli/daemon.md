# cornus daemon

长期运行的辅助 daemon: 客户端侧 Docker Engine API proxy、客户端侧 background agent 的状态 / 停止控制、主机环境 preflight，以及面向 pod 的 sidecar。

## 概要

```sh
cornus daemon <subcommand> [flags]
```

## 说明

`cornus daemon` 将辅助进程分组。面向最终用户的 subcommand 是 Docker Engine API proxy (`docker`) 、background-agent control (`status`、`stop`) 和主机环境检查 (`preflight`) 。其余 subcommand 是烘焙到生成 pod spec 中的 pod sidecar，不应手工运行。cornus 服务器本身为 [`cornus serve`](/zh/cli/serve)。

## cornus daemon docker

运行本地 daemon，在 unix socket 上提供 Docker Engine REST API 的一个子集，并将 container operation 转换为针对远程 cornus 服务器的 deploy。将 `DOCKER_HOST` 指向其 socket 后，标准 `docker` 就会在远程 cornus 上运行工作负载，同时调用方本地 bind-mount 目录经 9P 流式传输。

```sh
cornus daemon docker [flags]
```

frontend 由单个客户端侧 background agent 托管 (按需启动) 。前台运行会保持至 `Ctrl-C`，随后注销 frontend；`-d`/`--daemon` 则注册并返回，由 agent 继续托管。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--host` | `CORNUS_HOST` | `http://localhost:5000` | 远程 cornus 服务器 URL。依次回退到选定连接 profile 和默认值。 |
| `--socket` | `CORNUS_DOCKER_SOCK` | `$XDG_RUNTIME_DIR/cornus-docker.sock` | 要监听的 Unix socket。 |
| `-d`, `--daemon` | — | `false` | 在后台作为 daemon 运行 (默认: 在前台运行) 。 |
| `--no-forward-ports` | — | `false` | 不在本地 listener 上发布 container port (`docker -p`) 。 |

可借此将标准 `docker` / `docker compose` 指向远程 cornus 服务器；内置 Compose client 请使用 [`cornus compose`](/zh/cli/compose)，更完整的远程模型请参见[使用远程集群](/zh/guides/remote-clusters)。

::: warning 删除命名卷
proxy 通过所选部署后端预配命名卷，但 `docker volume rm` 只会从该 proxy 进程的内存中删除名称，并不会删除后端存储。`docker volume prune` 和 `docker system prune` 的卷处理阶段同样不会报告回收了任何内容，也会保留后端存储。必须删除数据时，请使用 Cornus 能识别后端的卷生命周期。
:::

## cornus daemon status

显示正在运行的 cornus client agent inventory (server、project、docker frontend 和 conduit banner) 。没有 agent 运行时会报告该状态。

```sh
cornus daemon status
```

## cornus daemon stop

停止正在运行的 cornus client agent。

```sh
cornus daemon stop
```

## cornus daemon preflight

检查此进程是否真的能够驱动所配置部署后端的容器运行时。

```sh
cornus daemon preflight
```

它执行的检测和检查与 [`cornus serve`](/zh/cli/serve)启动时完全相同，因此回答的是真实服务端的情况而非近似；并且在服务端会拒绝启动的配置下以**非零**状态退出 — 这样镜像冒烟测试或 CI 作业就可以据此设置门禁。

它的意义在于*先于*确定部署运行: 在你即将用来提供服务的容器镜像内部，用相同的挂载和相同的环境，趁改动绑定还很容易的时候执行。

```
cornus runs in a container (a1b2c3d4e5f6) on a docker host; translating its paths for the runtime
  [ok  ] data-dir-host-visible: data dir /var/lib/cornus is /srv/cornus on the host
  [warn] client-local-mounts: client-local mounts unavailable: ...
           remedy: run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded
```

每一行是一项检查、它的判定 (`ok`、`warn`、`fail`) 、检测到的情况，以及对需要处理的项给出的补救办法。`warn` 表示某项能力不可用，但当有东西请求它时它会自行报告缺失；`fail` 表示部署会静默地出错。`--output json` 以单个对象输出相同的结果。

对于直接运行在主机上、完全不需要这些的服务端，输出只有说明这一点的一行。参见[在容器中运行服务端](/zh/guides/server-in-a-container)。

## Pod sidecar 和内部 subcommand

这些 subcommand 不面向最终用户；因为其拼写会被写入生成 pod spec，或由 client 启动:

- `caretaker`——运行配置 role (9P mount、hub 等) 直至 teardown 的 pod sidecar。
- `caretaker-check`——sidecar readiness probe；所有 caretaker role 都存活时以 0 退出。
- `net-redirect`——将 app egress 通过 iptables 重定向到 caretaker proxy 的 init container。

隐藏的 `mounts` 和 `agent` subcommand 属于客户端侧 background agent 内部 (由 `cornus compose up -d` 等 client 启动，不应手动运行) 。

## 示例

在前台提供 Docker API proxy 并导出 `DOCKER_HOST`:

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

在自定义 socket 上分离运行 proxy:

```sh
cornus daemon docker -d --socket /run/cornus-docker.sock
```

检查并停止 background agent:

```sh
cornus daemon status
cornus daemon stop
```
