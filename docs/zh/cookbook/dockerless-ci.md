# 从 CI 无 Docker 地构建和部署

## 场景

CI pipeline（或 k3s / containerd node）需要构建镜像并 rollout 到 cluster，但任何地方都没有 Docker daemon——没有 `dockerd`、没有 `buildkitd`，也没有单独部署的 registry。In-cluster Cornus server 完成三项工作：进程内 BuildKit engine 构建镜像，内置 OCI registry 存储镜像，runtime（containerd / Kubernetes）拉取镜像。CI runner 只需携带 `cornus` binary 与 source tree。

## 使用的功能

- 从 runner 经 9P 驱动的进程内 build engine——见[构建镜像](/zh/guides/building-images)和[远程工作流](/zh/topics/remote-workflows)。
- 存储构建镜像的内置 OCI registry——见[镜像仓库和存储](/zh/guides/registry)。
- `kubernetes`（或 `containerd`）backend 的命令式 deploy engine——见[部署工作负载](/zh/guides/deploying-workloads)和[部署后端](/zh/reference/deploy-backends)。
- 使 pipeline step 无需 command-line 配置的 connection profile——见 [`cornus config`](/zh/cli/config)。

## 演练

1. **一次将 runner 指向 in-cluster server。**保存 connection profile，使后续 step 都能解析 endpoint（对于无 ingress cluster，还会为每条 command 打开到 Service 的 port-forward），无需 command-line 参数。指定 server 的 profile 也会自动将 remote build 路由到该 server。

   ```sh
   cornus config set-context ci \
     --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
   cornus config use-context ci
   ```

   对于可达 URL，`--server http://cornus.example:5000` 同样有效。CI 中，可从 runner 自己的 Kubernetes access 通过 `--kube-auth-service-account` / `--kube-auth-audience` 签发 bearer token，或传入 static `--token`。

2. **在 cluster 中构建并 push 到内置 registry。**Runner 经 9P-on-WebSocket 向 server stream build context 和任意 secret；进程内 BuildKit engine 构建，再将结果 push 到 Cornus colocated registry。Runner 不需要 Docker 或 build privilege。

   ```sh
   cornus build --builder ws://cornus.example:5000/.cornus/v1/build/attach \
     -t cornus.example:5000/app:$CI_COMMIT_SHA \
     --secret id=npmrc,src=$HOME/.npmrc \
     --rootless ./context
   ```

   `--rootless`（或 server-wide `CORNUS_ROOTLESS`）使 build 在 user namespace 内运行。由于 profile 已指定 server，可省略 `--builder`，让 build 自行路由至远程。要只拉取 build 实际读取的 byte，请在命名 build context 上添加 `--lazy`。

3. **部署刚构建的镜像。**对同一 server 应用 native deploy spec。`kubernetes` backend 会呈现 Deployment 和 Service；node 的 containerd 从内置 registry 拉回镜像。

   ```yaml
   # deploy.yaml
   name: app
   image: cornus.example:5000/app:$CI_COMMIT_SHA
   replicas: 3
   restart: unless-stopped
   ports:
     - { host: 8080, container: 80 }
   updateConfig:
     parallelism: 1
     order: start-first
   healthcheck:
     test: ["CMD", "curl", "-f", "http://localhost/healthz"]
     interval: 30s
     retries: 3
   ```

   ```sh
   envsubst < deploy.yaml > deploy.rendered.yaml
   cornus deploy -f deploy.rendered.yaml --server http://cornus.example:5000 --detach
   ```

   `--detach` POST spec 后返回，workload 无 client session 地保持运行，是 fire-and-forget pipeline step 的正确 mode。稍后使用 `cornus deploy -f deploy.yaml --delete --server ...` 拆除 deployment。

4. **或者通过 Compose 用一条 command 完成两步。**如果项目已有带 `build:` section 的 `compose.yaml`，`cornus compose up --build` 会在 cluster 构建每个 service image、push 并 deploy——同一条 daemonless path，只需一条 command。

   ```sh
   cornus compose up --build -d
   ```

## 工作原理

三个 subsystem 完全通过 OCI HTTP 集成：build engine 将 image reference push 到 registry，target runtime 再 pull。循环中没有 Docker daemon。Build engine 是 `docker buildx` 所用的**同一个** BuildKit solver，嵌入进程内，因此 cache mount、secret mount、SSH forwarding 和命名 build context 无需改变即可工作。Remote build 时，runner 在一个 WebSocket 上运行 read-through 9P server，engine 就地读取 context，因此 NAT 后的私有 CI runner 无需暴露任何内容。

存储结果的 registry 是 Cornus 自己内置的 OCI Distribution registry；image tag 的 registry host 由 `--registry` / `CORNUS_REGISTRY`、server 的 `GET /.cornus/v1/info`、再到 endpoint host 的顺序解析。多 node cluster 上，node containerd 必须能解析并信任该 host（标记 plain-HTTP 或提供 TLS）；参见[镜像仓库和存储](/zh/guides/registry)。

Deploy engine 将 spec 命令式应用至 selected backend。`kubernetes` 呈现 Deployment 和 Service；`containerd` 在 CNI bridge network 的 bare containerd host 原生运行 workload，无 dockerd。`containerd` backend 与 containerd **build worker**（`CORNUS_BUILD_WORKER=containerd`）配对时，刚构建镜像直接进入 host image store，部署完全无需 registry round trip。完整 backend matrix 见[部署后端](/zh/reference/deploy-backends)。

## 变体

- **Bare containerd node，无 Kubernetes。**以 root 运行 server，并设置 `CORNUS_DEPLOY_BACKEND=containerd CORNUS_BUILD_WORKER=containerd`；`cornus compose up --build` 会在 host 自身 containerd 上构建和部署。
- **外部 registry。**为已有 registry tag build，并设置 `CORNUS_REGISTRY`，使 deploy pull ref 指向它；其余流程相同。
- **跨运行的 registry cache。**添加 `--cache-to` / `--cache-from type=registry,ref=...`，使 cold CI runner 复用前一次 build cache。

**另请参阅：**[Cookbook](/zh/cookbook/) · [构建镜像](/zh/guides/building-images) · [部署工作负载](/zh/guides/deploying-workloads) · [镜像仓库和存储](/zh/guides/registry) · [远程工作流](/zh/topics/remote-workflows) · [部署后端](/zh/reference/deploy-backends)
