# 部署工作负载

以下是使用 [cornus deploy](/zh/cli/deploy) 应用 [deploy spec](/zh/reference/deploy-spec)、访问 workload，以及在四种[部署后端](/zh/reference/deploy-backends)间驱动 workload 的操作方法。Local backend 由环境变量 `CORNUS_DEPLOY_BACKEND` 选择；没有 CLI flag。

## 在 Docker host 本地部署 Compose project(默认 dockerhost backend)

将 spec 应用至 local Docker daemon，即默认 `dockerhost` backend。

```sh
cornus deploy -f app.yaml
```

```yaml
name: web
image: localhost:5000/app:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
```

- `dockerhost` 需要 Docker socket(`/var/run/docker.sock`)。它功能最完整，可映射最多 spec field。

**另请参阅: **[cornus deploy](/zh/cli/deploy)、[Deploy spec](/zh/reference/deploy-spec)、[部署后端](/zh/reference/deploy-backends)

## 部署到 bare containerd host(CORNUS_DEPLOY_BACKEND=containerd)

在无 dockerd 的 containerd host 上原生运行 workload。

```sh
sudo CORNUS_DEPLOY_BACKEND=containerd cornus deploy -f app.yaml
```

- 仅 Linux；需要 root(创建 netns、运行 CNI)、containerd socket(`CORNUS_CONTAINERD_ADDRESS`，默认 `/run/containerd/containerd.sock`)以及 `/opt/cni/bin` 下的标准 CNI plugin。
- 相对 dockerhost 的已知缺口: attach 仅输出，healthcheck 被忽略。

**另请参阅: **[部署后端](/zh/reference/deploy-backends)、[cornus deploy](/zh/cli/deploy)

## 部署到 Kubernetes cluster(通过 server / connection profile)

`kubernetes` backend 仅 server / in-cluster 支持，因此应针对 cluster 中运行的 cornus server 部署。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

- 设置 `CORNUS_DEPLOY_BACKEND=kubernetes` 的 local `cornus deploy` 会 warning 后回退到 `dockerhost`；cluster backend 在 server(`cornus serve`)上运行。
- 一次将 server 存为 connection profile，后续 command 无需 `--server`。

**另请参阅: **[远程集群](/zh/guides/remote-clusters)、[部署后端](/zh/reference/deploy-backends)、[远程工作流](/zh/topics/remote-workflows)

## 应用 raw deploy spec file(cornus deploy -f spec.yaml)

直接部署 native schema，即 Compose 和 devcontainer 转换出的同一形状。

```sh
cornus deploy -f spec.yaml
```

- Spec 以命令式方式应用: 输入一个 spec，backend 将 workload 收敛到该状态。port、mount、volume、resource 和 healthcheck 的完整字段见参考。

**另请参阅: **[Deploy spec](/zh/reference/deploy-spec)、[cornus deploy](/zh/cli/deploy)

## 删除 deployment(cornus deploy --delete / cornus compose down)

在本地或针对 server 按名称拆除 deployment。

```sh
cornus deploy -f app.yaml --delete
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

- Compose project 请使用 `cornus compose down`(添加 `--volumes` 也移除 project-scoped named volume)。

**另请参阅: **[cornus deploy](/zh/cli/deploy)、[cornus compose](/zh/cli/compose)

## 在后台运行 deploy(-d/--detach)

将 spec 向 server POST 一次并退出，workload 无 client session 地保持运行。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --detach
# later, tear it down:
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

- Detached deploy 拒绝 client-local bind mount 与 client-sourced credential，published port bind 在 server host 而不是自动 forward。
- `--detach` 对 local deploy 无作用。

**另请参阅: **[cornus deploy](/zh/cli/deploy)、[远程工作流](/zh/topics/remote-workflows)

## 扩缩 replica 并配置 rolling update(deploy spec replicas + updateConfig)

设置 desired instance count，以及 Kubernetes rolling update 的进行方式。

```yaml
name: web
image: localhost:5000/app:v1
replicas: 3
updateConfig:
  parallelism: 1
  order: start-first
```

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

- 每个 backend 都遵循 `replicas`；host backend 的 published host port 仅指向 replica 0。
- `updateConfig` 只映射到 Kubernetes Deployment `strategy.rollingUpdate`；host backend recreate 单一 instance 并忽略它。

**另请参阅: **[Deploy spec](/zh/reference/deploy-spec)、[部署后端](/zh/reference/deploy-backends)

## 在运行中 workload 内运行 command(cornus exec)

类似 `docker exec`，经 server exec 进入 deployment 的第一个 instance。

```sh
cornus exec --server https://cornus.example.com -it web -- sh
```

- Deployment name 之后的所有内容原样传给 command。`-i` 转发 stdin；`-t` 请求 PTY(stdin 不是 terminal 时降级为 plain stream)。
- Remote command exit code 会作为 cornus 自身 exit code 传播。

**另请参阅: **[cornus exec](/zh/cli/exec)、[cornus config](/zh/cli/config)

## 将 client-local directory mount 到 remote workload(--local-mount，经 9P stream)

将您机器上的 directory bind-mount 到 remote server 上运行的 workload。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --local-mount ./config:/etc/app:ro \
  --local-mount ./data:/data
```

- `--local-mount SRC:DST[:ro]` 可重复，在 session 存续期间经 9P 提供该路径。工作负载就地读取您的文件，无需预先复制。
- 添加 `,cache` 可将源声明为不变的只读源。它使用 server 按文件缓存，并隐含 `:ro`。
- 添加 `,async` 可获得由 block protocol 支持的可写、缓存一致挂载。它适用于开发数据库等写密集型单一 writer 工作负载，需要 `replicas: 1`，且不能与 `ro` 或 `cache` 组合。
- 对数据库形态的 async 挂载，可先在 server 和 deploy caller 环境中设置 `CORNUS_BLOCK_COHERENCE=subhash,subfill`，再设置 `CORNUS_BLOCK_READAHEAD=64k` 或更大的 cap。参见[server 环境变量](/zh/reference/server-env-vars)。
- 需要 foreground session；`--detach` 拒绝 client-local mount。

**另请参阅: **[cornus deploy](/zh/cli/deploy)、[网络](/zh/guides/networking)、[远程工作流](/zh/topics/remote-workflows)

## 访问 published 和 unpublished port(自动 client-side forward + cornus port-forward)

`--server` session 中，published port(spec `ports:`)自动 forward 至 `127.0.0.1:<host>`；其他任意 container port 可按需用 `cornus port-forward` 访问。

```sh
# Published ports auto-forward for the session's lifetime:
cornus deploy -f app.yaml --server https://cornus.example.com
# (prints forwarding 127.0.0.1:8080 -> :80)

# Reach an unpublished container port separately:
cornus port-forward web 5432:5432
```

- Deploy 使用 `--no-forward-ports` 禁用 auto-forward。`cornus port-forward` 为每个 `LOCAL:REMOTE`(或 bare `PORT`)mapping bind 一个 local listener，并在 foreground 运行至 Ctrl-C。
- Cluster profile 中，两条路径都用 kubeconfig 直达 workload pod，必要时回退到经 server 的 tunnel；`/udp` mapping 在 dockerhost、containerd 和 bare backend 工作，但在 Kubernetes 上跳过。

**另请参阅: **[cornus port-forward](/zh/cli/port-forward)、[网络](/zh/guides/networking)、[远程工作流](/zh/topics/remote-workflows)
