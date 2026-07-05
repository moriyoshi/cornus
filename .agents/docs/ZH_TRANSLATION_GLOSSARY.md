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
| observability store | 可观测性存储 |
| built-in store | 内置存储 |
| span / waterfall | span / 瀑布图 |
| retention | 保留策略 |
| shed / drop (records under load) | 丢弃 |
| resource usage | 资源用量 |
| sample / sampling (a metric) | 采样 |
| reading (one sampled value) | 读数 |
| semantic conventions | 语义约定 |
| metric family | 指标族 |
| cumulative / instantaneous | 累积 / 瞬时 |
| label (a metric dimension) | 标签 |
| replica ordinal | 副本序号 |
| datasource (Grafana) | 数据源 |
| full-text search | 全文检索 |
| record (a telemetry row) | 记录 |
| log tail / live stream | 日志 tail / 实时流 |
| survive (outlive the container) | 比容器存活得更久 |
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
| peer replica / peer credential | 对等副本 / 副本间凭据 |
| GC leader gate | GC 领导者选举控制 |
| provider / plugin | provider / plugin (保留英文) |
| lifecycle | 生命周期 |
| idempotent | 幂等 |
| dependent (service) | 依赖方 |
| prefix | 前缀 |
| auto-reload | 自动重载 |
| sidecar / companion / remote companion | sidecar / companion / remote companion (保留英文) |
| opt into | 启用 / 使用 |
| reroute | 改路 |
| co-located (server and daemon) | 同机 |
| mount propagation / `rshared` / `rslave` | propagation (保留英文; 挂载选项名同样保留) |
| pinned (network namespace) | 已固定的 |
| always-on / per-instance | 始终在线 / 每实例 |
| agent forwarding (ssh-agent) | agent 转发 |
| fast path | 快路径 |
| ingress tunnel | 入口隧道 |
| front door (server-side ingress) | 前端入口 |
| ingress controller | 入口控制器 |
| host mode / alias / passthrough / rewrite | Host 模式 / alias / passthrough / rewrite (模式名保持原文) |
| raw byte stream / raw splice | raw 字节流 / 中继 |
| end-to-end TLS | 端到端 TLS |
| default backend (of an ingress controller) | 默认后端 |
| flight record / recorder | 飞行记录 / 记录器 |
| activity / unfinished activity | 活动 / 没有完成的活动 |
| process lifetime | 进程生命周期 |
| incarnation (of a process) | 运行 (与 instance id 对应) |
| clean exit / unclean exit | 干净地退出 / 没有干净地退出 |
| follow (a stream) / live | 跟随 / 实时 |
| backlog (already-written records) | 历史 |
| keep-alive | 保活 |
| Server-Sent Events / SSE | 保持原文 |
| MCP tool / MCP resource | MCP 工具 / MCP 资源 |
| wizard / scenario / preset | 向导 / 场景 / 预设 |
| container host | 容器主机 |
| daemonless / no daemon | 无守护进程 |
| container runtime / OCI runtime | 容器 runtime / OCI runtime |
| application container (Incus) | 应用容器 |
| prerequisite(s) | 前提条件 |
| preflight (the `daemon preflight` check) | 预检 (命令名保留 `cornus daemon preflight`) |
| set up (a server) / already set up | 设置 / 已经设置好 |
| setup guide / next-steps checklist | 设置指引 / 后续步骤清单 |
| artifact (a generated setup file) | 产物 |
| systemd unit | systemd 单元 |
| probe (the server for facts) | 探测 |

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

## Auth and scope terms (settled 2026-07-29)

Settled while translating the scope-mapping and token-exchange sections of
`guides/security.md`, `guides/remote-clusters.md`, and
`reference/connection-config.md`. This page keeps more English technical nouns
inline than the ja tree does, matching the surrounding `guides/security.md`
prose (`token`, `claim`, `scope`, `endpoint`, `identity`, `credential` as
凭据).

| English | Chinese |
| --- | --- |
| claim (JWT) | claim (kept inline) |
| scope / scope map | scope / scope map (kept inline) |
| scope (a UI's subject, e.g. the Metrics page's Workloads/Server switch) | 作用范围 — NOT the inline `scope` above, which is the OAuth sense, and not 作用域, which reads as a programming-language variable scope. `cli/web.md` already used 作用范围 for this; settled 2026-08-02. The control's own label stays **Scope** (a UI string). |
| scope mapping | scope 映射 |
| issuer | issuer (kept inline); 签发者 in prose about the act |
| credential | 凭据 |
| mint / issue (a token) | 签发 |
| subject token | subject token (kept inline) |
| token exchange | 交换 (verb) / token exchange (noun, inline) |
| matcher | matcher (kept inline) |
| conjunction (all must match) | 合取 |
| catch-all rule | catch-all rule (kept inline) |
| allowlist | allowlist (kept inline) |
| narrow (a scope) | 收窄 |
| grant (verb) | 授予 |
| delegation | 委派 |
| audit trail | 审计记录 |

**Never use a full-width colon.** Writing this section introduced 9 of them and
`npm run docs:check-punctuation` caught every one. Chinese prose invites the U+FF1A form
by habit; the repo requires `: ` (half-width colon, half-width space), and at
end of line a bare `:` so no trailing space is left.

## Workspace and tiling terms (settled 2026-08-03)

Settled while translating the `## Workspace` section of `cli/web.md`, added when the
Files and Terminal screens merged into one tiled screen.

| English | Simplified Chinese |
| --- | --- |
| workspace (the screen) | 工作区 |
| pane / tile | 窗格 / 平铺块 |
| tiled | 平铺式 |
| split (verb / noun) | 切分 / 切分 |
| tab (of a pane stack) | 标签页 |
| file browser | 文件浏览器 |
| working directory | 工作目录 |
| mount list | 挂载列表 |
| command palette | 命令面板 |
| editor / viewer | 编辑器 / 查看器 |
| open (a file, into a pane) | 打开 |
| placement target | 落点标记 |

Note 工作区 (workspace) sits beside 工作负载 (workload) in the same sentences; they
differ by one character and are easy to typo into each other.

`prefix t` and other key spellings stay verbatim: they are typed, not read, and so
are `Enter` / `Space` / `Esc`. 打开 is the verb for putting a file into a pane and is
also the UI label (**打开**); it is deliberately the same word as 在终端中打开,
because in the UI they are the same verb. 落点 already carries "where a pane lands"
in this section (窗格的落点), so the wireframe targets are 落点标记 rather than a new
noun for the same idea.

## Inline code formatting vs the source (settled 2026-07-29)

`audit_markdown_translation.py` compares inline code spans as a multiset and
reports `missing` and `extra` separately. They are not equally meaningful, and
the difference is worth knowing before acting on a warning:

- **`missing` is a strong signal.** The source documents a flag, key, path, or
  value that the translation does not. Every `missing` triaged on 2026-07-29 was
  a real gap: a dropped bullet, an untranslated table row, a command the reader
  is meant to run, or a sentence still describing an older version of the source.
- **`extra` is a weak signal, but not noise.** It caught four real defects in one
  sweep — three of them in the ENGLISH source, which code-formatted a literal in
  one row and wrote it bare in the next (`dockerhost`, `perms`, `grpc`, and
  `0600` / `Ctrl-C` elsewhere). The translation was right and the source was
  inconsistent. When `extra` fires, check the SOURCE first.
- **`extra` is also produced legitimately.** CJK sentences often split where
  English runs on, so a translation may name a literal twice where the source
  names it once. That is faithful, not a defect. Six such cases were verified and
  left alone.

Rule: match the source's formatting decisions where the source is consistent;
where it is not, fix the source rather than propagating the inconsistency into
two more trees.

## Source-freshness tracking (added 2026-07-29)

`docs/.translation-state.json` records the SHA-256 of the English source each
translated page was last synced against. `npm run docs:check-translation-freshness`
(chained into `docs:check`) reports every page whose source has moved since.

Workflow when it fires:

1. Read the page against the current English. `git diff` on the source file is
   usually enough to see what moved.
2. Update the translation where the change is substantive.
3. Record it: `python3 .agents/skills/translate-documents/scripts/translation_state.py
   update --path <page.md>` (omit `--path` to re-record everything).

A mismatch proves the SOURCE changed, not that the translation is wrong — a typo
fix in English does not invalidate a translated page. Recording without looking is
the one use that defeats the mechanism.

Why this exists: on 2026-07-29 the structural audit passed for both locales while
three passages were silently stale — a whole intro sentence in
`reference/deploy-spec.md` still describing an older field set, a missing bullet in
`guides/security.md`, and two absent sections. No structural check can see prose
drift; a digest can. The baseline was seeded in bulk that day, which means it
asserts "not known to have drifted since 2026-07-29", NOT "verified correct
page-by-page". Its value is forward-looking.
