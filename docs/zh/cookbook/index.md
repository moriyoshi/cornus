# Cookbook

[指南](/zh/guides/)展示如何一次使用一项功能；这里则提供端到端演练，将多项功能组合起来解决真实问题——包括准确命令、完整 deploy spec，以及各部分如何协作的说明。每个示例都可直接按您的项目进行改造。

## 场景

### [在容器中运行带有客户端 egress 路由的 AI agent](/zh/cookbook/ai-agent-egress)

将自主 AI agent 作为集群工作负载运行，让其出站 LLM API 调用经您的网络（企业代理 / VPN / SASE）路由，并在运行时代理 API key，使它绝不进入镜像。组合客户端侧 [egress](/zh/topics/egress)、凭据代理和 [deploy spec](/zh/reference/deploy-spec)。

### [集群上的远程开发环境](/zh/cookbook/remote-dev-environment)

使用轻量笔记本电脑面向强大的远程集群开发：在本地编辑文件，经 9P 在远程运行，在 `localhost` 访问端口，并使用标准 docker / devcontainer 工具——包括通过 [Docker API 代理](/zh/cli/daemon) 在 VS Code 或 Zed 中打开 Dev Container。组合[连接配置文件](/zh/guides/remote-clusters)、[Compose / devcontainer](/zh/guides/compose-devcontainers-docker)和客户端本地 bind mount。

### [临时预览环境](/zh/cookbook/preview-environments)

为每个 pull request 构建镜像并启动短生命周期环境，再通过托管 tunnel 将其公开，以便审阅者点击 URL；随后同样快速地销毁它。组合[构建](/zh/guides/building-images)、[部署](/zh/guides/deploying-workloads)和[隧道](/zh/guides/tunnels)。

### [从 CI 无 Docker 地构建和部署](/zh/cookbook/dockerless-ci)

在没有任何 Docker daemon 的情况下构建并发布到集群：集群内构建引擎负责构建，内置镜像仓库存储，containerd / Kubernetes 拉取。组合[构建引擎](/zh/guides/building-images)、[镜像仓库](/zh/guides/registry)和[部署后端](/zh/reference/deploy-backends)。

### [不作修改地将本地 Compose 项目交付到 Kubernetes](/zh/cookbook/compose-to-kubernetes)

获取可工作的 `compose.yaml`，在真实 Kubernetes 集群中以相同命令运行同一个文件，无需重写 manifest。组合 [Compose 客户端](/zh/guides/compose-devcontainers-docker)和[连接配置文件](/zh/guides/remote-clusters)。

### [通过 hub 覆盖网络连接微服务](/zh/cookbook/microservices-hub)

让独立部署的工作负载无需硬编码地址，即可通过稳定名称在同一集群内或跨后端互相访问。组合 [hub 覆盖网络](/zh/topics/hub)和 [deploy spec](/zh/reference/deploy-spec)。
