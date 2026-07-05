# Simplified Chinese Documentation Translation Glossary

Use this table while translating `docs/` into `docs/zh/`. It is an internal
translation aid, not a published documentation page. Keep translated pages
faithful to their English source: do not add explanatory material, glossary
links, or first-use parenthetical English outside the source text.

## Preserve Verbatim

Keep product names, standards, command names, flags, environment variables,
configuration keys, front matter keys, API paths, URLs, code, and values
verbatim. This includes Cornus, Docker, Kubernetes, BuildKit, Compose, Helm,
OCI, HTTP, TLS, JWT, JWKS, SSH, WebSocket, 9P, CNI, Prometheus,
OpenTelemetry, and all text in code formatting or code blocks.

## Preferred Terms

| English | Simplified Chinese |
| --- | --- |
| build / deploy | 构建 / 部署 |
| server / client | 服务器 / 客户端 |
| service / workload | 服务 / 工作负载 |
| registry / storage | 注册表 / 存储 |
| backend / engine | 后端 / 引擎 |
| image / container | 镜像 / 容器 |
| cluster / host | 集群 / 主机 |
| remote / local | 远程 / 本地 |
| cache / mount | 缓存 / 挂载 |
| context / session | 上下文 / 会话 |
| connection profile | 连接配置文件 |
| endpoint / proxy / tunnel | 端点 / 代理 / 隧道 |
| secret / credential / token | 密钥 / 凭据 / 令牌 |
| credential brokering | 凭据中介 |
| authentication / authorization | 身份验证 / 授权 |
| ingress / egress | ingress / egress |
| reference / source of truth | 参考 / 权威来源 |
| default / required / optional | 默认 / 必需 / 可选 |
| read-only / full-access | 只读 / 完全访问 |
| filesystem / directory / path | 文件系统 / 目录 / 路径 |
| field / value / key / type | 字段 / 值 / 键 / 类型 |
| request / response / error | 请求 / 响应 / 错误 |
| observability / trace / metric | 可观测性 / 追踪 / 指标 |
| pluggable / persistence / persistent | 可插拔 / 持久化 / 持久 |
| automatic / manual | 自动 / 手动 |
| explicit / implicit | 显式 / 隐式 |
| external / internal | 外部 / 内部 |
| static / dynamic | 静态 / 动态 |
| named / shared / managed | 命名 / 共享 / 托管 |
| read-only / write-only | 只读 / 只写 |
| imperative / declarative | 命令式 / 声明式 |
| native / embedded | 原生 / 内嵌 |
| public / private | 公共 / 私有 |
| single / multiple | 单个 / 多个 |
| mint (a token or credential) | 签发 |
| port-forward / port-forwarding | 端口转发 |
| split-tunnel | 分流隧道 |
| task-oriented recipe | 面向任务的操作指南 |
| subsystem | 子系统 |
| environment variable(s) | 环境变量 |
| Kubernetes access | Kubernetes 访问权限 |
| rendezvous | 汇合点 / 连接协调 (视上下文而定) |
| clean up / tear down | 清理 / 拆除 (移除 保留给 remove) |
| apply / reconcile | 应用 / 调谐 (reconcile 在正文常保留英文) |
| rolling update | 滚动更新 |
| unpublished port | 未发布端口 |
| garbage collection | 垃圾回收 |
| content-addressable store | 内容寻址存储 |
| in-memory storage | 内存存储 |
| anonymous pull | 匿名拉取 |
| registry advertisement | 注册表通告 |
| no extra cost when disabled | 禁用时不产生额外开销 |
| dial back | 反向回连 |
| distributed hub store | 分布式 hub 存储 |
| GC leader gate | GC 领导者选举控制 |
| provider / plugin | provider / plugin (保留英文) |
| lifecycle | 生命周期 |
| idempotent | 幂等 |
| dependent (service) | 依赖方 |
| prefix | 前缀 |
| auto-reload | 自动重载 |

Translate compound terms as a unit before translating their components: build
engine (构建引擎), deploy engine (部署引擎), build cache (构建缓存), bind
mount (绑定挂载), cache mount (缓存挂载), secret mount (密钥挂载), named
context (命名上下文), client-side (客户端侧), client-local (客户端本机),
server-side (服务器端), content store (内容存储), object store (对象存储),
and data directory (数据目录). Preserve `cornus <command>`, `kubectl
<command>`, flags, configuration keys, and YAML keys verbatim even when their
prose equivalents appear in this table. Front matter is structured
configuration, so keys such as `layout`, `hero`, `image`, `src`, `actions`,
`theme`, `link`, and `linkText` must never be translated.
