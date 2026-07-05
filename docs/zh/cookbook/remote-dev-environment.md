# 集群上的远程开发环境

## 场景

您在轻量笔记本电脑上开发，但代码需要强大机器——大型 build、GPU 或会让风扇狂转的 database。您希望兼得两者：继续在自己的 editor 中*本地*编辑文件，却让代码在 cluster 中*远程*运行，workload port 可通过 `localhost` 访问，标准 Docker / Dev Container tooling 也无需改变。Cornus 让 remote server 像本地一样：[connection profile](/zh/reference/connection-config) 消除 endpoint 配置，[client-local bind mount](/zh/topics/remote-workflows) 经 9P 流式传输工作树而无需 copy，published port 自动 forward 回机器。

## 使用的功能

- [Connection profile](/zh/reference/connection-config)——使用 [`cornus config`](/zh/cli/config) 一次保存 server。
- [`cornus compose`](/zh/cli/compose)——在 server 上启动 Compose project（或 [Dev Container](/zh/guides/compose-devcontainers-docker)）。
- [经 9P 的 client-local bind mount](/zh/topics/remote-workflows)——source 留在笔记本电脑，按需 stream 至远程 workload。
- [自动端口转发](/zh/guides/deploying-workloads)——session 生命周期内，published port 在 `127.0.0.1:<host>` 响应。
- [`cornus daemon docker`](/zh/cli/daemon)——可选提供 `DOCKER_HOST`，使官方 `devcontainers` CLI（或标准 `docker`）驱动 remote server。

## 演练

**1. 将 cluster 存为 profile**，使每条 command 都无需 `--server` 或 token。对于无 ingress 的 in-cluster cornus，指定 Service，让 CLI 在每条 command 周围 port-forward：

```sh
cornus config set-context devbox \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context devbox
```

（如 server 有 URL，改用 `--server https://cornus.example.com --token "$(cat token.jwt)"`。`--kube-auth-*` flag 从您自身 kube access 签发 short-lived token，因此无需管理 static secret——见[远程集群](/zh/guides/remote-clusters)。）

**2. 将环境描述为 Compose project。**`volumes:` 中的 bind mount 是*您*笔记本电脑的路径；`ports:` 是希望在 `localhost` 访问的 port：

```yaml
name: devbox

services:
  app:
    build: .                      # built by the cornus engine, pushed to its registry
    command: ["npm", "run", "dev"]
    working_dir: /workspace
    volumes:
      - ./:/workspace             # client-local: streamed over 9P, edits sync live
    ports:
      - "3000:3000"               # dev server, reachable at 127.0.0.1:3000
    environment:
      NODE_ENV: development
    depends_on:
      - db

  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: dev
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  pgdata:                         # named: shared/persistent across up/down
```

**3. 在前台启动。**它在 server 构建所需内容，按 dependency order deploy，通过 9P 持有 bind mount，将 `3000` 和 `5432` 自动 forward 到 `127.0.0.1`，并 stream log：

```sh
cornus compose up --build
```

**4. 本地编辑，远程运行。**Editor 在笔记本电脑写入 `./src/...`；`app` container 经 9P mount 看到改动，dev server reload。打开 `http://localhost:3000`——request 被 tunnel 到 workload pod（cluster profile 使用 kubeconfig 直达 pod，否则经 server）。`psql -h 127.0.0.1 -p 5432` 也以同样方式访问 remote database。`Ctrl-C` 拆除 `up` 启动的内容。

**5. 按需访问从未发布的 port**，无需编辑 spec：

```sh
cornus port-forward app 9229:9229     # e.g. a debugger port
```

## 工作原理

这些组件协作，使 inner loop 无需改变。**Connection profile** 是 client-side、kubeconfig-style file，携带 endpoint、auth 和（此例中）in-cluster port-forward target，因此每次 `cornus compose` invocation 均无需 command-line 配置即可解析 server。**Client-local bind mount** 是本地编辑的关键：指向 host path 的 Compose `volumes:` entry 从本机经 9P 流式传输，并在 session 生命周期内由 server mount area 提供，因此 workload 就地读取文件——无需 upfront copy、无需 rsync，且 mount 始终无需放宽 host-privilege policy。**Published port** 自动 forward 至 `127.0.0.1:<host>`，即使 backend 是 Kubernetes，remote workload 也像 `docker compose` 一样在本地响应。三者均绑定到 live foreground `up`；detached `up -d` 会将 mount 和 forward 交给 background client agent（使用 `cornus daemon status` 检查）。细节见[远程工作流](/zh/topics/remote-workflows)及[部署工作负载](/zh/guides/deploying-workloads)步骤。

## 变体

**使用 Dev Container 而非 Compose file。**若 repo 有 `.devcontainer/devcontainer.json`，`cornus compose` 可原生读取它——无需手写 Compose file——并运行其 lifecycle hook（host 上的 `initializeCommand`，随后 container 内的 `postCreate` / `postStart` / `postAttach`），同时将 project 以 9P mount 至 `workspaceFolder`：

```sh
cornus compose --devcontainer . up
```

**在 VS Code 或 Zed 中面向 remote server 打开 devcontainer。**运行 client-side Docker Engine API proxy，并将 `DOCKER_HOST` 指向它；标准 `docker`、`docker compose`、官方 `devcontainers` CLI 与 editor Dev Container support 都会在 remote cornus 上运行 container，同时本地 bind-mount 目录经 9P stream：

```sh
cornus daemon docker -d
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/cornus-docker.sock"
devcontainer up --workspace-folder .      # official CLI, remote execution
```

Proxy 讲 Docker 的精确 protocol（create/start、attach、wait、lifecycle event stream），这正是使 VS Code Dev Containers extension 使用的官方 `@devcontainers/cli` 可不作修改驱动它的原因。因此从同一 shell 启动 editor（使其继承 `DOCKER_HOST`），并使用正常 Dev Container flow，container 便会远程运行：

- **VS Code**——安装 Dev Containers extension，运行 `code .`，然后选择 **Dev Containers: Reopen in Container**。
- **Zed**——运行 `zed .`，打开 project 的 Dev Container；Zed 通过同一 Docker endpoint 启动它。

因为 proxy 不模拟 Docker `/build` endpoint（构建属于 [`cornus build`](/zh/cli/build)），请在 `devcontainer.json` 中引用预构建 `image:`，而不是 `build:` / `dockerFile:`——若来自 Dockerfile，先使用 `cornus build -t <registry>/devcontainer:latest .` 构建。

**通过一个 proxy 按名称访问每个 service。**将 profile conduit 设置为 SOCKS5，一个 split-tunnel proxy 即可访问 `app`、`db` 和其他任意 service（其余 target 直接 egress）：

```sh
cornus config set-context devbox --merge --conduit-mode socks5
```

**另请参阅：**[远程工作流](/zh/topics/remote-workflows) · [远程集群](/zh/guides/remote-clusters) · [Compose、devcontainer 与 docker CLI](/zh/guides/compose-devcontainers-docker) · [连接配置](/zh/reference/connection-config) · [Cookbook](/zh/cookbook/)
