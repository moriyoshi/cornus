# 安全模型

安全是分层且**可选**的：未配置任何内容时，Cornus 是适用于本地开发的 pass-through。下列每一层均由配置启用，并且**一旦启用即 fail closed**——错误 policy 会导致 hard startup error，需要 identity 时拒绝空 identity，配置错误的 verifier 会拒绝而非放行。本页说明模型；[认证与 TLS](/zh/topics/auth-and-tls)介绍设置，[安全指南](/zh/guides/security)提供加固步骤。

## 认证

认证是环绕全部 HTTP surface 的 middleware seam，接在 telemetry handler 内，因此被拒绝的 request 仍会得到 trace。未配置 verifier 时它是 pass-through。启用后，`/healthz` 和 `/readyz` 保持开放；其他每条 route 都需要 bearer auth，只有设置 `CORNUS_REGISTRY_ANONYMOUS_PULL` 时 `/v2/*` 下的 `GET`/`HEAD` 例外。Verifier 配置由环境变量驱动：

| 变量 | 方法 |
|---|---|
| `CORNUS_AUTH_TOKEN` | opaque full-access bearer token，constant-time 比较 |
| `CORNUS_JWT_HS256_SECRET` | HS256 JWT |
| `CORNUS_JWT_PUBLIC_KEY` | 来自 PEM public key 的 RS256/ES256 JWT |
| `CORNUS_JWT_JWKS_FILE` / `_URL` | 使用 `kid` selection 和 rotation 的 JWKS（仅 asymmetric） |
| `CORNUS_JWT_ISSUER` / `_AUDIENCE` | 可选 registered-claim check |
| `CORNUS_CARETAKER_TOKEN` | 仅在 caretaker attach endpoint 接受的 scoped static token |

JWT verification 将每个 key 绑定到允许的 algorithm set——拒绝 `alg: none`、algorithm confusion 与 public-key-as-HMAC——并将 caller identity 保存到 request context 供授权使用。Server 仅验证：token issuance（`cornus token issue`）是 operator/CLI 操作，而不是 HTTP minting endpoint。Kubernetes caretaker sidecar 从 Kubernetes Secret 获取**scoped** credential（仅对 caretaker attach endpoint 有效），而非将 full-access token 带入每个 pod。

镜像仓库还支持 `docker login`：仅在 `/v2/*` 上，以相同 credential 作为 password 接受 HTTP Basic——static token 或 JWT——并忽略 username（`docker login -u token -p $CORNUS_TOKEN`），输入同一 verifier chain。Registry 的 401 challenge 是 `Basic realm="cornus"` 而不是 `Bearer`：Cornus 没有 token service，Bearer challenge 会令 docker 前往不存在的 token realm；Basic 可使标准 docker/podman 用保存的 login 重试。非 registry route 仍 challenge `Bearer`，以 Basic 封装的 caretaker-scoped credential 在 registry 上仍会被拒绝。

## TLS 和 mTLS identity

TLS serving 内置于 `cornus serve`，使用 `--tls-cert`/`--tls-key`，并通过 reload callback 在文件 modification time 前进时重新读取文件——外部 rotator（cert-manager、Vault、SPIFFE）可原地续签挂载 certificate，无需 restart。

mTLS client-cert identity 是额外认证方法：已验证 client certificate 是 full credential，其 CommonName 为 caller identity，并优先于 bearer token。hub 使用同一 authenticated identity，因此 hub reach/register policy 基于 spoke 无法伪造的 credential。

## 授权

按 identity 的 API authorization 位于认证之上，是 configure-to-enforce matrix：`CORNUS_API_POLICY` 将 identity 映射到允许 action（`build`、`deploy`、`exec`、`push`、`pull`、`gc`）。未设置即 allow-all；一旦配置，caller 必须列在所请求 action 中，且**空 identity 被拒绝**——有效执行要求 JWT `sub` 或 mTLS CommonName。Pure read（deploy status、log、registry pull）默认保持开放，由认证而不是 per-identity authorization 管理。另有两项细化：

- **`exec` 是独立 action。**如果 policy 允许 `exec` *或* `deploy`，则允许 Exec/attach——deploy 蕴含 exec，因此该 action 的价值在于 exec-only identity 可 shell 进入运行中 workload，却不能 apply 或 delete。
- **Registry pull authorization 是 opt-in。**任何 rule 显式提及 `pull` action（`"*"` wildcard 不计）时，registry `GET`/`HEAD` 要求它。显式 pull policy 优先于 `CORNUS_REGISTRY_ANONYMOUS_PULL`——anonymous caller 没有 identity，会被拒绝——两者同时配置时 server 会在 startup warning。

Deploy backend 还独立执行**workload privilege policy**以实施 defense in depth：host backend 除非由 `CORNUS_ALLOW_PRIVILEGED` / `CORNUS_ALLOW_BIND_SOURCES` opt in，否则拒绝 `Privileged` 和 host bind source；Kubernetes backend 默认拒绝用户请求的 privileged workload，但允许 Cornus 自身注入、确实需要 privilege 进行 kernel 9P mount 或 network redirection 的 sidecar。

## 信任边界

以下边界在其子系统中已说明，集中如下：

- **Remote-build export 只读且受限。**Remote builder 仅经 9P 访问 context、dockerfile 和 named-context 目录——无 `..`、无 symlink escape、无 write，且字节离开调用方前执行 `.dockerignore`。参见[构建引擎](/zh/architecture/build-engine#信任边界)。
- **Session id 是 capability。**Deploy-attach session id 不可猜测，位于已认证 stream 内而非 URL；mount relay 仅发布其 digest。
- **每一跳重新评估 egress policy。**Caretaker、server 和 client 都检查 routing policy，受损 pod 无法提升路由；无 session egress 仅对 operator-gated gateway route 有效。参见[客户端侧 egress](/zh/architecture/caretaker#客户端侧-egress)。
- **Hub policy 基于 verified identity。**mTLS 下 spoke identity 来自 client certificate，不来自其自身声明。参见[hub](/zh/architecture/networking#发现和策略)。
- **Pod 内 Docker endpoint 需要显式 operator grant。**仅在配置专用 client-scoped token Secret 时才启用 `docker` caretaker role，因为它授予 workload deploy-engine access。参见[Docker endpoint](/zh/architecture/caretaker#docker-endpoint)。

## 相关页面

- [认证与 TLS](/zh/topics/auth-and-tls)——配置所有 verifier 和 TLS mode。
- [安全指南](/zh/guides/security)——加固步骤。
- [cornus token](/zh/cli/token)——签发 JWT。
- [服务器环境变量](/zh/reference/server-env-vars)——完整 policy surface。
