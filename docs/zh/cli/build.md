# cornus build

使用基于 BuildKit 的引擎从 context 构建镜像，并将其推送至镜像仓库。

## 概要

```sh
cornus build -t <ref> [flags] [context]
```

## 说明

`cornus build` 根据 build context 目录（位置参数，默认 `.`）构建 `-t/--tag` 指定名称的镜像。默认情况下，它使用此主机上的进程内构建引擎，并将结果推送到目标镜像仓库。

使用 `--builder`（或选定的、指定服务器的连接 profile）时，构建改为在远程 cornus 服务器运行：本机经 9P/WebSocket 向其流式传输 context、`--build-context` 目录和 secret。参见[构建镜像](/zh/guides/building-images)。

远程构建中，不带 registry 部分的 `-t/--tag`（例如 `app:v1` 或 `team/app:v1`）会被限定到服务器内置镜像仓库——bare tag 指向*默认*镜像仓库，而 Cornus 的默认镜像仓库是自身而非 Docker Hub。`--registry` / `CORNUS_REGISTRY` 覆盖该 host；未设置时，依次默认为服务器声明的 registry host 和 builder endpoint host。已有 registry 的 tag（例如 `registry.example.com/app:v1`）保持不变；纯本地的进程内构建则保留 bare tag 的 Docker 自身规范化方式。

`--build-arg`、`--secret`、`--ssh` 和 `--build-context` 都可重复。`--cache-to` / `--cache-from` 接受 buildx 风格 cache spec；对于 `type=local`，`dest=` / `src=` 值是引擎管理的 key（未指定时从 `--tag` 自动派生），而不是 filesystem path。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-t`, `--tag` | — | 必需 | 目标镜像引用，例如 `localhost:5000/app:v1`。 |
| `context`（位置参数） | — | `.` | Build context 目录。 |
| `-f`, `--file` | — | `Dockerfile` | 相对 context 的 Dockerfile 路径。 |
| `--build-arg` | — | — | Build arg `KEY=VALUE`，可重复。 |
| `--secret` | — | — | Secret mount `id=NAME,src=PATH`（`RUN --mount=type=secret`），可重复。省略 `src` 时默认为 id。 |
| `--ssh` | — | — | SSH agent forwarding：`default` 或 `ID[=SOCKET]`（`RUN --mount=type=ssh`），可重复。缺失 socket 时回退到 `$SSH_AUTH_SOCK`。 |
| `--build-context` | — | — | 命名 build context `NAME=PATH`（`RUN --mount=type=bind,from=NAME`），可重复。 |
| `--builder` | `CORNUS_BUILDER` | — | 远程 cornus build endpoint（`ws://` 或 `http(s)://` base URL）。设置时构建在该端运行，本机经 9P/WebSocket 流式传输 context、build-context 目录和 secret。 |
| `--registry` | `CORNUS_REGISTRY` | 派生 | 远程构建中不含 registry 部分的 `--tag` 所用 registry host。默认是服务器声明 host，否则为 builder endpoint host。 |
| `--rootless` | `CORNUS_ROOTLESS` | `false` | 以 rootless mode（user namespace）运行构建。 |
| `--lazy` | `CORNUS_LAZY_BUILD` | `false` | 通过 9P 按需提供 `--build-context` 目录（lazy build），而不是 eager sync。服务器范围的 `CORNUS_LAZY_BUILD` 也会启用此项。 |
| `--cache-to` | — | — | Cache export backend（buildx syntax），例如 `type=registry,ref=HOST/app:cache[,registry.insecure=true]`。可重复。 |
| `--cache-from` | — | — | Cache import backend（buildx syntax），例如 `type=registry,ref=HOST/app:cache[,registry.insecure=true]`。可重复。 |
| `--no-cache` | — | `false` | 不使用 build cache。 |
| `--no-push` | — | `false` | 仅构建，不推送结果。 |
| `--insecure` | — | `true` | 允许推送至 HTTP（非 TLS）镜像仓库。 |

## 示例

构建并推送本地镜像：

```sh
cornus build -t localhost:5000/app:v1 .
```

使用替代 Dockerfile 和 build arg：

```sh
cornus build -t localhost:5000/app:v1 -f docker/Dockerfile --build-arg VERSION=1.2.3 .
```

传递 secret 并转发 SSH agent：

```sh
cornus build -t localhost:5000/app:v1 \
  --secret id=npmrc,src=$HOME/.npmrc \
  --ssh default .
```

在远程 cornus builder 上运行构建：

```sh
cornus build -t registry.example.com/app:v1 --builder wss://build.example.com .
```

导出并导入 registry cache：

```sh
cornus build -t localhost:5000/app:v1 \
  --cache-to type=registry,ref=localhost:5000/app:cache \
  --cache-from type=registry,ref=localhost:5000/app:cache .
```

## 另请参阅

- [构建镜像](/zh/guides/building-images)
- [`cornus push`](/zh/cli/push)
- [快速开始](/zh/introduction/quick-start)
