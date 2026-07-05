# 构建镜像

以下是面向任务的操作方法，适用于本地或 remote cornus server 上运行的进程内 BuildKit engine。所有 flag 及其行为见 [cornus build](/zh/cli/build)。

## 构建 Dockerfile 并 push 到内置 registry

从 context directory 构建 `-t` 指定名称的 image，并 push 至 target registry。

```sh
cornus build -t localhost:5000/app:latest .
```

- Positional context 默认为 `.`；non-default Dockerfile path（相对 context）使用 `-f docker/Dockerfile`。
- `--insecure`（默认 `true`）允许 push 至 `localhost:5000` 等 plain-HTTP registry。

**另请参阅：**[cornus build](/zh/cli/build)、[镜像仓库](/zh/guides/registry)

## 只构建不 push（--no-push）

仅构建 image，不在 registry 中留下内容。

```sh
cornus build -t localhost:5000/app:latest --no-push .
```

- 适合验证 Dockerfile，或在不发布 tag 时预热 cache。

**另请参阅：**[cornus build](/zh/cli/build)

## 传递 build arg（--build-arg）

设置 Dockerfile 中 `ARG` 消费的 build-time variable。

```sh
cornus build -t localhost:5000/app:latest \
  --build-arg VERSION=1.2.3 \
  --build-arg COMMIT=$(git rev-parse --short HEAD) .
```

- `--build-arg` 可重复，每个 flag 一个 `KEY=VALUE`。

**另请参阅：**[cornus build](/zh/cli/build)

## 使用 build cache mount（RUN --mount=type=cache）

跨 build 持久保存 package 或 compiler cache directory。这是 Dockerfile feature，无需 CLI flag。

```dockerfile
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl
```

```sh
cornus build -t localhost:5000/app:latest .
```

- Cache 位于 build engine 中，在同一 host 或 remote builder 上的 build 间存活。

**另请参阅：**[cornus build](/zh/cli/build)

## 向 build 传递 secret（--secret id=NAME,src=PATH）

将 secret file mount 到 `RUN --mount=type=secret` step，而不将其烘焙进 image。

```sh
cornus build -t localhost:5000/app:latest \
  --secret id=npmrc,src=$HOME/.npmrc .
```

```dockerfile
RUN --mount=type=secret,id=npmrc,target=/root/.npmrc npm ci
```

- `--secret` 可重复。省略 `src` 时默认为 id。
- Remote build（`--builder`）中，secret 经 9P/WebSocket 流向 server，绝不会落入 layer。

**另请参阅：**[cornus build](/zh/cli/build)、[凭据](/zh/guides/credentials)

## 向 build 转发 SSH agent（--ssh）

使 `RUN --mount=type=ssh` step 可访问本地 ssh-agent，例如 clone private repo。

```sh
cornus build -t localhost:5000/app:latest --ssh default .
```

```dockerfile
RUN --mount=type=ssh git clone git@github.com:me/private.git
```

- `--ssh` 可重复，接受 `default` 或 `ID[=SOCKET]`；缺失 socket 时回退至 `$SSH_AUTH_SOCK`。

**另请参阅：**[cornus build](/zh/cli/build)

## 使用命名 build context（--build-context NAME=PATH）

向 build 暴露额外 directory，使 step 可使用 `from=NAME` bind-mount 它。

```sh
cornus build -t localhost:5000/app:latest \
  --build-context data=./data .
```

```dockerfile
RUN --mount=type=bind,from=data,target=/data ./import.sh /data
```

- `--build-context` 可重复。Remote build 中 directory 被 stream 至 server（默认 eager，使用 `--lazy` 时 lazy）。

**另请参阅：**[cornus build](/zh/cli/build)

## 在 remote server 构建（--builder）并 lazy stream context（--lazy）

`cornus build --builder` 会在 Cornus 服务器上运行构建，同时通过 **9P-on-WebSocket** 流式传输调用方的上下文、具名绑定目录、密钥和 SSH agent。构建保持 BuildKit 原生，缓存留在服务器上；主机无需 Docker，也不需要构建权限。

```sh
cornus build --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 \
  --build-context data=./big-data \
  --lazy ./context
```

在 Dockerfile 中，流式输入会作为普通 buildx 挂载 (`RUN --mount=type=bind,from=data`、`--mount=type=secret,id=token`、`--mount=type=ssh`) 出现。调用方的 ssh-agent 会为 `type=ssh` 挂载转发，因此可获取私有依赖，同时密钥绝不会离开你的机器。

- `--builder` 接受 `ws://` / `wss://` 或 `http(s)://` base URL (环境变量 `CORNUS_BUILDER`)；指定 server 的 selected connection profile 也会将 build 路由至 remote，但显式的 `--builder` 仍优先。
- 默认情况下，具名上下文会被急切同步。使用 `--lazy` (或 `CORNUS_LAZY_BUILD`) 时改为按需提供，因此只有构建实际读取的字节才会穿过网络：一个大小为 20 MB、构建仅读取 11 字节的上下文，只传输 11 字节。`containerd` build worker (`CORNUS_BUILD_WORKER=containerd`) 不支持 lazy。
- 以 `type=local` 指定的构建缓存使用名称而非文件系统路径，因此同一组 `--cache-to` / `--cache-from` 在本地和远程构建中表现完全一致。

**另请参阅：**[cornus build](/zh/cli/build)、[使用远程集群](/zh/guides/remote-clusters)

## 导入/导出 remote build cache（--cache-to / --cache-from）

使用 registry-backed cache 跨 machine 或 CI run 持久化和复用 build cache。

```sh
cornus build -t localhost:5000/app:latest \
  --cache-to type=registry,ref=localhost:5000/app:cache \
  --cache-from type=registry,ref=localhost:5000/app:cache .
```

- 两个 flag 都可重复，接受 buildx-style spec。对于 `type=local`，`dest=` / `src=` 值是 engine-managed key（省略时从 `--tag` 自动派生），并非 filesystem path，因此 local 与 remote build 行为一致。

**另请参阅：**[cornus build](/zh/cli/build)

## 强制 clean build（--no-cache）

忽略所有 cached layer，从头重新构建每个 step。

```sh
cornus build -t localhost:5000/app:latest --no-cache .
```

- 用于确定性复现 build，或上游 base-image 变化后使用。

**另请参阅：**[cornus build](/zh/cli/build)

## Rootless 构建（--rootless）

在 user namespace 中而非以 root 运行 local build。

```sh
cornus build -t localhost:5000/app:latest --rootless .
```

- 也可通过 `CORNUS_ROOTLESS` server-wide 设置。Host 需要可工作的 rootless user-namespace stack。

**另请参阅：**[cornus build](/zh/cli/build)、[安全](/zh/guides/security)
