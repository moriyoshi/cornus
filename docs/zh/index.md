---
layout: home

hero:
  name: Cornus
  text: 将你的 Docker 工作流一路带到 Kubernetes
  tagline: >-
    将基于熟悉的 docker compose、docker CLI 和 devcontainer 的工作负载，直接带到 Kubernetes。
  image:
    src: /cornus-logo.svg
    alt: Cornus
  actions:
    - theme: brand
      text: 快速开始
      link: /zh/introduction/quick-start
    - theme: alt
      text: Cornus 是什么？
      link: /zh/introduction/what-is-cornus
    - theme: alt
      text: CLI 参考
      link: /zh/cli/
    - theme: alt
      text: 在 GitHub 上查看
      link: https://github.com/moriyoshi/cornus

features:
  - icon: 🔨
    title: 构建引擎 + OCI 镜像仓库
    details: >-
      BuildKit 的求解器已内嵌进二进制文件，无需单独运行 buildkitd，并提供与 docker
      buildx 对等的能力: 缓存 / 密钥 / SSH 挂载、命名上下文与远程缓存。构建出的镜像会写入
      内置的 OCI Distribution v1.1 镜像仓库 (/v2/*)，其持久化后端可插拔 (文件系统、内存、
      S3，以及通过构建标签启用的 GCS / Azure Blob)。构建既可在本地运行，也可通过
      9P-on-WebSocket 在远程服务器上运行。
    link: /zh/cli/build
    linkText: cornus build
  - icon: 🚀
    title: 命令式部署引擎
    details: >-
      在统一的接口之后，整合了四种可插拔的部署后端: dockerhost、原生 containerd、无 daemon 的 bare，以及基于
      client-go 的 Kubernetes。同时支持客户端侧的绑定挂载、端口转发、出口 (egress) 控制，
      以及连接各工作负载的 hub 覆盖网络。
    link: /zh/reference/deploy-backends
    linkText: 部署后端
  - icon: 🔁
    title: 与本地桥接反其道而行
    details: >-
      Telepresence、mirrord 与 Gefyra 让进程运行在你本地，再把它伪装成身处集群之中。
      Cornus 的思路恰好相反: 它把真正的工作负载部署进集群，再把集群拉到你面前。已发布的
      端口会自动转发到 127.0.0.1，cornus exec / port-forward 可访问任意容器端口，而
      *.cornus.internal 则按名称解析服务。
    link: /zh/introduction/comparison
    linkText: Cornus 有何不同
  - icon: 🐳
    title: 兼容 Docker 的客户端
    details: >-
      cornus compose 兼容 Docker Compose 命令；cornus daemon docker 则暴露一个 Docker
      Engine API 代理，让标准 docker CLI 与 devcontainer 也能驱动远程的 Cornus 服务器。
      devcontainer 定义可被原生读取。
    link: /zh/cli/compose
    linkText: cornus compose
  - icon: 🔐
    title: 默认安全且适合远程使用
    details: >-
      Bearer 认证 (静态 token / JWT / JWKS)、mTLS 身份，以及按身份授权，均为可选功能，
      关闭时不带来额外开销。连接配置文件可自动将端口转发进集群，并签发短期凭据。
    link: /zh/topics/auth-and-tls
    linkText: 认证与 TLS
  - icon: 📈
    title: 可观测性
    details: >-
      提供 OpenTelemetry 的追踪、指标与日志，以及可选的 Prometheus /metrics 端点；
      禁用时不产生任何开销，可按需启用。
    link: /zh/architecture/
    linkText: 架构
---
