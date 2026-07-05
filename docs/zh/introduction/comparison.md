# 与类似工具的比较

Cornus 所处的生态十分拥挤，但它的组合方式并不常见：它是一个**自包含的单一二进制文件**，同时充当镜像仓库、镜像构建器和部署引擎，并通过您**已有的** `compose.yaml`、`docker` 命令或 `devcontainer.json` 驱动三者——**无需新的配置 DSL，也无需预先部署镜像仓库、`buildkitd` 或 GitOps controller**。这一领域的大多数工具编排您已运行的组件；Cornus 本身就是这些组件。最常与之比较的工具可分为三类。

## 内循环“在 Kubernetes 上开发”的编排器

[Skaffold](https://skaffold.dev/)、[Tilt](https://tilt.dev/)、[DevSpace](https://www.devspace.sh/)、[Garden](https://garden.io/) 和 [Okteto](https://www.okteto.com/) 都会自动化面向集群的 build -> push -> deploy 循环。它们是**编排器**：调用您的构建器（`docker` / BuildKit / kaniko），推送到您提供的镜像仓库，应用您编写的 manifest / Helm / kustomize，所有操作由工具特定的配置文件驱动（Skaffold YAML、`Tiltfile`、`devspace.yaml`、Garden 的项目图）。Cornus 在两个维度上不同：它**内置**构建器和镜像仓库，而非调用外部组件；它使用您**已有的 Docker 工件**——Compose 文件或 devcontainer——而不是新 DSL。Okteto 和 DevSpace 会将源码同步到集群中运行的开发容器，而 Cornus 将文件留在您的机器上，只经 9P 流式传输构建或 bind mount 实际读取的字节。

## 本地与远程集群之间的桥接工具

[Telepresence](https://www.telepresence.io/)、[mirrord](https://mirrord.dev/) 和 [Gefyra](https://gefyra.dev/) 在**本地**运行进程，同时让它表现得仿佛**位于**集群中——将运行中 pod 的流量、环境和文件读取一直截获到您的笔记本电脑。Cornus 从相反方向解决相邻问题：它将工作负载**部署到集群中**，再把集群带回您身边——已发布端口自动转发至 `127.0.0.1`，`cornus exec` / `cornus port-forward` 可到达任意容器端口，SOCKS5 conduit 将 `*.cornus.internal` 解析为按名称访问的服务，工作负载到工作负载的 hub 则跨 NAT 和集群边界连接服务。若目标是“在本地运行代码并访问集群依赖”，请选择 mirrord 或 Telepresence；若目标是“让 Compose 项目在集群中**运行**，同时保有本地 Docker 内循环的便利”，则应选择 Cornus。

## 远程文件同步工具

存在整类远程开发工具，只为让本地目录和远程目录保持同步。几乎都可归结为**两种同步引擎**：[Mutagen](https://mutagen.io/)（以及其 [mutagen-compose](https://mutagen.io/documentation/orchestration/compose/) 集成；Docker 于 2024 年收购，目前是 Docker Desktop 同步 bind mount 的基础）和 [Syncthing](https://syncthing.net/)，它们承袭自经典的 [Unison](https://github.com/bcpierce00/unison) 与 `rsync`（+ `lsyncd`）。Kubernetes 开发工具大多封装其中之一——[ksync](https://ksync.github.io/ksync/) 和 [Okteto](https://www.okteto.com/) 使用 Syncthing，[Garden](https://garden.io/) 的 code-sync 使用 Mutagen；[DevSpace](https://www.devspace.sh/) 自带实现，[Skaffold](https://skaffold.dev/docs/filesync/) / [Tilt](https://tilt.dev/) 则在变更时把文件复制进运行中的容器。它们共享同一种模型：将树**复制**到远端，再持续调谐两份副本——获得本地速度的远程读取和离线容忍度，但代价是第二份实体化副本、初始全量传输与双向冲突解决。

Cornus 完全不属于这一阵营。它不做同步，而是**提供服务**，因此更接近网络文件系统家族——**sshfs**、**NFS**、**virtiofs**（Docker Desktop 的 VM bind 路径）、9P。远程构建或客户端本地 bind mount 期间，调用方运行 read-through 9P 服务器，工作负载**就地**读取调用方文件——只有一个事实来源，因此没有分歧、冲突解决或预先复制。它与普通网络挂载的不同在于传输和作用域：9P 经一个 WebSocket 隧道传输（无需任一端运行 mount daemon，也能穿越 NAT），限制在 context / named-context / mount 目录内并由 `.dockerignore` 过滤；使用 `--lazy` 时按需提供，因此只有构建或挂载实际访问的字节跨网传输（构建只读取 11 字节的 20 MB context 实际只传输 11 字节）。其取舍正是同步的镜像：未缓存读取依赖链路，而非驻留的本地副本，所以 Cornus 面向内循环 / 开发场景，而非长期离线工作。若您的流程是“在此编辑、在彼运行、两端保持收敛”，Mutagen 这类专用同步器更适合；Cornus 将等价能力融入自身传输，无需额外运行任何组件。（Mutagen 也可转发网络端口；Cornus 以自己的每连接 tunnel 覆盖该能力——参见[网络](/zh/guides/networking)。）

## Cornus 所整合的组件

| 否则需要运行的组件 | Cornus 的做法 |
| --- | --- |
| [BuildKit](https://github.com/moby/buildkit) / 作为 daemon 的 `buildkitd` | 在进程内嵌入**同一** BuildKit solver——完整 `buildx` 功能集，无需 daemon |
| [Docker Registry](https://github.com/distribution/distribution)（`distribution`）、[Zot](https://zotregistry.dev/)、[Harbor](https://goharbor.io/) | 内置小型 OCI Distribution v1.1 镜像仓库，并使用可插拔内容存储 |
| [Kompose](https://kompose.io/) / [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) | 它们只将 Compose **一次性**转换为 manifest；Cornus 让 Compose 保持实时控制界面 |
| [nerdctl](https://github.com/containerd/nerdctl)（containerd 上的 Docker CLI） | containerd 部署后端可在裸 containerd 主机上原生运行 Compose 项目，也能面向 Docker 和 Kubernetes |
| 面向本地 daemon 的标准 `docker` / `docker compose` | 相同命令被重定向至远程 Cornus 服务器（`cornus daemon docker`、`cornus compose`），文件从您的机器流式传输 |

最接近的单二进制类比是 [Werf](https://werf.io/)，它同样从一个二进制文件构建并部署到 Kubernetes；但 Werf 由 Git 驱动，仍依赖外部镜像仓库和基于 Helm 的应用，而 Cornus 由 Compose / devcontainer 驱动，提供自己的镜像仓库，并在 Docker、containerd 和 Kubernetes 上以命令式方式调谐 `DeploySpec`。

## 另请参阅

- [Cornus 是什么？](/zh/introduction/what-is-cornus)——三个子系统和端到端流程。
- [快速开始](/zh/introduction/quick-start)——从 Compose 文件到运行中的工作负载。
- [架构](/zh/architecture/)——各组件如何组合，以及背后的原因。
