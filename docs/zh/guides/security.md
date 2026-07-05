# 安全与认证

Cornus HTTP API (`/v2/*`、`/.cornus/v1/*`) 默认**没有认证**。未配置 auth 时，能访问 port 的任何人都能 push/pull image、运行 build 和创建 deployment。请只在 trusted network、authenticating reverse proxy 后运行 Cornus，或启用下方内置 bearer auth。本页每个 security control 都是 opt-in，关闭时没有成本：不设置相关 env var 时，server 行为完全与之前相同，每 request 没有额外成本。

`--tls-cert` / `--tls-key` (或 `CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`) 可提供进程内 TLS，但它提供的是 transport encryption，而非 caller authentication。

## 工作原理

### Bearer 认证

只要至少配置一个 verifier，bearer authentication 就开启。启用后，每个 request 都需要有效 `Authorization: Bearer <token>`，但 `/healthz`、`/readyz` (始终开放) 以及启用 anonymous pull 时 `/v2/*` 下的 `GET` / `HEAD` 例外。Cornus 只**验证** token，不签发 token。三种 verifier (opaque shared secret、对称或非对称 JWT key、JWKS key set) 可组合——任一 verifier 验证 token 即接受 request。

可选 JWT claim check 仅在设置时强制：`CORNUS_JWT_ISSUER` 必须匹配 token `iss`，`CORNUS_JWT_AUDIENCE` 必须匹配 token `aud`。始终以一分钟 leeway 验证 `exp` 和 `nbf`，拒绝 `alg: none` 或意外 algorithm 的 token。完整 env var 见[服务器环境变量](/zh/reference/server-env-vars)。

### Caller identity

Caller 认证身份——mTLS CommonName 或 JWT `sub`——统一进入同一套 per-identity authorization policy。Opaque static token (`CORNUS_AUTH_TOKEN`) 不带 **identity**，被视为 anonymous。

### Client 侧

Cornus CLI 和 `pkg/client` 读取 `CORNUS_TOKEN`，并在 `/.cornus/v1/*` 调用、archive `PUT` 与 WebSocket attach handshake (deploy attach、build、exec) 中以 `Authorization: Bearer <token>` 发送：

```sh
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

对于 auth enabled 时访问 `/v2/*` 的 external OCI client，`cornus push` 将 `CORNUS_TOKEN` 作为 registry bearer credential 发送。标准 `docker` / `podman` / `crane` 使用普通 `docker login`：registry 在 `/v2/*` 接受 HTTP Basic，password 是 token (static token 或 JWT)，忽略 username，401 challenge 为 `Basic realm="cornus"`，标准 login flow 无需 token service：

```sh
docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"
```

**另请参阅：**[cornus serve](/zh/cli/serve)、[服务器环境变量](/zh/reference/server-env-vars)

## 要求 static bearer token

使用单一 opaque shared secret 开启 bearer auth。

```sh
# Server: enforcement turns on as soon as a verifier is configured.
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# Client: sent as Authorization: Bearer <token> on /.cornus/v1/* and /v2/*.
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

- `/healthz` 和 `/readyz` 保持开放；每个其他 request 都需要 token。
- Static token 不带**identity**，被当作 anonymous，故无法满足 per-identity policy (见下文)。标准 OCI client 使用：`docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"`。

**另请参阅：**[cornus serve](/zh/cli/serve)

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

- `--scope api` (或空) 是 full credential；`--scope caretaker` 限定于 `/.cornus/v1/caretaker/attach`。
- `--sub` 成为下方 policy 的 caller identity。设置时，`--iss` / `--aud` 必须匹配 `CORNUS_JWT_ISSUER` / `CORNUS_JWT_AUDIENCE`。
- Key 类型决定 algorithm (RSA -> RS256，ECDSA -> ES256)；对 public key 绝不接受 HS256，因此该配置对 algorithm confusion 是安全的。

**另请参阅：**[cornus token](/zh/cli/token)

## 针对 JWKS endpoint 验证 token

针对发布的 key set 验证 asymmetric JWT，支持 `kid` selection 和 rotation。

```sh
# Remote JWKS (cached, refetched on TTL and, rate-limited, on an unknown kid):
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve

# Local JWKS file (hot-reloaded on change):
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
```

- 仅接受 asymmetric algorithm；token 的 `kid` header 选择 key。签发时使用 `cornus token issue --kid <id> --private-key key.pem ...` 写入匹配 id。
- 始终验证 `exp` / `nbf` (一分钟 leeway)；拒绝 `alg: none` 或意外 algorithm。

**另请参阅：**[cornus token](/zh/cli/token)

## 启用 mTLS，并从 client cert 派生 identity

提供 TLS 时，Cornus 还可通过 **client certificate** 认证 caller——它是 bearer token 之外的额外方法，并非替代。将 `--tls-client-ca` (或 `CORNUS_TLS_CLIENT_CA`) 指向 PEM CA bundle。

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- 提交的 cert 必须 chain 到 `--tls-client-ca`；其已验证 `Subject.CommonName` 是 identity。提交 cert 仍是**可选**的 (listener 使用 `VerifyClientCertIfGiven`，因此 `/healthz`、`/readyz` 和 bearer-only client 继续工作)，但一旦提交则必须验证。
- 已验证 client cert 是 full credential，并**优先于**同一 request 上的 bearer token。设置 `--tls-client-ca` (或 `CORNUS_TLS_CLIENT_CA`) 本身即开启 auth。

**另请参阅：**[安装](/zh/introduction/installation)

## 按 identity 授权 action

`CORNUS_API_POLICY` 限制哪些 identity 可执行哪些 API action。它是将 identity 映射到允许 action list 的 JSON object；entry 可使用 `"*"` 允许所有 action。

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

| Action | 覆盖范围 |
| --- | --- |
| `deploy` | 创建/删除 deployment 及其 mutating lifecycle/attach action (蕴含 `exec`) |
| `exec` | 在运行中 deployment 内 exec/attach (exec-only entry 可提供 shell 但无 deploy 权限) |
| `build` | `POST /.cornus/v1/build` |
| `push` | `/v2/*` 下的 registry write (image push 和 delete) |
| `pull` | registry `GET` / `HEAD`——opt-in：仅当 rule 显式提及 `pull` 时强制 (`"*"` 不计) |
| `gc` | destructive `POST /.cornus/v1/gc` reclaim endpoint |

未设置时允许所有内容；一旦配置，caller 必须为 action 被列出 (或 `"*"`)，且**空 identity 被拒绝 (fail closed)**——因此 policy 需要 identifying credential (JWT `sub` 或 mTLS CommonName；opaque static token 与 anonymous caller 被拒绝)。错误 JSON 是 hard startup error。Read/GET endpoint 不受限制，除 registry pull 外，且只有 rule opt in 后才限制。

**另请参阅：**[服务器环境变量](/zh/reference/server-env-vars)

## 在保护写入的同时允许匿名 registry pull

保持 push、build 和 deploy 在 auth 后面，但允许任何人 pull image。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- 只打开 `/v2/*` 下的 `GET` / `HEAD`；每个 write verb 仍需 credential。该 flag 接受 `1`/`true`/`yes`/`on`。
- `CORNUS_API_POLICY` 中显式 `pull` rule 优先于此 flag (两者均设置时 startup warning)。没有 `pull` rule 时，registry pull 由 authentication 管理，因此两者不冲突。

**另请参阅：**[镜像仓库和存储](/zh/guides/registry)

## 理解 scoped caretaker credential

每 pod caretaker 只访问 `/.cornus/v1/caretaker/attach`，因此获得**独立 scoped** token，而非 full token。在 auth 下运行 Kubernetes backend 时，与 client auth 一同设置；backend 会自动注入 mount/hub sidecar。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

- Server 仅在 caretaker endpoint 接受 caretaker token，并在 client API 与 registry 上拒绝它，因此从 pod spec 读出的 sidecar credential 无法 deploy、build、exec 或 push。
- 它可为 opaque `CORNUS_CARETAKER_TOKEN`，或 `caretaker`-scoped JWT (`cornus token issue --scope caretaker`)，因此完全没有 static token 的 JWT-only server 仍支持 k8s live mount。要使 token 不进入 pod spec，请将其保存在 Kubernetes Secret 中，并用 `CORNUS_CARETAKER_TOKEN_SECRET` 指向它；sidecar 随后在 runtime 通过 `secretKeyRef` 获取 token。

**另请参阅：**[服务器环境变量](/zh/reference/server-env-vars)
