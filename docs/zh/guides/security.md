# 安全与认证

Cornus HTTP API (`/v2/*`、`/.cornus/v1/*`) 默认**没有认证**。未配置 auth 时，能访问 port 的任何人都能 push/pull image、运行 build 和创建 deployment。请只在 trusted network、authenticating reverse proxy 后运行 Cornus，或启用下方内置 bearer auth。本页每个 security control 都是 opt-in，关闭时没有成本: 不设置相关 env var 时，server 行为完全与之前相同，每 request 没有额外成本。

上一段对一个不带参数的 `cornus serve` 完全适用，因为默认监听地址是 **`:5000`，即全部网络接口**。它必须如此: 容器化的 caretaker 为客户端本地挂载、客户端侧 egress、凭据投递和工作负载遥测反向连接 server，而宿主机的 `127.0.0.1` 并不是容器的那一个，仅绑定回环地址会让这些功能失效。所以请把绑定范围与开启 auth 当作同一个决定。

如果你**不**需要工作负载访问 server，可以用 `--addr 127.0.0.1:5000` (`CORNUS_ADDR=127.0.0.1:5000`) 把它限制在本机；server 会在启动日志中说明该限制。参见[监听地址与暴露范围](/zh/cli/serve#监听地址与暴露范围)。

`--tls-cert` / `--tls-key` (或 `CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`) 可提供进程内 TLS，但它提供的是 transport encryption，而非 caller authentication。

## 工作原理

### Bearer 认证

只要至少配置一个客户端 verifier，bearer authentication 就开启。启用后，每个 request 都需要有效 `Authorization: Bearer <token>`，但 `/healthz`、`/readyz` (始终开放) 以及启用 anonymous pull 时 `/v2/*` 下的 `GET` / `HEAD` 例外。Cornus 验证客户端 token，但不公开通用 HTTP token minting service。三种 verifier (opaque shared secret、对称或非对称 JWT key、JWKS key set) 可组合——任一 verifier 验证 token 即接受 request。

可选 JWT claim check 仅在设置时强制: `CORNUS_JWT_ISSUER` 必须匹配 token `iss`，`CORNUS_JWT_AUDIENCE` 必须匹配 token `aud`。始终以一分钟 leeway 验证 `exp` 和 `nbf`，拒绝 `alg: none` 或意外 algorithm 的 token。完整 env var 见[服务器环境变量](/zh/reference/server-env-vars)。

### Caller identity

Caller 认证身份——mTLS CommonName 或 JWT `sub`——统一进入同一套 per-identity authorization policy。Opaque static token (`CORNUS_AUTH_TOKEN`) 不带 **identity**，被视为 anonymous。

### Client 侧

Cornus CLI 和 `pkg/client` 读取 `CORNUS_TOKEN`，并在 `/.cornus/v1/*` 调用、archive `PUT` 与 WebSocket attach handshake (deploy attach、build、exec) 中以 `Authorization: Bearer <token>` 发送:

```sh
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

对于 auth enabled 时访问 `/v2/*` 的 external OCI client，`cornus push` 将 `CORNUS_TOKEN` 作为 registry bearer credential 发送。标准 `docker` / `podman` / `crane` 使用普通 `docker login`: registry 在 `/v2/*` 接受 HTTP Basic，password 是 token (static token 或 JWT)，忽略 username，401 challenge 为 `Basic realm="cornus"`，标准 login flow 无需 token service:

```sh
docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"
```

启用客户端认证时，Cornus 还会为自身数据平面创建独立的安装签名 key (`CORNUS_INSTALLATION_SECRET`，或 `$CORNUS_DATA/installation.key`)。进程内构建使用 15 分钟的 `registry:push` credential；dockerhost、containerd、bare 和 Incus 拉取使用 15 分钟的 `registry:pull` credential。Kubernetes 获得 namespace-scoped `cornus-registry-pull` pull Secret，其中的 credential 有效期为 12 小时，每 4 小时刷新一次。只对服务器自身通告或 loopback registry host 签发 credential，绝不会对第三方 registry 签发。安装 key 不会启用客户端认证，不作为客户端 credential 接受，也不会由 HTTP endpoint 公开。

Helm chart 将此安装 key 置于共享 Secret 中，并通过 Helm `lookup` 在 upgrade 时保留实际值。因此，无法查询 cluster 的单独 `helm template` 每次 render 都会显示新的随机值；这只是 render output 的表面差异，并非轮换已安装的 key。

### 副本间转发

同时启用客户端身份验证和分布式 hub 存储 (`CORNUS_HUB_REDIS` 或 `CORNUS_HUB_STORE=kube`) 时，每个服务器副本都会在 `$CORNUS_DATA/peer.key` 创建模式为 `0600` 的 ECDSA P-256 私钥。只有公钥会发布到 hub 存储。Redis 按副本的心跳 TTL 保存公钥；Kubernetes 存储让公钥归副本的 Lease 所有，因此已离开副本的密钥与其路由记录遵循相同的存活生命周期。关闭身份验证或使用内存存储的单副本服务器不会创建副本间密钥。

对于 `/.cornus/v1/hub/forward`、`/.cornus/v1/mount/forward` 和 `/.cornus/v1/cred/forward`，没有 `CORNUS_AUTH_TOKEN` 的发送方会签发并缓存有效期为 5 分钟的 ES256 JWT。其 scope 为 `peer`，`sub` 和 `kid` 都是副本 ID。接收方通过 hub 存储解析 `kid`，只接受 ES256，并要求 `sub == kid`。`peer` scope 只能访问这三个转发端点，不能调用客户端 API、读写注册表，也不能作为 caretaker attach。

显式设置的 `CORNUS_AUTH_TOKEN` 仍具有绝对优先级，并且会原样发送。旧副本不理解 `peer` scope，因此该规则可保持混合版本的滚动更新。也就是说，只有此前没有副本间凭据的纯 JWT、JWKS 或 mTLS 多副本配置会启用副本间凭据。

**另请参阅:** [cornus serve](/zh/cli/serve)、[服务器环境变量](/zh/reference/server-env-vars)

## 要求 static bearer token

使用单一 opaque shared secret 开启 bearer auth。

```sh
# Server: enforcement turns on as soon as a verifier is configured.
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# Client: sent as Authorization: Bearer <token> on /.cornus/v1/* and /v2/*.
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

- `/healthz` 和 `/readyz` 保持开放；每个其他 request 都需要 token。
- Static token 不带**identity**，被当作 anonymous，故无法满足 per-identity policy (见下文)。标准 OCI client 使用: `docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"`。

**另请参阅:** [cornus serve](/zh/cli/serve)

## 使用 SSH 公钥会话

SSH 密钥身份验证只在服务器上保存已授权的公钥。客户端对服务器签发且绑定用途的质询进行签名，并获得使用固定 Cornus SSH 密钥 issuer 和 audience 的短期 JWT。服务器的安装密钥为这些会话签名；操作员 JWT 密钥保持独立。

启用适用于单服务器的可写密钥存储，获取其本地注册码，然后注册客户端密钥:

```sh
# 服务器 (不设置此项时，默认的无身份验证状态不会改变):
CORNUS_AUTH_KEYSTORE=file cornus serve

# 以服务器 uid 在本地运行，或通过 ssh/docker exec/kubectl exec 运行:
code=$(cornus auth enrollment-code)

# 客户端:
cornus auth enroll --server https://cornus.example \
  --identity-file ~/.ssh/id_ed25519 --name laptop --code "$code"
```

注册成功后，代码会轮换。运行时密钥存储在模式为 `0600` 的 `<CORNUS_DATA>/auth/authorized_keys`。对于声明式或多副本安装，请设置以换行分隔的 `CORNUS_AUTHORIZED_KEYS` 和 `CORNUS_AUTH_KEYSTORE=none`；此时注册返回 `409`，并引导操作员使用环境设置。Helm chart 在 `replicas > 1` 时会自动选择 `none`。

将签名器选择信息保存在连接配置文件中:

```sh
cornus config set-context prod --server https://cornus.example \
  --key-auth-identity-file ~/.ssh/id_ed25519 --key-auth-name laptop
cornus config use-context prod
cornus auth keys
```

配置文件只保存路径和公钥指纹，绝不保存私钥内容。普通命令会签发有效期为 1 小时的 `api` 会话，并以私有方式缓存；请求的有效期最长为 24 小时。`CORNUS_TOKEN` 仍具有最高优先级，之后依次为密钥身份验证、kube 身份验证和静态配置文件令牌。一个配置文件不能同时包含 `key-auth` 和 `kube-auth`。

`POST /.cornus/v1/auth/enroll` 和 `POST /.cornus/v1/auth/token` 使用相同的两步握手。第一个未签名请求会返回 `401` 和无状态质询，随后客户端对质询签名并重试同一端点。质询和证明会绑定公钥以及注册或令牌请求的所有字段，因此已签名的请求无法在传输过程中被篡改。启用密钥身份验证时，只有这两个确切路由免身份验证。`GET` 和 `DELETE /.cornus/v1/auth/keys` 需要完全访问凭据。RSA 使用 SHA-2 签名，并拒绝 RSA/SHA-1 和 DSA。删除密钥会阻止签发新会话，而已签发的无状态会话会自然过期。

**另请参阅:** [cornus auth](/zh/cli/auth)、[连接配置](/zh/reference/connection-config)

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

- `--scope api` 是 full credential；`--scope registry:push` 允许 registry 读写；`--scope registry:pull` 仅允许 registry 读取；`--scope caretaker` 限定于 `/.cornus/v1/caretaker/attach`。scope 采用 allowlist 且 fail closed: 未指定以上任一 scope 的 token (包括完全没有 `scope` claim 的 token) 会在所有端点被拒绝，并返回说明原因的 401；`cornus token issue` 也不会签发这样的 token。
- `scope` claim 只有在**你持有签名 key** 时才具有决定权 —— 即 installation secret、`CORNUS_JWT_HS256_SECRET` 或 `CORNUS_JWT_PUBLIC_KEY`。通过 JWKS 验证的 token 属于第三方，因此其 `scope` 本身不授予任何权限；参见下文的 [scope 映射](#将第三方-claim-映射到-cornus-scope)。
- `--sub` 成为下方 policy 的 caller identity。设置时，`--iss` / `--aud` 必须匹配 `CORNUS_JWT_ISSUER` / `CORNUS_JWT_AUDIENCE`。
- Key 类型决定 algorithm (RSA -> RS256，ECDSA -> ES256)；对 public key 绝不接受 HS256，因此该配置对 algorithm confusion 是安全的。

**另请参阅:** [cornus token](/zh/cli/token)

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

**另请参阅:** [cornus token](/zh/cli/token)

## 将第三方 claim 映射到 cornus scope

JWKS 指向的是**别人的** key set: 你可以验证他们的 token，但无法签发。因此这类 token 上的 `scope` claim 是该 issuer 的断言，而不是你的断言 —— 采信它会让任何你信任其证明*身份*的 issuer 同时能给自己授予*权限*，办法是签发 `scope: api`，或者在它自己的词汇里把 "scope" 一词用作完全不同的含义。

因此 cornus 通过你编写的 **scope map** 决定第三方 token 能触及什么。这也是向 Kubernetes ServiceAccount token 授予任何权限的唯一方式 —— 后者没有 `scope` claim，也无法被赋予。

```yaml
# /etc/cornus/scopes.yaml — ordered; first matching rule wins; no match grants nothing.
rules:
  - name: the deploy robot is an operator
    scope: api
    match:
      sub: { prefix: "system:serviceaccount:cornus-system:" }

  - name: CI pushes images
    scope: registry:push
    match:
      aud: { equals: cornus }
      "kubernetes.io/serviceaccount/namespace": { equals: ci }

  - name: verified staff read the registry
    scope: registry:pull
    match:
      email: { suffix: "@example.com" }
      email_verified: { equals: true }
```

```sh
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json \
CORNUS_JWT_SCOPE_MAP=/etc/cornus/scopes.yaml cornus serve
```

- 每条 rule 的 `match` 是**合取** —— 每个 claim 都必须匹配。放宽策略靠增加 rule，收紧策略靠给某条 rule 增加 claim。
- Matcher 有 `equals`、`prefix`、`suffix`、`glob` (`path.Match`，其中 `*` 不跨越 `/`)、`any_of` 和 `contains` (用于 JSON 数组或空格分隔的字符串)。这里刻意**没有正则表达式**: allowlist 中未加锚定的模式，授予的范围会超出作者阅读它时的理解。
- claim 先按**字面名称**查找 (`kubernetes.io/serviceaccount/namespace` 是一个 claim，而不是一条路径)，然后再作为进入嵌套对象的点分路径查找 (`kubernetes.io.pod.name`)。
- 格式错误的 map、未知的 scope、`match` 为空的 rule，或未指定任何测试的 matcher，都会**在启动时终止 server**。一份静默加载失败的策略，比一个拒绝启动的 server 更糟。
- `CORNUS_JWT_DEFAULT_SCOPE=api` 相当于一条 catch-all rule，并追加在 map **之后**，因此显式 rule 仍然具有决定权。当 verifier 接受的每一个 token 确实都是完整凭据时使用它 —— 关键在于这一点现在是被*陈述*的，而不是从缺失的 claim 推断出来的。
- token 自身的 `scope` claim 同样可以像其他 claim 一样参与匹配，因此一个你已刻意配置为签发 cornus scope 的 issuer 可以被显式采信: `match: {iss: {equals: "https://idp.example.com"}, scope: {contains: "registry:pull"}}`

**另请参阅:** [服务器环境变量](/zh/reference/server-env-vars)、[远程集群](/zh/guides/remote-clusters)

## 用第三方 token 换取 Cornus 凭据

`POST /.cornus/v1/auth/exchange` 实现了 [OAuth 2.0 Token Exchange (RFC 8693)](https://www.rfc-editor.org/rfc/rfc8693): 出示一个 Cornus 能验证的 token，换回一个标明其 scope 的短期 Cornus 凭据。该 endpoint 仅在配置了 JWT 或 JWKS verifier 时出现。

```sh
curl -s -X POST https://cornus.example.com/.cornus/v1/auth/exchange \
  -d grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
  -d subject_token_type=urn:ietf:params:oauth:token-type:jwt \
  -d subject_token="$(kubectl create token cornus-client --audience cornus)" \
  -d scope=registry:pull
```

```json
{
  "access_token": "eyJhbGciOi...",
  "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
  "token_type": "Bearer",
  "expires_in": 3600,
  "scope": "registry:pull"
}
```

它与直接路径是同一套策略，只是应用一次而不是每个 request 都应用: subject token 走同样的 verifier，由同样的 scope map 决定，换回的凭据由 Cornus 签发并标明自身的 scope —— 因此 request 路径上无需推断任何东西。

- **`scope` 只能收窄。** 省略它就得到 map 所授予的全部。请求更少就得到更少。请求任何 map 未授予的内容都会以 `invalid_scope` 被拒绝 —— request 参数永远不能超越策略。
- **`caretaker` 和 `peer` 永不签发**，无论 map 是否授予，也无论 client 是否请求收窄到它们。两者都不是 client 凭据: `caretaker` 属于在直接路径上出示它的 sidecar，`peer` 是针对 hub store 中已发布的 key 验证的 server 间凭据。client 不在这两者的任何一侧。
- token 存活一小时，携带 issuer 和 audience `cornus:exchange`，因此在审计记录中可与运维人员签发的 token 区分开。每次 exchange 记录一行，写明 subject、命中的 rule 和签发的 scope —— 每个凭据一条记录，而不是每个 request 一条。
- 委派 (`actor_token`) 会被拒绝，而不是被忽略。

**另请参阅:** [scope 映射](#将第三方-claim-映射到-cornus-scope)

## 启用 mTLS，并从 client cert 派生 identity

提供 TLS 时，Cornus 还可通过 **client certificate** 认证 caller——它是 bearer token 之外的额外方法，并非替代。将 `--tls-client-ca` (或 `CORNUS_TLS_CLIENT_CA`) 指向 PEM CA bundle。

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- 提交的 cert 必须 chain 到 `--tls-client-ca`；其已验证 `Subject.CommonName` 是 identity。提交 cert 仍是**可选**的 (listener 使用 `VerifyClientCertIfGiven`，因此 `/healthz`、`/readyz` 和 bearer-only client 继续工作)，但一旦提交则必须验证。
- 已验证 client cert 是 full credential，并**优先于**同一 request 上的 bearer token。设置 `--tls-client-ca` (或 `CORNUS_TLS_CLIENT_CA`) 本身即开启 auth。

**另请参阅:** [安装](/zh/introduction/installation)

## 按 identity 授权 action

`CORNUS_API_POLICY` 限制哪些 identity 可执行哪些 API action。它是将 identity 映射到允许 action list 的 JSON object；entry 可使用 `"*"` 允许所有 action。

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

| Action | 覆盖范围 |
| --- | --- |
| `deploy` | 创建/删除 deployment 及其 mutating lifecycle/attach action (蕴含 `exec`) |
| `exec` | 在运行中 deployment 内 exec/attach (`exec`-only entry 可提供 shell 但无 deploy 权限) |
| `build` | `POST /.cornus/v1/build` |
| `push` | `/v2/*` 下的 registry write (image push 和 delete) |
| `pull` | registry `GET` / `HEAD`——opt-in: 仅当 rule 显式提及 `pull` 时强制 (`"*"` 不计) |
| `gc` | destructive `POST /.cornus/v1/gc` reclaim endpoint |
| `activity` | `GET /.cornus/v1/activity` (服务器活动的飞行记录) |
| `observe` | 可观测性摄取、查询和 Grafana 代理端点 |
| `tunnel` | 创建和操作公共 ingress 隧道 (`deploy` 也隐含此权限) |

未设置时允许所有内容；一旦配置，caller 必须为 action 被列出 (或 `"*"`)，且**空 identity 被拒绝 (fail closed)**——因此 policy 需要 identifying credential (JWT `sub` 或 mTLS CommonName；opaque static token 与 anonymous caller 被拒绝)。错误 JSON 是 hard startup error。大多数只读端点没有单独的 action gate，但活动日志和可观测性功能会按上表限制。仅当 rule 显式选择启用时，registry pull 才受限制。启用身份验证时，除已注明的健康检查 / 就绪检查和匿名拉取例外外，它仍独立应用于每个端点。

**另请参阅:** [服务器环境变量](/zh/reference/server-env-vars)

## 在保护写入的同时允许匿名 registry pull

保持 push、build 和 deploy 在 auth 后面，但允许任何人 pull image。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- 只打开 `/v2/*` 下的 `GET` / `HEAD`；每个 write verb 仍需 credential。该 flag 接受 `1`/`true`/`yes`/`on`。
- `CORNUS_API_POLICY` 中显式 `pull` rule 优先于此 flag (两者均设置时 startup warning)。没有 `pull` rule 时，registry pull 由 authentication 管理，因此两者不冲突。

**另请参阅:** [镜像仓库和存储](/zh/guides/registry)

## 理解 scoped caretaker credential

每 pod caretaker 只访问 `/.cornus/v1/caretaker/attach`，因此获得**独立 scoped** token，而非 full token。在 auth 下运行 Kubernetes backend 时，与 client auth 一同设置；backend 会自动注入 mount/hub sidecar。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

- Server 仅在 caretaker endpoint 接受 caretaker token，并在 client API 与 registry 上拒绝它，因此从 pod spec 读出的 sidecar credential 无法 deploy、build、exec 或 push。
- 它可为 opaque `CORNUS_CARETAKER_TOKEN`，或 `caretaker`-scoped JWT (`cornus token issue --scope caretaker`)，因此完全没有 static token 的 JWT-only server 仍支持 k8s live mount。要使 token 不进入 pod spec，请将其保存在 Kubernetes Secret 中，并用 `CORNUS_CARETAKER_TOKEN_SECRET` 指向它；sidecar 随后在 runtime 通过 `secretKeyRef` 获取 token。

**另请参阅:** [服务器环境变量](/zh/reference/server-env-vars)
