# 保护服务器

Cornus HTTP API（`/v2/*`、`/.cornus/v1/*`）默认**没有认证**——能访问 port 的任何人都能 push、build 和 deploy。下列 control 均为 opt-in，关闭时没有成本。请在 trusted network、authenticating proxy 后运行 Cornus，或启用此处认证。深入模型见[认证与 TLS](/zh/topics/auth-and-tls)。

## 要求 static bearer token

使用单一 opaque shared secret 开启 bearer auth。

```sh
# Server: enforcement turns on as soon as a verifier is configured.
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# Client: sent as Authorization: Bearer <token> on /.cornus/v1/* and /v2/*.
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

- `/healthz` 和 `/readyz` 保持开放；每个其他 request 都需要 token。
- Static token 不带**identity**，被当作 anonymous，故无法满足 per-identity policy（见下文）。标准 OCI client 使用：`docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"`。

**另请参阅：**[认证与 TLS](/zh/topics/auth-and-tls)、[cornus serve](/zh/cli/serve)

## 为 client 签发 JWT

Server 只验证 token；使用 `cornus token issue` 签发其接受的 JWT，并用相同 material 签名。

```sh
# Symmetric (HS256): the server verifies with the same secret.
export CORNUS_JWT_HS256_SECRET="$(openssl rand -hex 32)"   # >= 32 bytes
cornus token issue --sub ci-bot --scope api --ttl 1h --hs256-secret "$CORNUS_JWT_HS256_SECRET"

# Asymmetric: mint with a private key; the server holds only the public half.
cornus token issue --sub pod-x --scope caretaker --ttl 720h --private-key ./jwt-priv.pem
#   server side: CORNUS_JWT_PUBLIC_KEY=./jwt-pub.pem cornus serve
```

- `--scope api`（或空）是 full credential；`--scope caretaker` 限定于 `/.cornus/v1/caretaker/attach`。
- `--sub` 成为下方 policy 的 caller identity。设置时，`--iss` / `--aud` 必须匹配 `CORNUS_JWT_ISSUER` / `CORNUS_JWT_AUDIENCE`。

**另请参阅：**[cornus token](/zh/cli/token)、[认证与 TLS](/zh/topics/auth-and-tls)

## 针对 JWKS endpoint 验证 token

针对发布的 key set 验证 asymmetric JWT，支持 `kid` selection 和 rotation。

```sh
# Remote JWKS (cached, refetched on TTL and, rate-limited, on an unknown kid):
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve

# Local JWKS file (hot-reloaded on change):
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
```

- 仅接受 asymmetric algorithm；token 的 `kid` header 选择 key。签发时使用 `cornus token issue --kid <id> --private-key key.pem ...` 写入匹配 id。
- 始终验证 `exp` / `nbf`（一分钟 leeway）；拒绝 `alg: none` 或意外 algorithm。

**另请参阅：**[认证与 TLS](/zh/topics/auth-and-tls)、[cornus token](/zh/cli/token)

## 启用 mTLS，并从 client cert 派生 identity

使用其 CommonName 成为 caller identity 的 client certificate 认证 caller。

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- 提交的 cert 必须 chain 到 `--tls-client-ca`；其已验证 `Subject.CommonName` 是 identity。提交 cert 仍是**可选**的（bearer-only 和 probe client 继续工作），但一旦提交则必须验证。
- 已验证 client cert 是 full credential，并**优先于**同一 request 上的 bearer token。设置 `--tls-client-ca`（或 `CORNUS_TLS_CLIENT_CA`）本身即开启 auth。

**另请参阅：**[认证与 TLS](/zh/topics/auth-and-tls)、[安装](/zh/introduction/installation)

## 按 identity 授权 action

限制哪些 identity 可执行哪些 API action。

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

- Action：`deploy`（蕴含 `exec`）、`exec`、`build`、`push`、`pull`、`gc`。`"*"` 允许全部。
- 未设置时允许全部。设置后 caller 必须在该 action 的列表中，且**空 identity 被拒绝（fail closed）**——policy 需要 identifying credential（JWT `sub` 或 mTLS CommonName）；static token 与 anonymous caller 被拒绝。错误 JSON 是 hard startup error。

**另请参阅：**[认证与 TLS](/zh/topics/auth-and-tls)、[服务器环境变量](/zh/reference/server-env-vars)

## 在保护写入的同时允许匿名 registry pull

保持 push、build 和 deploy 在 auth 后面，但允许任何人 pull image。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- 只打开 `/v2/*` 下的 `GET` / `HEAD`；每个 write verb 仍需 credential。
- `CORNUS_API_POLICY` 中显式 `pull` rule 优先于此 flag（两者均设置时 startup warning）。没有 `pull` rule 时，registry pull 由 authentication 管理，因此两者不冲突。

**另请参阅：**[镜像仓库和存储](/zh/guides/registry)、[认证与 TLS](/zh/topics/auth-and-tls)

## 理解 scoped caretaker credential

每 pod caretaker（sidecar）只访问 `/.cornus/v1/caretaker/attach`，因此应为其提供独立 scoped token，而非 full token。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

- Server 仅在 caretaker endpoint 接受 caretaker token，并在 client API 与 registry 上拒绝它，因此从 pod spec 读出的 sidecar credential 无法 deploy、build、exec 或 push。
- 它可为 opaque `CORNUS_CARETAKER_TOKEN`，或 `caretaker`-scoped JWT（`cornus token issue --scope caretaker`），因此 JWT-only server 仍支持 k8s live mount。要使其不出现在 pod spec 中，请保存在 Secret，并用 `CORNUS_CARETAKER_TOKEN_SECRET` 指向它。

**另请参阅：**[认证与 TLS](/zh/topics/auth-and-tls)、[服务器环境变量](/zh/reference/server-env-vars)
