# Compose、devcontainer 和 docker CLI

以下是兼容 Docker 界面的操作方法：内置 [cornus compose](/zh/cli/compose) client、devcontainer 支持，以及通过 [cornus daemon docker](/zh/cli/daemon) 驱动标准 `docker` CLI。三者均从 `--host` / connection profile / `http://localhost:5000` 解析 server。

## 启动和停止 Compose project（cornus compose up / down）

必要时构建、部署并在 foreground stream log；然后拆除 project。

```sh
cornus compose up
# Ctrl-C to stop, or from another terminal:
cornus compose down
```

- Foreground `up` 持有 client-local mount 和 auto-forwarded port，并保持至 Ctrl-C，再移除自己启动的内容。`down` 按 reverse dependency order 停止 service；添加 `--volumes` 也移除 project-scoped named volume。
- Compose file discovery 在 working directory 查找 `compose.yaml` / `compose.yml` / `docker-compose.yaml` / `docker-compose.yml`。

**另请参阅：**[cornus compose](/zh/cli/compose)、[部署工作负载](/zh/guides/deploying-workloads)

## 检查 project（cornus compose ps / logs）

列出 service 及状态，并 stream 它们的 log。

```sh
cornus compose ps
cornus compose logs --follow --tail 100 web
```

- `ps` 接受 `--format table|json`、`-q` 或 `--services`。`logs` 并发 stream 所有 selected service；`--follow` 没有短 `-f`，因为 group 已将 `-f` 用于 `--file`。
- Cluster profile 中，log 直接通过 kubeconfig 从 pod 读取，回退至 server proxy。

**另请参阅：**[cornus compose](/zh/cli/compose)

## 在 up 时构建镜像（cornus compose up --build，使用 --ssh）

启动前构建 service image，并向需要它的 build step 转发 ssh-agent。

```sh
cornus compose up --build --ssh default
```

- `--build` 在启动前构建所有 image（build service 始终构建）。`--ssh` 接受 `default` 或 `id[=socket]`，并与每 service 的 `build.ssh` merge。
- 如需只构建不启动，使用 `cornus compose build [--no-cache] [--build-arg KEY=VALUE]`。

**另请参阅：**[cornus compose](/zh/cli/compose)、[构建镜像](/zh/guides/building-images)

## 使用多个 Compose file、env file 和 profile（-f、--env-file、--profile）

Merge 多个 Compose file，指定 env file，并激活 profile service。

```sh
cornus compose \
  -f compose.yaml -f compose.prod.yaml \
  --env-file .env.prod \
  --profile debug up
```

- 这些是适用于每个 subcommand 的 group flag。`-f` 可重复并分层；`--env-file` 替换默认 `.env` discovery（后者获胜，process environment 仍优先）；`--profile` 可重复，并遵循 `COMPOSE_PROFILES`。

**另请参阅：**[cornus compose](/zh/cli/compose)

## 使用后台 agent detached 运行 (cornus compose up -d)

立即返回，将 client-local mount、forwarded port、SOCKS5 和 relay-backed egress 交给后台 agent。

```sh
cornus compose up -d
# later:
cornus compose down
```

- `-d`/`--detach` 将 mount、forwarded port、任意 SOCKS5 proxy 和 `proxy` / `transparent` egress session 交给客户端侧后台 agent，然后返回。之后使用 `down` 停止。用 `cornus daemon status` / `cornus daemon stop` 检查或停止 agent。
- 以文件为源的 Compose `configs:` 和 `secrets:` 是单文件客户端本地挂载。dockerhost 使用父目录加 subpath 实现；Kubernetes 的共享 9P sidecar mount 无法把单个文件投影到任意 rootfs target，因此会拒绝它们。目录 bind mount 在 Kubernetes 上仍受支持。containerd backend 当前不支持客户端本地 deploy mount。

**另请参阅：**[cornus compose](/zh/cli/compose)、[cornus daemon](/zh/cli/daemon)

## Rebuild / restart / stop / start service

在不完整 down/up 的情况下重新构建 image 或循环运行中 service。

```sh
cornus compose build web          # rebuild one service's image
cornus compose restart web        # restart in forward dependency order
cornus compose stop web           # stop in reverse dependency order
cornus compose start web          # start in forward dependency order
```

- `restart` / `stop` / `start` 均接受可选 service list（默认全部）。其 client-local mount 被 background `up -d` helper 持有的 service 会被拒绝；使用 `down` 停止它。

**另请参阅：**[cornus compose](/zh/cli/compose)

## 运行 Dev Container（cornus compose --devcontainer，或自动检测 .devcontainer）

启动 devcontainer definition 并运行其 lifecycle hook。

```sh
# Explicit path or search directory:
cornus compose --devcontainer .devcontainer up
# Or auto-detected when no Compose file is present:
cornus compose up
```

- 使用 `--devcontainer`、`-f` 指向 `devcontainer.json`，或不存在 Compose file 但可发现 `.devcontainer/devcontainer.json`（或 `.devcontainer.json`）时，使用 devcontainer。混合 repo 中 Compose file 始终优先。
- Lifecycle hook 会运行：任意 container 前在 host 上运行 `initializeCommand`，随后 container 启动时运行每 service 的 `postCreate` / `postStart` / `postAttach`。

**另请参阅：**[cornus compose](/zh/cli/compose)

## 面向 Cornus server 驱动标准 docker CLI（cornus daemon docker + DOCKER_HOST）

运行一个讲 Docker Engine API 的 local proxy，它将 container op 转换为 Cornus deploy，然后将 standard `docker` 指向它。

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

- Foreground run 持续到 Ctrl-C；`-d`/`--daemon` 在 background agent 上注册 frontend 后返回。Socket 默认 `$XDG_RUNTIME_DIR/cornus-docker.sock`（使用 `--socket` / `CORNUS_DOCKER_SOCK` 覆盖）。
- 调用方本地 bind-mount directory 经 9P stream 到 server。

**另请参阅：**[cornus daemon](/zh/cli/daemon)、[使用远程集群](/zh/guides/remote-clusters)

## Render merged config / print version（cornus compose config / version）

检查 Cornus 解析并 merge 后的 project view，或打印 Compose CLI version。

```sh
cornus compose config              # full merged model as YAML
cornus compose config --services   # just service names, in dependency order
cornus compose version --short
```

- `config` 还接受 `--volumes`、`--images`、`--format yaml|json` 和 `-q`（仅 validate，不打印）。`version` 接受 `--short` 或 `--format pretty|json`。

**另请参阅：**[cornus compose](/zh/cli/compose)、[cornus version-health](/zh/cli/version-health)
