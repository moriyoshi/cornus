# Credentials

Task-oriented recipes for handing a remote workload a caller-minted secret —
cloud credentials, an LLM API key, anything — without baking it into the image,
the spec, or the pod spec. For the model behind them see [credential
brokering](/topics/credentials) and the [deploy spec](/reference/deploy-spec). To
route a workload's outbound traffic through the caller instead, see the
[Egress](/guides/egress) guide.

## Broker a credential into a workload without baking it into the image

Declare a `credentials:` block; the secret is minted on your machine and delivered by the caretaker, never entering the image, spec, or pod spec.

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

- Realized on the **kubernetes** backend over a foreground `cornus deploy --server` session (the client answers fetches for the workload's lifetime, so `--detach` rejects it).
- `deliver[].kind` is `endpoint` (default), `file`, or `env`; a workload may fetch only the credential names its own session declared.

**See also:** [deploy spec](/reference/deploy-spec), [credential brokering](/topics/credentials)

## Proxy an LLM API or inject an API key into a workload

Let a workload call an LLM through a caretaker reverse proxy that injects the auth header, so the key never enters the container.

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

- The `anthropic-proxy` / `openai-proxy` providers set `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`, strip any client-sent auth, and add the real credential per request.
- To inject a plain env var instead, use `backend: env` with `config.var` and an `env`-kind delivery (static, stored in a Kubernetes Secret; prefer `endpoint` / `file` for short-lived secrets).

**See also:** [credential brokering](/topics/credentials), [deploy spec](/reference/deploy-spec)

## Source a credential from AWS STS

Mint short-lived AWS credentials from your own AWS credential chain and surface them in the SDK's expected shape.

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

- `aws-sts` uses your AWS credential chain via STS; it needs the `credaws`-tagged binary and supports modes `auto` / `assume-role` / `session-token` / `passthrough`.
- `provider: aws-imds` renders the credential in the ECS / IMDSv2 shapes so an unmodified AWS SDK picks it up. On loopback (the default) it injects `AWS_CONTAINER_CREDENTIALS_FULL_URI` (the ECS credential endpoint); `wellKnown: true` instead binds `169.254.169.254` (IMDSv2) in the pod with no env var (needs `NET_ADMIN`).

**See also:** [credential brokering](/topics/credentials), [deploy spec](/reference/deploy-spec)
