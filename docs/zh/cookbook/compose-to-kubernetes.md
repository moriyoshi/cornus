# 不作修改地将本地 Compose 项目交付到 Kubernetes

## 场景

团队已有每天在本地运行的 `compose.yaml`。他们希望在真实 Kubernetes 集群中运行**同一个文件**——用于共享 staging environment 或 integration run——而无需将其重写为 Deployment、Service 和 PVC。Cornus 让 Compose file 在每个 backend 上都是实时 control surface，因此迁移只改变 connection profile，不改变 source。

## 使用的功能

- 驱动 server 上 build 与 deploy 的 Compose-compatible client——见[Compose、devcontainer 与 docker CLI](/zh/guides/compose-devcontainers-docker)和 [`cornus compose`](/zh/cli/compose)。
- 用于从 local server 切换到 in-cluster server 的 connection profile——见[使用远程集群](/zh/guides/remote-clusters)。
- Deploy engine 将 Compose 概念转换为 native spec——见[Deploy spec](/zh/reference/deploy-spec)和[部署后端](/zh/reference/deploy-backends)。

## 演练

1. **从已有的 Compose file 开始。**一个普通的多 service project: web frontend 连接 API，API 经 user network 连接 database:

   ```yaml
   # compose.yaml
   name: shop
   services:
     web:
       build: ./web
       ports:
         - "8080:80"
       depends_on:
         - api
       networks:
         - frontend
     api:
       build: ./api
       environment:
         DATABASE_URL: postgres://db:5432/shop
       networks:
         - frontend
         - backend
     db:
       image: postgres:16
       volumes:
         - db-data:/var/lib/postgresql/data
       networks:
         - backend
   networks:
     frontend:
     backend:
   volumes:
     db-data:
   ```

2. **按当前方式在本地运行。**面向 local Cornus server(默认 `dockerhost` backend)，`cornus compose up` 会构建带 `build:` 的 service、部署 stack，并将已发布 port 保持在 `127.0.0.1:8080`:

   ```sh
   cornus compose up --build
   # -> forwarding 127.0.0.1:8080 -> :80 ; curl http://127.0.0.1:8080 answers
   ```

3. **将 profile 指向 cluster。**一次保存 in-cluster server。对于没有 ingress 的 cluster，指定其 Service，让 CLI 在每个 command 前后打开 port-forward:

   ```sh
   cornus config set-context staging \
     --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
   cornus config use-context staging
   ```

4. **在 cluster 上运行完全相同的命令。**文件相同、command 相同——仅有区别是 selected profile，它解析到 `CORNUS_DEPLOY_BACKEND=kubernetes` 的 in-cluster server:

   ```sh
   cornus compose up --build
   ```

   `build:` service 会在集群中构建并 push 到内置 registry；每个 service 成为名为 `shop-web` / `shop-api` / `shop-db` 的 Deployment，加上其已发布 port 的 Service；`frontend` / `backend` user network 会在 cluster 中实现；`8080` 在 session 存续期自动转发回本机的 `127.0.0.1:8080`，因此虽然 workload 在 cluster 内运行，`curl http://127.0.0.1:8080` 仍会响应。

5. **用相同方式检查和拆除。**

   ```sh
   cornus compose ps
   cornus compose logs --follow web
   cornus compose down --volumes     # --volumes also removes the db-data PVC
   ```

## 工作原理

Compose file 在内部转换为 native [deploy spec](/zh/reference/deploy-spec)，同一个 spec 被应用到 server 运行的任意 backend，因此所有核心概念均不变:

- **Service**各成为一个 deployment，名为 `<project>-<service>`。
- **`ports:`**成为 published port。session 中它们在所有 backend(包括 Kubernetes)上自动转发至 `127.0.0.1:<host>`，所以 workload 会在 localhost 响应。可选择每 port listener(默认)，或用 `--conduit` 选择按名称访问 service 的单个 SOCKS5 proxy。
- **`networks:`**成为 user-defined network: 同一 network 的 member 通过 service name(及 alias)互相解析。Kubernetes 上默认 driver 是 `services`(仅 DNS，任意 cluster)；通过 `CORNUS_K8S_NET_DRIVER` opt in `bridge` / `ipvlan` / `macvlan`(Multus)或 `cilium`。
- **`volumes:`**成为 managed volume——named volume 是 project-scoped store，在单个 deployment delete 后仍存在(Kubernetes 为 PVC，`dockerhost` 为 Docker named volume)；anonymous volume 是 ephemeral。
- **`depends_on`**、**`healthcheck`**、**`deploy.replicas`**和**`deploy.update_config`**也会映射。

由于 backend 在 server 端选择，CLI-side workflow 在 `dockerhost`、`containerd`、`bare` 和 `kubernetes` 上相同——见[部署后端](/zh/reference/deploy-backends)。

### Kubernetes 上的差异

少数 Compose knob 没有 Kubernetes 等价物，按 field 处理([Deploy spec 参考](/zh/reference/deploy-spec)会标出每一项):

- Port 的 `hostIP`(Compose `127.0.0.1:8080:80`)在 host backend 被遵循，但 Kubernetes Service 没有等价物。
- UDP published port 在 `dockerhost` / `containerd` / `bare` 工作，但 Kubernetes port-forward 仅 TCP，因此 `/udp` mapping 在此跳过。
- Healthcheck 在 `dockerhost` 成为 Docker healthcheck，在 Kubernetes 成为 exec liveness / readiness probe。
- `deploy.update_config` 只映射到 Kubernetes Deployment `strategy.rollingUpdate`；host backend recreate 单一 instance。
- Compose `labels:` 在 Kubernetes 上成为 pod-template **annotation**，而非 label。大量 host-only knob(`init`、`stop_signal`、`ulimits`、`devices` 等)在 Kubernetes 上会 warning 后忽略。

## 变体

- **Detached staging。**`cornus compose up --build -d` 将 mount 和 forwarded port 交给 background helper 并返回；之后使用 `cornus compose down` 停止。
- **按名称访问 service。**`cornus compose up --conduit socks5` 用一个 proxy 替代 per-port listener，因此 `web.cornus.internal` 和 `db.cornus.internal` 经它解析。
- **分层 override。**保留 base `compose.yaml`，并为 cluster-only tweak 添加 `-f compose.staging.yaml`，command 仍然相同。

**另请参阅: **[Compose、devcontainer 与 docker CLI](/zh/guides/compose-devcontainers-docker) · [部署工作负载](/zh/guides/deploying-workloads) · [使用远程集群](/zh/guides/remote-clusters) · [Deploy spec](/zh/reference/deploy-spec) · [部署后端](/zh/reference/deploy-backends)
