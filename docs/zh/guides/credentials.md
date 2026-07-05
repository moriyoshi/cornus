# 凭据

以下面向任务的操作方法说明如何向远程工作负载提供调用方签发的 secret——云凭据、LLM API key 或任何其他内容——而不将其写入镜像、spec 或 pod spec。其模型参见[凭据代理](/zh/topics/credentials)和 [deploy spec](/zh/reference/deploy-spec)。如需改为让工作负载的出站流量经调用方路由，请参见 [Egress](/zh/guides/egress) 指南。

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

- 在 **kubernetes** 后端上通过前台 `cornus deploy --server` session 实现（客户端会在工作负载存续期响应 fetch，因此 `--detach` 会拒绝它）。
- `deliver[].kind` 可为 `endpoint`（默认）、`file` 或 `env`；工作负载只能 fetch 自己 session 声明的凭据名称。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)、[凭据代理](/zh/topics/credentials)

## 代理 LLM API 或向工作负载注入 API key

让工作负载通过 caretaker reverse proxy 调用 LLM；该代理注入 auth header，因此 key 永不进入容器。

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional Anthropic-/OpenAI-compatible gateway
```

- `anthropic-proxy` / `openai-proxy` provider 会设置 `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`，移除客户端发送的 auth，并按请求添加真实 credential。
- 如需注入普通 env var，请将 `backend: env` 与 `config.var` 和 `env` kind delivery 结合使用（static，保存在 Kubernetes Secret 中；短生命周期 secret 优先使用 `endpoint` / `file`）。

**另请参阅：**[凭据代理](/zh/topics/credentials)、[deploy spec](/zh/reference/deploy-spec)

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
- `provider: aws-imds` 以 ECS / IMDSv2 形式呈现 credential，未修改的 AWS SDK 可直接取得它。默认 loopback 情况下会注入 `AWS_CONTAINER_CREDENTIALS_FULL_URI`（ECS credential endpoint）；`wellKnown: true` 则在 pod 内无 env var 地绑定 `169.254.169.254`（IMDSv2，需要 `NET_ADMIN`）。

**另请参阅：**[凭据代理](/zh/topics/credentials)、[deploy spec](/zh/reference/deploy-spec)
