# 指南

这里提供覆盖 Cornus 全部功能、以任务为中心的分功能操作指南。每篇指南都给出简短、可直接复制粘贴的操作步骤及必要上下文；需要完整细节时，请沿着“另请参阅”链接查看 [CLI 参考](/zh/cli/)、[参考](/zh/reference/deploy-spec)和[主题](/zh/topics/remote-workflows)页面。

初次使用 Cornus？先阅读[快速开始](/zh/introduction/quick-start)，然后回到这里查找任务所需的准确操作方法。需要组合多项功能的端到端场景，请参阅[Cookbook](/zh/cookbook/)。

## 查找指南

### [构建镜像](/zh/guides/building-images)

构建 Dockerfile 并推送到内置镜像仓库，传递 build arg，使用 cache / secret / SSH mount，添加命名构建上下文，在远程服务器上（按需）构建，导入和导出远程构建缓存，以及以 rootless 方式构建。

### [部署工作负载](/zh/guides/deploying-workloads)

将 Compose 项目或原始 deploy spec 部署到 Docker 主机、裸 containerd 主机或 Kubernetes；删除、分离、扩缩容和滚动发布；在工作负载中 exec；挂载客户端本地目录；访问已发布和未发布端口。

### [Compose、devcontainer 与 docker CLI](/zh/guides/compose-devcontainers-docker)

启动和停止 Compose 项目，检查和重新构建服务，使用多个文件 / env 文件 / profile，运行 Dev Container，并通过 Docker API 代理让标准 `docker` CLI 访问 Cornus 服务器。

### [使用远程集群](/zh/guides/remote-clusters)

将命令指向远程服务器，创建连接配置文件，自动转发端口进入集群内服务器，从自己的 kube 访问权限签发短期凭据，切换 context，并让流量经服务器路由。

### [网络操作](/zh/guides/networking)

转发本地端口，运行 SOCKS5 split-tunnel 代理，选择 conduit，并通过 hub 覆盖网络连接工作负载。

### [隧道](/zh/guides/tunnels)

通过托管 tunnel 将工作负载端口公开到公网——ngrok、SSH、Cloudflare 和 Tailscale 后端的分步设置说明。

### [Ingress](/zh/guides/ingress)

在 Kubernetes 后端为工作负载提供公开 HTTP(S) 主机名：自动派生或显式主机名、通过 cert-manager 提供 TLS，以及路径 / 端口 / class 路由。

### [Egress](/zh/guides/egress)

使用路由规则或 PAC policy script，让远程工作负载的出站流量经调用方网络访问 VPN、企业代理或隔离集群。

### [凭据](/zh/guides/credentials)

将调用方签发的 secret（包括 LLM API key）代理给工作负载，而不把它写入镜像、spec 或 pod spec。

### [镜像仓库与存储](/zh/guides/registry)

在文件系统、内存、S3 或 GCS / Azure 存储上提供镜像仓库；推送和拉取镜像；允许匿名拉取；向集群运行时声明镜像仓库；使用外部镜像仓库；并通过垃圾回收释放空间。

### [保护服务器](/zh/guides/security)

要求 bearer token，签发 JWT，针对 JWKS 验证，启用按身份授权的 mTLS，并在保护写入的同时允许匿名拉取。

### [可观测性](/zh/guides/observability)

启用 OpenTelemetry trace、metric 和 log；添加 Prometheus `/metrics` 端点；配置日志；并接入存活和就绪探针。

### [输出模式](/zh/guides/output-modes)

选择 CLI 呈现进度和结果的方式——`auto`、`fancy`、`plain` 或 `json`（供 agent 和脚本使用的 NDJSON）——并控制彩色输出。
