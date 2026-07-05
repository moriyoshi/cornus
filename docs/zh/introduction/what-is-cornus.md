# Cornus 是什么？

Cornus 通过单个 Go 二进制文件，将 Docker 开发工作流——`docker compose`、`docker` CLI 和 devcontainer——带到 Kubernetes 集群(或普通 Docker 主机)。它把通常需要由内部平台团队分别运行的三种工具收拢为一项自包含服务，因此小团队无需分别部署镜像仓库、BuildKit daemon 和 GitOps controller，即可构建、推送并将 Compose 项目部署到真实集群。

该项目是单一模块(`module cornus`、Go 1.26、Apache-2.0)。

## 三个子系统

Cornus 在同一个二进制文件中集成了它所依赖的镜像仓库、构建引擎和部署引擎。

1. **镜像仓库**——一个小型 OCI Distribution v1.1 镜像仓库(`/v2/*`)，由持久化 sha256 内容寻址存储支撑。持久化可插拔: 通过 `--storage` 选择文件系统(默认)、内存和 S3 / S3 兼容对象存储(使用 `-tags cloudblob` 构建时还可使用 `gs://` / `azblob://`)。它由卷 / PVC 或对象存储桶支撑，重启后仍可保留数据。参见[存储后端](/zh/reference/storage-backends)。
2. **构建引擎**——进程内 BuildKit solver(无需独立 `buildkitd`)，具备与 `docker buildx` 对等的能力: Dockerfile 构建、缓存挂载(`RUN --mount=type=cache`)、密钥挂载(`RUN --mount=type=secret`)、SSH agent 转发(`RUN --mount=type=ssh`)、命名构建上下文 / bind mount 以及远程缓存。构建可在本地或远程 Cornus 服务器运行；调用方的目录、密钥和 SSH agent 经 9P-on-WebSocket 流式传输，且可选择按需传输，因此只有构建实际读取的字节会跨越网络。可通过 [`cornus build`](/zh/cli/build) CLI 和 `/.cornus/v1/build` HTTP 端点使用。
3. **部署引擎**——命令式、可插拔的部署后端，提供四种后端: `dockerhost`(默认)在 Docker 主机上运行容器；`containerd` 在裸 containerd 主机上原生运行容器(CNI bridge 网络，无 dockerd)；`bare` 直接通过 OCI runtime 运行容器，无需任何 daemon；`kubernetes`(client-go)将 Deployment + Service 部署到集群。v1 没有 git-watch / 持续调谐功能。除核心功能外，部署侧还提供通过 9P 流式传输至远程工作负载的客户端本地 bind mount、已发布端口的自动客户端转发、通过托管 tunnel 对外暴露工作负载，以及让远程工作负载经调用方网络访问的客户端出口能力。参见[部署后端](/zh/reference/deploy-backends)。

这些子系统通过 OCI HTTP 而非共享 Go 存储集成: 构建引擎将镜像引用推送到镜像仓库，目标运行时再拉取它。镜像仓库的内容存储是 `pkg/storage` 背后的私有持久化层，因此 Cornus 也可以使用外部 OCI 镜像仓库。

## build → push → deploy 流程

工作负载通过与三个子系统一一对应的三个步骤到达集群:

1. 使用构建引擎**构建**镜像。
2. 将其**推送**到镜像仓库(Cornus 自己的或外部镜像仓库)。
3. 通过向部署后端应用 spec 来**部署**它；后端拉取镜像并运行它。

[`cornus compose up`](/zh/cli/compose) 是这些原语之上的便捷封装；需要显式控制时，也可直接使用 [`cornus build`](/zh/cli/build)、[`cornus push`](/zh/cli/push) 和 [`cornus deploy`](/zh/cli/deploy)。完整演示请参见[快速开始](/zh/introduction/quick-start)。

## 部署模型

Cornus 以容器镜像(以及预构建的静态 CLI 二进制文件)发布，既可作为本地 Docker 容器运行，也可作为一等 Kubernetes 服务运行(StatefulSet + PVC + Service + RBAC；提供 Helm chart)。预构建多架构镜像发布至 `ghcr.io/moriyoshi/cornus`(semver tag + `edge`)，镜像内附带第三方许可证归属信息。发布版本还附带静态 CLI 二进制文件(linux/darwin/windows)及 `SHA256SUMS` 清单，将 Helm chart 发布为 OCI artifact，并通过无密钥 cosign 对全部内容签名。

镜像仓库和部署子系统不需要特殊权限；构建引擎需要 root 或 rootless user-namespace 栈。请参阅[安装](/zh/introduction/installation)获取二进制文件，并参阅[架构概览](/zh/architecture/)了解权限要求。

## 接口

* **HTTP: **`/v2/*`(镜像仓库)、`/.cornus/v1/build` + `/.cornus/v1/build/attach`、`/.cornus/v1/deploy[/{name}[/{action}]]` + `/.cornus/v1/deploy/attach`、`/.cornus/v1/caretaker/attach`(pod sidecar 会合点)、`/.cornus/v1/hub/catalog`、`/.cornus/v1/gc`、`/healthz`、`/readyz`，以及可选的 Prometheus `/metrics`。
* **CLI (kong): **[`serve`](/zh/cli/serve)、[`config`](/zh/cli/config)、[`build`](/zh/cli/build)、[`push`](/zh/cli/push)、[`deploy`](/zh/cli/deploy)、[`exec`](/zh/cli/exec)、[`port-forward`](/zh/cli/port-forward)、[`tunnel`](/zh/cli/tunnel)、[`socks5`](/zh/cli/socks5)、[`compose`](/zh/cli/compose)、[`daemon`](/zh/cli/daemon)、[`hub`](/zh/cli/hub)、[`token`](/zh/cli/token)、[`health`](/zh/cli/version-health) 和 [`version`](/zh/cli/version-health)。[`cornus config`](/zh/cli/config) 管理 kubeconfig 风格的连接配置文件；它可自动转发端口到集群内服务器，并从调用方的 kube 访问权限签发短期凭据，因此每个命令都能在无需手动 tunnel 或 token 的情况下访问远程集群。参见[远程工作流](/zh/topics/remote-workflows)。
* **`cornus compose`: **兼容 Docker Compose 的命令组(`up` / `down` / `ps` / `build` / `restart` / `stop` / `start`)，将 Compose 命令转发给正在运行的 Cornus 服务器。它也原生读取 Dev Container 定义(`.devcontainer/devcontainer.json`)，支持单容器和基于 compose 的两种形式，以及生命周期命令和工作区挂载。
* **`cornus daemon`: **长期运行的客户端辅助 daemon: `daemon docker` 是本地 Docker Engine API 代理(将 `DOCKER_HOST` 指向它后，标准 `docker` CLI、`docker compose`，乃至官方 `@devcontainers/cli` 都会驱动远程 Cornus 服务器)；`daemon mounts` 则是由 `cornus compose up -d` 启动的每项目后台挂载 daemon。

## 接下来去哪里

* [比较](/zh/introduction/comparison)——了解 Cornus 与 Skaffold、Tilt、Telepresence、Mutagen、Werf 等工具的关系。
* [安装](/zh/introduction/installation)——获取 CLI、容器镜像，或从源码构建。
* [快速开始](/zh/introduction/quick-start)——完成 serve → build → deploy 演练。
* [输出模式](/zh/guides/output-modes)——了解 `auto` / `plain` / `fancy` / `json` 渲染方式。
* [架构](/zh/architecture/)——了解模块布局和设计决策。
