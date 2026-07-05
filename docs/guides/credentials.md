# Credentials

Cornus can hand a running workload a secret — cloud credentials, an LLM API key,
anything — **without the secret ever entering the image, the deploy spec, or the
pod spec**. The credential is minted on your machine (from your own local
credentials), relayed through the server over the live deploy-attach connection,
and delivered into the container by the per-pod caretaker sidecar — fetched on
demand, TTL-cached, and refreshed as it nears expiry. Its companion feature,
routing a workload's outbound traffic through the caller, is
[client-side egress](/guides/egress).

## How it works

It is declared as a `credentials:` block in the deploy spec, realized on the
**kubernetes** backend over a foreground `cornus deploy --server` session (the
client stays connected for the workload's lifetime to answer fetches, so
`--detach` and the host backends reject it). Each entry under `sources:` names a
client-side **backend** that produces the secret and one or more **deliveries**
that surface it to the container.

Only the backend name and non-secret `config` ever travel to the server; the
secret is produced by the backend at fetch time.

### Source backends

Each backend mints from the caller's own environment.

| `backend` | Mints from | Notes |
| --- | --- | --- |
| `static` | literal `config` values (or a file) | |
| `exec` | stdout of `config.command` | JSON, or a single `raw` value under `config.key` |
| `env` | a client env var (`config.var`) | e.g. `ANTHROPIC_API_KEY` |
| `aws-sts` | short-lived AWS creds via STS, using your AWS credential chain | needs the `credaws`-tagged binary; modes `auto` / `assume-role` / `session-token` / `passthrough` |
| `anthropic` / `claude-code` / `codex` | your local LLM login | short-lived tokens re-read near expiry |

### Delivery kinds

`deliver[].kind` defaults to `endpoint`.

- **`endpoint`** — the caretaker serves the credential from a loopback HTTP
  endpoint. `provider: generic` (default) serves the native contract
  (`GET /credentials/<name>` yielding `{"values":{...},"expiration":"..."}`),
  advertised to the app via `CORNUS_CREDENTIALS_URL` /
  `CORNUS_CREDENTIAL_<NAME>_URL`. `provider: aws-imds` renders the credential in
  the shapes an unmodified AWS SDK expects — see
  [Source a credential from AWS STS](#source-a-credential-from-aws-sts) below.
- **`file`** — materialize to `path:` in a shared volume, `format:` one of
  `json` (default), `env` (`KEY=VALUE` lines), `raw` (a single value), or
  `aws-credentials` (an ini profile). Written `0600`.
- **`env`** — inject `envVar:` into the app container. The value is fetched once
  at deploy time and stored in a Kubernetes Secret referenced via `secretKeyRef`
  (so it is not a pod-spec literal), but it is static (no refresh) and lives in
  etcd — prefer `endpoint` / `file` for short-lived or never-materialized
  secrets.

### Trust

The secret is answered per fetch over the live session and is never in the spec
or the wire control frames. A workload may fetch **only** the credential names
its own deploy session declared — the session id is an unguessable capability,
checked at the server relay and again in the caretaker. The auth proxies strip
client-supplied auth before injecting the real credential, so a workload can
neither read the raw secret nor spoof it.

**See also:** [deploy spec](/reference/deploy-spec)

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

**See also:** [deploy spec](/reference/deploy-spec)

## Proxy an LLM API or inject an API key into a workload

The `anthropic-proxy` and `openai-proxy` endpoint providers go one step further
than serving the credential: the caretaker runs a loopback reverse proxy to the
vendor API and **injects the auth header itself**, so the workload calls the LLM
with no key of its own. It sets `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` on the
app, strips any client-sent auth, and adds the real credential per request. So a
coding-agent workload can ride **your own** Claude Code / Codex login without the
secret ever entering the container.

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

- `upstream` points the proxy at any Anthropic- or OpenAI-compatible gateway instead of the vendor default (`https://api.anthropic.com` / `https://api.openai.com`).
- To inject a plain env var instead, use `backend: env` with `config.var` and an `env`-kind delivery (static, stored in a Kubernetes Secret; prefer `endpoint` / `file` for short-lived secrets).

### API keys and OAuth tokens

The proxy handles both credential shapes transparently, so it works with a plain
API key **or** an OAuth login token with no change to the workload:

- An **API key** is sent in the vendor's normal key header (`x-api-key` for
  Anthropic).
- An **OAuth token** — for example an `sk-ant-oat...` token from a
  `claude` / `ant auth login` sign-in — is sent as `Authorization: Bearer <token>`
  with the `anthropic-beta: oauth-2025-04-20` header the Anthropic API requires for
  OAuth bearer tokens. The proxy picks the credential value in order: `oauth_token`
  (forces OAuth), `api_key` (forces API-key), else `value` / `token`.

The `anthropic` / `claude-code` / `codex` source backends read your local login
store and **refresh the short-lived OAuth access token** as it nears expiry (codex
reads the ChatGPT sign-in's `tokens.access_token`, falling back to an API key), so
a long-running agent keeps working without you re-authenticating — and the token
still never enters the container.

**See also:** [deploy spec](/reference/deploy-spec)

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

The `aws-imds` endpoint provider renders a brokered credential in the shapes AWS
SDKs already look for, so an **unmodified** SDK picks it up with no code and no
app change. The adapter is pure HTTP with no AWS SDK dependency of its own, and it
answers two shapes over one endpoint:

- **ECS container credentials** — `GET /creds` returns
  `{AccessKeyId, SecretAccessKey, Token, Expiration}`.
- **EC2 IMDSv2** — `PUT /latest/api/token`, then
  `GET /latest/meta-data/iam/security-credentials/<role>` (the listing advertises
  a single synthetic role, `cornus`). IMDSv1 clients simply skip the token step.

How the SDK reaches it depends on `wellKnown`:

| `wellKnown` | Binding | How the SDK finds it | Needs |
| --- | --- | --- | --- |
| `false` (default) | loopback | Cornus injects `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://<loopback>/creds`, the standard ECS-credentials env var AWS SDKs honor. | nothing extra |
| `true` | link-local `169.254.169.254:80` in the pod netns | the SDK's built-in IMDSv2 path — **no env var at all**, exactly as on a real EC2 instance | `NET_ADMIN` on the caretaker |

This is a delivery *adapter*, not a general-purpose metadata service you run: it
serves exactly the one brokered credential for the workload's session. The same
mechanism is how GCP / Azure metadata adapters would slot in.

**See also:** [deploy spec](/reference/deploy-spec)
