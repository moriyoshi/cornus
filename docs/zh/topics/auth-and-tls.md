# 认证和 TLS

Cornus HTTP API（`/v2/*`、`/.cornus/v1/*`）默认**没有认证**。未配置 auth 时，能访问 port 的任何人都能 push/pull image、运行 build 和创建 deployment。请只在 trusted network、authenticating reverse proxy 后运行 Cornus，或启用下方内置 bearer auth。本页每个 security control 都是**opt-in，关闭时没有成本**：不设置相关 env var 时，server 行为完全与之前相同，每 request 没有额外成本。

`--tls-cert` / `--tls-key`（或 `CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`）可提供进程内 TLS，但它提供的是 transport encryption，而非 caller authentication。

## Bearer 认证

只要至少配置一个 verifier，bearer authentication 就开启。启用后，每个 request 都需要有效 `Authorization: Bearer <token>`，但 `/healthz`、`/readyz`（始终开放）以及启用 anonymous pull 时 `/v2/*` 下的 `GET` / `HEAD` 例外。Cornus 只**验证** token，不签发 token。三种 verifier 可组合——任一 verifier 验证 token 即接受 request。

```sh
# 1. Opaque shared secret (level 0, zero dependencies):
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# 2. Symmetric JWT (HS256). Use a secret of at least 32 bytes:
CORNUS_JWT_HS256_SECRET="$(openssl rand -hex 32)" cornus serve

# 3. Asymmetric JWT (RS256 or ES256) verified with a PEM public key. The key
#    type selects the algorithm (RSA -> RS256, ECDSA -> ES256); HS256 is never
#    accepted against a public key (algorithm-confusion-safe):
CORNUS_JWT_PUBLIC_KEY=/etc/cornus/jwt-pub.pem cornus serve

# 4. JWKS with kid selection + rotation, from a file (hot-reloaded) or a URL
#    (cached, refetched on TTL and, rate-limited, on an unknown kid):
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve
```

可选 JWT claim check 仅在设置时强制：`CORNUS_JWT_ISSUER` 必须匹配 token `iss`，`CORNUS_JWT_AUDIENCE` 必须匹配 token `aud`。始终以一分钟 leeway 验证 `exp` 和 `nbf`，拒绝 `alg: none` 或意外 algorithm 的 token。完整 env var 见[服务器环境变量](/zh/reference/server-env-vars)。

### 签发 JWT

由于 server 只验证，请使用 `cornus token issue` 签发其接受的 JWT，并使用 server 验证端相同的 material 签名。Token `scope` 决定可达范围：`api`（或空 scope）是 full credential；`caretaker` 仅限 `/.cornus/v1/caretaker/attach`。

```sh
# Symmetric (HS256) -- the server verifies with the same secret:
cornus token issue --sub ci-bot --scope api --ttl 1h \
  --hs256-secret "$CORNUS_JWT_HS256_SECRET"

# Asymmetric -- mint with a private key; the server holds only the public key:
cornus token issue --sub pod-x --scope caretaker --ttl 720h \
  --private-key ./jwt-priv.pem      # server: CORNUS_JWT_PUBLIC_KEY=./jwt-pub.pem
```

向 JWKS verifier 签发时，用 `--kid <id>` 写入匹配 key id。完整 flag 集见 [`cornus token`](/zh/cli/token)。

### Client 侧

Cornus CLI 和 `pkg/client` 读取 `CORNUS_TOKEN`，并在 `/.cornus/v1/*` 调用、archive `PUT` 与 WebSocket attach handshake（deploy attach、build、exec）中以 `Authorization: Bearer <token>` 发送：

```sh
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

对于 auth enabled 时访问 `/v2/*` 的 external OCI client，`cornus push` 将 `CORNUS_TOKEN` 作为 registry bearer credential 发送。标准 `docker` / `podman` / `crane` 使用普通 `docker login`：registry 在 `/v2/*` 接受 HTTP Basic，password 是 token（static token 或 JWT），忽略 username，401 challenge 为 `Basic realm="cornus"`，标准 login flow 无需 token service：

```sh
docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"
```

## mTLS client-certificate identity

提供 TLS 时，Cornus 还可通过 **client certificate** 认证 caller——它是 bearer token 之外的额外方法，并非替代。将 `--tls-client-ca`（或 `CORNUS_TLS_CLIENT_CA`）指向 PEM CA bundle：

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

提交的 client certificate 必须 chain 到该 CA；其已验证 `Subject.CommonName` 成为 caller identity。提交 cert 仍然**可选**（listener 使用 `VerifyClientCertIfGiven`），因此 `/healthz`、`/readyz` 和 bearer-only client 无证书也能工作——但提交的 cert 必须通过验证。已验证 client certificate 是**full credential**，并**优先于**同一 request 的 bearer token。仅配置 `CORNUS_TLS_CLIENT_CA` 即开启 auth。

Caller 认证身份——mTLS CommonName 或 JWT `sub`——统一进入下方 per-identity authorization。Opaque static token（`CORNUS_AUTH_TOKEN`）不带 **identity**，被视为 anonymous。

## Per-identity authorization policy

`CORNUS_API_POLICY` 限制哪些 identity 可执行哪些 API action。它是将 identity 映射到允许 action list 的 JSON object；entry 可使用 `"*"` 允许所有 action。

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

| Action | 覆盖范围 |
| --- | --- |
| `deploy` | 创建/删除 deployment 及其 mutating lifecycle/attach action（蕴含 `exec`） |
| `exec` | 在运行中 deployment 内 exec/attach（exec-only entry 可提供 shell 但无 deploy 权限） |
| `build` | `POST /.cornus/v1/build` |
| `push` | `/v2/*` 下的 registry write（image push 和 delete） |
| `pull` | registry `GET` / `HEAD`——opt-in：仅当 rule 显式提及 `pull` 时强制（`"*"` 不计） |
| `gc` | destructive `POST /.cornus/v1/gc` reclaim endpoint |

未设置时允许所有内容；一旦配置，caller 必须为 action 被列出（或 `"*"`），且**空 identity 被拒绝（fail closed）**——因此 policy 需要 identifying credential（JWT `sub` 或 mTLS CommonName；opaque static token 与 anonymous caller 被拒绝）。错误 JSON 是 hard startup error。Read/GET endpoint 不受限制，除 registry pull 外，且只有 rule opt in 后才限制。

## Registry posture 和 anonymous pull

启用 auth 时，`/v2/*` 对每个 verb 都要求 auth。若要允许 unauthenticated pull（`GET` / `HEAD`），同时仍要求 push/delete auth：

```sh
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve   # 1/true/yes/on
```

`CORNUS_API_POLICY` 中显式 `pull` rule 优先于 `CORNUS_REGISTRY_ANONYMOUS_PULL`（两者同时设置时 startup warning）。没有提及 `pull` 的 rule 时，registry pull 由 authentication 而非 policy 管理，因此二者不冲突。

## Caretaker credential

每 pod caretaker 只访问 `/.cornus/v1/caretaker/attach`，因此获得**独立 scoped** token，而非 full token——server 只在 caretaker endpoint 接受它，并在 client API 与 registry 上拒绝它，所以从 pod spec 读出的 sidecar credential 无法 deploy、build、exec 或 push。在 auth 下运行 Kubernetes backend 时，与 client auth 一同设置；backend 会自动注入 mount/hub sidecar：

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

Scoped caretaker credential 可为 opaque `CORNUS_CARETAKER_TOKEN` string，也可为用 `cornus token issue --scope caretaker` 签发的 `caretaker`-scoped JWT——因此 JWT-only server（完全没有 static token）仍支持 k8s live mount。要使 token 不进入 pod spec，请将其保存在 Kubernetes Secret 中，并用 `CORNUS_CARETAKER_TOKEN_SECRET` 指向它；sidecar 随后在 runtime 通过 `secretKeyRef` 获取 token。

Server flag 见 [`cornus serve`](/zh/cli/serve)，完整 configuration surface 见[服务器环境变量](/zh/reference/server-env-vars)。
