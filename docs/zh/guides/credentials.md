# 凭据

Cornus 可以向正在运行的工作负载提供密钥：云凭据、LLM API 密钥或其他任何内容，同时**密钥绝不会进入镜像、部署规范或 Pod 规范**。凭据会在你的机器上签发 (使用你本地的凭据)，通过实时 deploy-attach 连接经服务器中继，再由每个 Pod 的 caretaker 边车交付到容器中：按需获取、按 TTL 缓存，并在临近过期时刷新。其配套功能是将工作负载的出站流量通过调用方路由，即[客户端侧出站流量](/zh/guides/egress)。

## 工作原理

它在部署规范中声明为 `credentials:` 块，在前台 `cornus deploy --server` 会话中由 **kubernetes** 后端实现 (客户端须在工作负载整个生命周期内保持连接以响应获取请求，因此 `--detach` 和主机后端会拒绝它)。`sources:` 下的每个条目会命名一个客户端侧**后端**以生成密钥，并命名一个或多个将其呈现给容器的**交付方式**。

只有后端名称和不含密钥的 `config` 会传到服务器；密钥由后端在获取时生成。

### 源后端

每个后端都从调用方自身的环境中签发凭据。

| `backend` | 签发来源 | 说明 |
| --- | --- | --- |
| `static` | 字面 `config` 值 (或文件) | |
| `exec` | `config.command` 的标准输出 | JSON，或 `config.key` 下单个 `raw` 值 |
| `env` | 客户端环境变量 (`config.var`) | 例如 `ANTHROPIC_API_KEY` |
| `aws-sts` | 通过 STS 获取的短期 AWS 凭据，使用你的 AWS 凭据链 | 需要带 `credaws` tag 的二进制文件；模式包括 `auto` / `assume-role` / `session-token` / `passthrough` |
| `anthropic` / `claude-code` / `codex` | 你的本地 LLM 登录 | 临近过期时重新读取短期 token |

### 交付类型

`deliver[].kind` 默认为 `endpoint`。

- **`endpoint`**：caretaker 从回环 HTTP 端点提供凭据。`provider: generic` (默认值) 提供原生协议 (`GET /credentials/<name>` 返回 `{"values":{...},"expiration":"..."}`)，并通过 `CORNUS_CREDENTIALS_URL` / `CORNUS_CREDENTIAL_<NAME>_URL` 向应用公布。`provider: aws-imds` 会以未修改的 AWS SDK 所期望的格式渲染凭据，见下方[从 AWS STS 获取凭据](#从-aws-sts-获取凭据)。
- **`file`**：将内容写入共享卷中的 `path:`，`format:` 可为 `json` (默认)、`env` (`KEY=VALUE` 行)、`raw` (单个值) 或 `aws-credentials` (ini profile)。以 `0600` 权限写入。
- **`env`**：向应用容器注入 `envVar:`。该值在部署时获取一次，并存储在由 `secretKeyRef` 引用的 Kubernetes Secret 中 (因此不是 pod-spec 字面量)，但它是静态的 (不会刷新) 且存在 etcd 中。对于短期或绝不应实体化的密钥，应优先使用 `endpoint` / `file`。

### 信任

密钥会通过实时会话按每次获取响应，绝不会包含在规范或线路控制帧中。工作负载只能获取其自身部署会话所声明的凭据名称：会话 id 是不可猜测的能力令牌，会在服务器中继处检查一次、在 caretaker 中再次检查。认证代理会在注入真实凭据前移除客户端提供的认证信息，因此工作负载既无法读取原始密钥，也无法伪造它。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)

## 将凭据代理给工作负载，而不写入镜像

声明 `credentials:` block；secret 在您的机器上签发并由 caretaker 交付，绝不进入镜像、spec 或 pod spec。

```yaml
name: app
image: localhost:5000/app:v1
credentials:
  sources:
    - name: db
      backend: static                              # produce the secret on the client
      config: { username: app, password: s3cret }  # non-secret config for other backends
      deliver:
        - { kind: endpoint, provider: generic }        # GET $CORNUS_CREDENTIALS_URL -> JSON
        - { kind: file, path: /creds/db.json, format: json }
```

- 在 **kubernetes** 后端上通过前台 `cornus deploy --server` session 实现 (客户端会在工作负载存续期响应 fetch，因此 `--detach` 会拒绝它)。
- `deliver[].kind` 可为 `endpoint` (默认)、`file` 或 `env`；工作负载只能 fetch 自己 session 声明的凭据名称。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)

## 代理 LLM API 或向工作负载注入 API key

`anthropic-proxy` 和 `openai-proxy` 端点提供方比单纯提供凭据更进一步：caretaker 运行一个指向供应商 API 的回环反向代理，并**自行注入认证请求头**，因此工作负载调用 LLM 时无需持有自己的密钥。它会在应用上设置 `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`，去除客户端发送的任何认证信息，并在每个请求中添加真实凭据。因此，编码代理工作负载可以使用**你自己的** Claude Code / Codex 登录，而密钥从不进入容器。

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional: Azure OpenAI, an on-prem gateway, a mock
```

- `upstream` 使代理指向任意兼容 Anthropic 或 OpenAI 的网关，而不是供应商默认端点 (`https://api.anthropic.com` / `https://api.openai.com`)。
- 如需注入普通 env var，请将 `backend: env` 与 `config.var` 和 `env` kind delivery 结合使用 (static，保存在 Kubernetes Secret 中；短生命周期 secret 优先使用 `endpoint` / `file`)。

### API 密钥和 OAuth token

代理会透明处理两种凭据格式，因此无需改变工作负载，既可使用普通 API 密钥，也可使用 OAuth 登录 token：

- **API 密钥**会在供应商的常规密钥请求头中发送 (Anthropic 使用 `x-api-key`)。
- **OAuth token**，例如通过 `claude` / `ant auth login` 登录获取的 `sk-ant-oat...` token，会作为 `Authorization: Bearer <token>` 发送，并带有 Anthropic API 对 OAuth bearer token 所需的 `anthropic-beta: oauth-2025-04-20` 请求头。代理按以下顺序选取凭据值：`oauth_token` (强制 OAuth)、`api_key` (强制 API-key)，否则使用 `value` / `token`。

`anthropic` / `claude-code` / `codex` 源后端会读取你的本地登录存储，并在短期 OAuth access token 临近过期时**刷新它** (codex 读取 ChatGPT 登录的 `tokens.access_token`，必要时回退到 API 密钥)，因此长时间运行的代理无需你重新认证便可继续工作，同时 token 仍不会进入容器。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)

## 从 AWS STS 获取凭据

从您自身 AWS credential chain 签发短期 AWS credential，并以 SDK 预期形式提供。

```yaml
credentials:
  sources:
    - name: aws
      backend: aws-sts
      config: { role_arn: arn:aws:iam::123456789012:role/app, region: us-east-1 }
      deliver:
        - { kind: endpoint, provider: aws-imds, wellKnown: true }
        - { kind: file, path: /root/.aws/credentials, format: aws-credentials }
```

- `aws-sts` 通过 STS 使用您的 AWS credential chain；需要带 `credaws` tag 的 binary，支持 `auto` / `assume-role` / `session-token` / `passthrough` 模式。

`aws-imds` 端点提供方会将代理的凭据渲染为 AWS SDK 已会查找的格式，因此**未修改的** SDK 无需代码或应用改动即可获取它。该适配器是纯 HTTP，自身不依赖 AWS SDK，并通过一个端点响应两种格式：

- **ECS 容器凭据**：`GET /creds` 返回 `{AccessKeyId, SecretAccessKey, Token, Expiration}`。
- **EC2 IMDSv2**：先 `PUT /latest/api/token`，然后 `GET /latest/meta-data/iam/security-credentials/<role>` (列表公布一个合成角色 `cornus`)。IMDSv1 客户端只需跳过 token 步骤。

SDK 如何访问它取决于 `wellKnown`：

| `wellKnown` | 绑定 | SDK 的发现方式 | 所需条件 |
| --- | --- | --- | --- |
| `false` (默认) | 回环地址 | Cornus 注入 `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://<loopback>/creds`，这是 AWS SDK 遵从的标准 ECS 凭据环境变量。 | 无额外要求 |
| `true` | Pod netns 中的链路本地地址 `169.254.169.254:80` | SDK 内建的 IMDSv2 路径：**完全不需要环境变量**，与真实 EC2 实例一致。 | caretaker 需要 `NET_ADMIN` |

这是一种交付**适配器**，并非需要运行的通用元数据服务：它仅为工作负载的会话提供这一个代理凭据。GCP / Azure 元数据适配器也可通过同一机制接入。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)
