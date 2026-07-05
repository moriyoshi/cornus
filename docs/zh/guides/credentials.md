# 凭据

Cornus 可以向正在运行的工作负载提供密钥: 云凭据、LLM API 密钥或其他任何内容，同时**密钥绝不会进入镜像、部署规范或 Pod 规范**。凭据会在你的机器上签发 (使用你本地的凭据)，并通过实时 deploy-attach 连接经服务器中继。

它如何抵达容器取决于交付方式。在 kubernetes 上，`file` 与 `endpoint` 交付由每个 Pod 的 caretaker 边车提供: 按需获取、按 TTL 缓存，并在临近过期时刷新。在主机后端上，服务器自身完成全部三种交付，任何地方都不需要 caretaker: 它在部署时解析 `env` 值，实体化并绑定 `file`，并在工作负载自身的网络命名空间内绑定 `endpoint` 监听器。其配套功能是将工作负载的出站流量通过调用方路由，即[客户端侧出站流量](/zh/guides/egress)。

## 工作原理

它在部署规范中声明为 `credentials:` 块，或在 Compose service 上声明为 `x-cornus-credentials:` (字段完全相同，参见下文的[从 Compose 文件](#从-compose-文件)) 。每种交付方式都需要客户端在工作负载整个生命周期内保持的会话，因此 `cornus deploy --detach` 在所有后端上都会拒绝它。`sources:` 下的每个条目会命名一个客户端侧**后端**以生成密钥，并命名一个或多个将其呈现给容器的**交付方式**。

可用的交付方式取决于后端:

| 后端 | `env` | `file` | `endpoint` |
| --- | --- | --- | --- |
| `kubernetes` | 支持 (通过 Secret 与 `secretKeyRef`) | 支持 | 支持 |
| `dockerhost` / `podman` / `bare` | 支持 | 支持 | 支持 |
| `podman` (rootless) | 支持 | 支持 | 支持 |
| `containerd` | 支持 | 支持 | 支持 |
| `incus` | 支持 | 不支持 (见下文) | 支持 |

在主机后端上，**任何交付方式都不需要 caretaker**，因此它们都不需要 `CORNUS_ADVERTISE_URL` 或 `CORNUS_AGENT_IMAGE`:

- **`env`** 的值在部署时解析一次，并在创建容器时写入环境变量。
- **`file`** 由服务器渲染到自身数据目录下的一个目录中，以只读方式绑定进工作负载，并按凭据的 TTL 通过替换符号链接来刷新，这与 Kubernetes 采用的原子写入形式相同。
- **`endpoint`** 是服务器在*工作负载自身的网络命名空间内*绑定的监听器，并在会话的整个生命周期内由服务器提供。这与 kubernetes caretaker 是同一套安全模型，而非更弱的替代: 工作负载能通过 `127.0.0.1` 访问它，是因为二者共享该命名空间，而主机上的其他任何东西都完全无法访问。

有几点值得了解:

- **文件绑定覆盖的是该文件所在的目录。** 位于 `/creds/db.json` 的凭据会绑定 `/creds`，因此镜像原本放在那里的内容会被遮蔽。请为凭据准备专用目录。Kubernetes 的 Secret 卷也做了同样的取舍。
- **`endpoint` 在容器启动之后才绑定。** 启动初期存在一个短暂窗口，此时监听器尚未就绪，连接会被拒绝。凭据端点的客户端会重试，因此这在此处可以接受，而对文件则不可接受。在 `dockerhost` 上这个窗口无法避免 (Docker 在容器启动时才创建网络命名空间) ; `containerd` 与 `bare` 自行固定命名空间，窗口要短得多。
- **remote 模式会同时拒绝两者。** 设置 `CORNUS_DOCKER_REMOTE=1` (以及 containerd 与 bare 的对应设置) 时，运行时可能位于另一台机器，服务器的路径与进程 ID 在那里都不指向任何东西，而 Docker 会创建一个空目录而不是报错，因此这里会直接拒绝，而不是让凭据悄无声息地以空内容送达或无人提供。
- **对会重映射 ID 的运行时，文件会以真正读取它的那组 ID 来拥有。** rootless 的 `podman` 在用户命名空间中运行容器，因此以容器侧 uid 拥有的文件到达时会归一个工作负载看不到的 ID 所有 (显示为 `nobody`，无论权限位如何都读不了) 。cornus 会向运行时索取其 ID 映射并据此设置所有者，因此 `user: "1000"` 的工作负载也能读取自己的凭据。在这类运行时上，数据目录会变为可遍历 (`0711` — 可穿过但不可列出) ，而其中存放的密钥仍为 `0600`。
- **`incus` 拒绝 `file`，原因是时序而非权限。** incus 把实例的 ID 映射记录在**实例本身**上，而凭据文件必须在实例存在之前写出 — 它是作为创建请求中的 disk 设备送达工作负载的。守护进程本身并不暴露 ID 映射基点，因此事先无处可问。在 `incus` 上应使用可用的 `env` 与 `endpoint`。

::: warning 主机后端没有可用于隐藏 `env` 交付的 Secret
在 kubernetes 上，`env` 交付会实体化为 Secret 并通过 `secretKeyRef` 引用，绝不会成为 pod-spec 字面量。主机后端没有这种间接层，该值会进入容器配置，任何能与守护进程通信的人都可以读取 (`docker inspect`) 。这是交付*种类*本身的固有性质，而非某个后端的实现问题。对于短生命周期或高价值的密钥，在可用之处应优先选择 `file` 或 `endpoint`。
:::

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
| `github-cli` | 你本地的 `gh auth login` | 运行 `gh auth token`；GitHub Enterprise 用 `hostname`，选择账号用 `user` |

### 交付类型

`deliveries[].kind` 默认为 `endpoint`。

- **`endpoint`**: caretaker 从回环 HTTP 端点提供凭据。`provider: generic` (默认值) 提供原生协议 (`GET /credentials/<name>` 返回 `{"values":{...},"expiration":"..."}`)，并通过 `CORNUS_CREDENTIALS_URL` / `CORNUS_CREDENTIAL_<NAME>_URL` 向应用公布。`provider: aws-imds` 会以未修改的 AWS SDK 所期望的格式渲染凭据，见下方[从 AWS STS 获取凭据](#从-aws-sts-获取凭据)。注入认证信息的 provider (`anthropic-proxy`、`openai-proxy`、`github-proxy`) 更进一步，自行持有凭据，因此容器根本不会收到它。
- **`file`**: 将内容写入共享卷中的 `path:`，`format:` 可为 `json` (默认)、`env` (`KEY=VALUE` 行)、`raw` (单个值) 或 `aws-credentials` (ini profile)。以 `0600` 权限写入。
- **`env`**: 向应用容器注入 `envVar:`。该值在部署时获取一次，并存储在由 `secretKeyRef` 引用的 Kubernetes Secret 中 (因此不是 pod-spec 字面量)，但它是静态的 (不会刷新) 且存在 etcd 中。对于短期或绝不应实体化的密钥，应优先使用 `endpoint` / `file`。

### 信任

密钥会通过实时会话按每次获取响应，绝不会包含在规范或线路控制帧中。工作负载只能获取其自身部署会话所声明的凭据名称: 会话 id 是不可猜测的能力令牌，会在服务器中继处检查一次、在 caretaker 中再次检查。认证代理会在注入真实凭据前移除客户端提供的认证信息，因此工作负载既无法读取原始密钥，也无法伪造它。

**另请参阅:** [deploy spec](/zh/reference/deploy-spec)

## 将凭据代理给工作负载，而不写入镜像

声明 `credentials:` block；secret 在您的机器上签发并由 caretaker 交付，绝不进入镜像、spec 或 pod spec。

```yaml
name: app
image: localhost:5000/app:v1
credentials:
  sources:
    - name: db
      backend: static                              # 在客户端生成密钥
      config: { username: app, password: s3cret }  # 供其他后端使用的非密钥配置
      deliveries:
        - { kind: endpoint, provider: generic }        # GET $CORNUS_CREDENTIALS_URL -> JSON
        - { kind: file, path: /creds/db.json, format: json }
```

- 需要前台 `cornus deploy --server` session (客户端会在工作负载存续期响应 fetch，因此 `--detach` 会拒绝它)。
- `deliveries[].kind` 可为 `endpoint` (默认)、`file` 或 `env`；工作负载只能 fetch 自己 session 声明的凭据名称。
- 上面的 `endpoint` 交付目前**仅限 kubernetes**; `file` 与 `env` 在主机后端上同样可用。参见[支持矩阵](#工作原理)。

**另请参阅:** [deploy spec](/zh/reference/deploy-spec)

## 代理 LLM API 或向工作负载注入 API key

`anthropic-proxy` 和 `openai-proxy` 端点提供方比单纯提供凭据更进一步: caretaker 运行一个指向供应商 API 的回环反向代理，并**自行注入认证请求头**，因此工作负载调用 LLM 时无需持有自己的密钥。它会在应用上设置 `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`，同时设置一个*占位*的 `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` (因为即使真正持有凭据的是代理，SDK 和 CLI 在缺少各自的 key 环境变量时仍会拒绝启动) ，去除客户端发送的任何认证信息，并在每个请求中添加真实凭据。因此，编码代理工作负载可以使用**你自己的** Claude Code / Codex 登录，而密钥从不进入容器。

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # 或: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliveries:
        - kind: endpoint
          provider: anthropic-proxy         # 设置 ANTHROPIC_BASE_URL 并注入请求头
          # upstream: https://my-gateway    # 可选: Azure OpenAI、本地网关或 mock
```

- `upstream` 使代理指向任意兼容 Anthropic 或 OpenAI 的网关，而不是供应商默认端点 (`https://api.anthropic.com` / `https://api.openai.com`)。
- 如需注入普通 env var，请将 `backend: env` 与 `config.var` 和 `env` kind delivery 结合使用 (static，保存在 Kubernetes Secret 中；短生命周期 secret 优先使用 `endpoint` / `file`)。

### API 密钥和 OAuth token

代理会透明处理两种凭据格式，因此无需改变工作负载，既可使用普通 API 密钥，也可使用 OAuth 登录 token:

- **API 密钥**会在供应商的常规密钥请求头中发送 (Anthropic 使用 `x-api-key`)。
- **OAuth token**，例如通过 `claude` / `ant auth login` 登录获取的 `sk-ant-oat...` token，会作为 `Authorization: Bearer <token>` 发送，并带有 Anthropic API 对 OAuth bearer token 所需的 `anthropic-beta: oauth-2025-04-20` 请求头。代理按以下顺序选取凭据值: `oauth_token` (强制 OAuth)、`api_key` (强制 API-key)，否则使用 `value` / `token`。

`anthropic` / `claude-code` / `codex` 源后端会读取你的本地登录存储，并在短期 OAuth access token 临近过期时**刷新它** (codex 读取 ChatGPT 登录的 `tokens.access_token`，必要时回退到 API 密钥)，因此长时间运行的代理无需你重新认证便可继续工作，同时 token 仍不会进入容器。

**另请参阅:** [deploy spec](/zh/reference/deploy-spec)

## 从 AWS STS 获取凭据

从您自身 AWS credential chain 签发短期 AWS credential，并以 SDK 预期形式提供。

```yaml
credentials:
  sources:
    - name: aws
      backend: aws-sts
      config: { role_arn: arn:aws:iam::123456789012:role/app, region: us-east-1 }
      deliveries:
        - { kind: endpoint, provider: aws-imds, wellKnown: true }
        - { kind: file, path: /root/.aws/credentials, format: aws-credentials }
```

- `aws-sts` 通过 STS 使用您的 AWS credential chain；需要带 `credaws` tag 的 binary，支持 `auto` / `assume-role` / `session-token` / `passthrough` 模式。

`aws-imds` 端点提供方会将代理的凭据渲染为 AWS SDK 已会查找的格式，因此**未修改的** SDK 无需代码或应用改动即可获取它。该适配器是纯 HTTP，自身不依赖 AWS SDK，并通过一个端点响应两种格式:

- **ECS 容器凭据**: `GET /creds` 返回 `{AccessKeyId, SecretAccessKey, Token, Expiration}`。
- **EC2 IMDSv2**: 先 `PUT /latest/api/token`，然后 `GET /latest/meta-data/iam/security-credentials/<role>` (列表公布一个合成角色 `cornus`)。IMDSv1 客户端只需跳过 token 步骤。

SDK 如何访问它取决于 `wellKnown`:

| `wellKnown` | 绑定 | SDK 的发现方式 | 所需条件 |
| --- | --- | --- | --- |
| `false` (默认) | 回环地址 | Cornus 注入 `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://<loopback>/creds`，这是 AWS SDK 遵从的标准 ECS 凭据环境变量。 | 无额外要求 |
| `true` | Pod netns 中的链路本地地址 `169.254.169.254:80` | SDK 内建的 IMDSv2 路径:** 完全不需要环境变量**，与真实 EC2 实例一致。 | caretaker 需要 `NET_ADMIN` |

这是一种交付**适配器**，并非需要运行的通用元数据服务: 它仅为工作负载的会话提供这一个代理凭据。GCP / Azure 元数据适配器也可通过同一机制接入。

**另请参阅:** [deploy spec](/zh/reference/deploy-spec)

## 用你自己的 `gh` 登录给工作负载 GitHub API 访问权限

`github-cli` 源在你的机器上运行 `gh auth token`，`github-proxy` 端点 provider 把这个 token 注入到工作负载的 GitHub **REST API** 调用中，于是容器在完全不持有 token 的情况下发出已认证的请求。

```yaml
credentials:
  sources:
    - name: gh
      backend: github-cli
      ttl: 1h                                # gh 不报告过期时间，见下文
      deliveries:
        - kind: endpoint
          provider: github-proxy             # 设置 GITHUB_API_URL 并注入请求头
          # upstream: https://ghe.corp/api/v3   # GitHub Enterprise Server
```

token 从 `gh` 实际保存它的地方读取——在多数机器上是操作系统的密钥环，因此在直接读取 `~/.config/gh/hosts.yml` 行不通的场景下这种方式仍然有效。`gh` 同样尊重 `GH_TOKEN` / `GITHUB_TOKEN` (其他主机则是 `GH_ENTERPRISE_TOKEN`) ，所以同一份 spec 在 CI 中也无需改动。配置键: `hostname` (GitHub Enterprise) 、`user` (在多个已登录账号间选择) 、`command`、`timeout`、`key`。

如果 `gh` 不在你的 `PATH` 中，或者以别的名字安装，请在持有 deploy session 的机器上设置 `CORNUS_GH_BIN`——token 是在那台机器上签发的，因此这个变量也在那里读取。它的用途是在不修改共享 spec 的前提下适配某一台机器；显式的 `config.command` 是写 spec 的人做出的刻意选择 (比如一个不容绕过的 wrapper) ，因此优先级更高。优先级顺序: `config.command`，然后 `CORNUS_GH_BIN`，最后 `gh`。

```sh
CORNUS_GH_BIN=/opt/homebrew/bin/gh cornus deploy --server -f app.yaml
```

请显式设置 `ttl:`。`gh auth token` 不报告过期时间，因此默认的 5 分钟会让每个副本每 5 分钟就重新运行一次 `gh`，并可能触碰你的密钥环。对于没有过期时间的 token，1 小时足够了。

### 这是 REST API，不是 git

`git clone` 和 `git push` **不会**经过这个代理: git over HTTPS 直接连往 `github.com:443`，不受影响。`gh` CLI 本身也无法配合它工作——`gh` 接受的是主机名而非 base URL，并且始终使用 HTTPS，无法指向明文的回环 sidecar。这两种情况都应改为直接交付 token 本身，并接受它会进入容器:

```yaml
deliveries:
  - { kind: endpoint }                                   # GET $CORNUS_CREDENTIALS_URL -> JSON
  - { kind: file, path: /run/secrets/gh-token, format: raw }
```

### 让你的客户端指向代理

只有 `@actions/github` 会自行读取 `GITHUB_API_URL`。其他客户端都需要加一行:

| 客户端 | |
| --- | --- |
| Octokit (JS) | `new Octokit({ baseUrl: process.env.GITHUB_API_URL })` |
| PyGithub | `Github(base_url=os.environ["GITHUB_API_URL"])` |
| go-github | `c := github.NewClient(nil); c.BaseURL, _ = url.Parse(os.Getenv("GITHUB_API_URL") + "/")` |

go-github 请直接设置 `BaseURL`，并且**带结尾斜杠**——不要用 `WithEnterpriseURLs`，它会给任何看起来不像 `api.github.com` 的主机追加 `api/v3/`，于是回环代理地址会变成 `https://api.github.com/api/v3/...` 并返回 404。

在默认 upstream 下，`GITHUB_GRAPHQL_URL` 会与 `GITHUB_API_URL` 一同设置。对 GitHub Enterprise 的 `upstream` 则被刻意**省略**: GHES 在 `/api/v3` 下提供 REST，而 GraphQL 在同级的 `/api/graphql` 下，单个代理无法同时到达；公布一个错误的 URL 只会让客户端不带凭据地直连 GHES。

容器里不会设置 `GITHUB_TOKEN`，这是刻意的: 占位值会被 `gh`、git 凭据 helper 以及任何直接调用 `api.github.com` 的脚本取用，从而把"没有凭据"变成一个远离根因的、令人困惑的 `401`。如果某个客户端坚持要求该变量存在，请自行在 spec 的 `env:` 中设一个哑值；代理会去除客户端发送的任何内容。

### 两个需要注意的点

**让 `hostname` 与 `upstream` 保持一致。** 源的 `hostname` 在你的机器上解析，交付的 `upstream` 在部署路径上解析，没有任何机制检查两者是否匹配。把 `hostname: ghe.corp` 的 `github-cli` 源与默认 `upstream` 搭配，会把一份有效的 GitHub Enterprise 凭据发往 `api.github.com`。这两项务必成对配置。

**注意 scope。** `gh auth login` 的 token 通常带有 `repo` (对你能访问的所有私有仓库的读写权限) 、`read:org`，往往还有 `workflow`，而 pod 中运行的任何东西都能通过回环代理以你的身份行事，且无法收窄。这比一个 LLM key 的影响范围大得多。失控的循环还会消耗你自己的速率限额。除非是你完全信任的工作负载，否则请签发 fine-grained PAT 并改用 `static` / `env` / `exec` 交付。

未覆盖的部分: `uploads.github.com` (release 资源) 是另一个主机；响应*正文*中的绝对 URL 不会被重写 (`Link` 和 `Location` 请求头会被重写，因此分页和重定向仍留在代理上) ；私有 CA 后面的 GitHub Enterprise 实例需要把该 CA 放进 caretaker 镜像或通过 `SSL_CERT_FILE` 提供。

**另请参阅:** [deploy spec](/zh/reference/deploy-spec)

## 从 Compose 文件

Compose service 用 `x-cornus-credentials:` 声明同一个块，于是整个 stack——agent、它的数据库、它的缓存——用一条 `cornus compose up` 就能起来，而 agent 依然搭乘你自己的登录。

```yaml
services:
  agent:
    image: localhost:5000/agent:v1
    x-cornus-credentials:
      - name: claude
        backend: claude-code
        deliveries:
          - { kind: endpoint, provider: anthropic-proxy }
  db:
    image: postgres:16
```

- 该块既可以是上面这样的裸 source 列表，也可以是 spec 的对象形式 (用 `sources:` 承载同一个列表) ——spec 中的块可以原样粘贴过来。
- 交付字段采用 Compose 的 snake_case 拼写 (`well_known`、`env_var`、`value_key`) ；spec 的 camelCase 拼写同样有效。两者之外的 key 会报错，而不是被悄悄忽略。
- 声明该块的 service 会在其整个生命周期内持有一个 deploy-attach session。在 `cornus compose up -d` 下由项目的后台 agent 持有——因此与 `cornus deploy --detach` 不同，分离模式的 compose `up` 支持凭据。 `up` 那一行会说明原因 (`brokering credentials`) 。
- 主机后端没有实现凭据交付: 它们要么拒绝该部署，要么发出警告并忽略这个 block。
- 项目级的 `x-cornus-credentials:` block 为每个未自行声明的 service 提供默认值，service 级 block 会整体覆盖它。每个继承它的 service 各自持有一个 session。

**另请参阅:** [cornus compose](/zh/cli/compose)
