# Credentials

Cornus can hand a running workload a secret — cloud credentials, an LLM API key,
anything — **without the secret ever entering the image, the deploy spec, or the
pod spec**. The credential is minted on your machine (from your own local
credentials) and relayed through the server over the live deploy-attach
connection.

How it reaches the container depends on the delivery. On kubernetes a `file` or
`endpoint` delivery is served by the per-pod caretaker sidecar — fetched on
demand, TTL-cached, and refreshed as it nears expiry. On a host backend the
server does all three itself, with no caretaker anywhere: it resolves an `env`
value at deploy time, materializes and binds a `file`, and binds an `endpoint`
listener inside the workload's own network namespace. Its companion feature,
routing a workload's outbound traffic through the caller, is
[client-side egress](/guides/egress).

## How it works

It is declared as a `credentials:` block in the deploy spec — or as
`x-cornus-credentials:` on a Compose service, which takes the same fields (see
[Compose](#from-a-compose-file) below). Every delivery needs a session the client
holds for the workload's lifetime, so `cornus deploy --detach` rejects it on every
backend. Each entry under `sources:` names a client-side **backend** that produces
the secret and one or more **deliveries** that surface it to the container.

Which deliveries work depends on the backend:

| Backend | `env` | `file` | `endpoint` |
| --- | --- | --- | --- |
| `kubernetes` | yes (via a Secret + `secretKeyRef`) | yes | yes |
| `dockerhost` / `podman` / `bare` | yes | yes | yes |
| `podman` (rootless) | yes | yes | yes |
| `containerd` | yes | yes | yes |
| `incus` | yes | no (see below) | yes |

On the host backends **no delivery needs a caretaker**, so neither
`CORNUS_ADVERTISE_URL` nor `CORNUS_AGENT_IMAGE` is required for any of them:

- An **`env`** value is resolved once at deploy time and set in the container's
  environment at create.
- A **`file`** is rendered by the server into a directory under its own data dir
  and bound read-only into the workload, refreshed on the credential's TTL by
  swapping a symlink — the same atomic-write shape Kubernetes uses.
- An **`endpoint`** is a listener the server binds *inside the workload's own
  network namespace*, then serves for the session's lifetime. This is the same
  security model as the kubernetes caretaker rather than a weaker one: the
  workload reaches it on `127.0.0.1` because it shares the namespace, and nothing
  else on the host can reach it at all.

Consequences worth knowing:

- **A file bind covers the file's directory.** A credential at `/creds/db.json`
  binds `/creds`, so anything the image had there is hidden. Give credentials a
  directory of their own. Kubernetes makes the same trade for Secret volumes.
- **An endpoint is bound after the container starts.** There is a brief window at
  startup where the listener is not yet up and a connection is refused. Clients of
  a credential endpoint retry, which is why this is acceptable here and would not
  be for a file. On `dockerhost` the window is unavoidable — Docker creates the
  network namespace when the container starts — while `containerd` and `bare` pin
  the namespace themselves and the window is much shorter.
- **Remote mode declines both.** With `CORNUS_DOCKER_REMOTE=1` (or the containerd
  or bare equivalents) the runtime may be on another machine, where the server's
  paths name nothing and its process ids name nothing — and Docker would create an
  empty bind directory rather than fail. Both kinds are refused there instead of
  silently arriving empty or unserved.
- **A remapping runtime gets the file owned by the ids it will actually read it
  with.** Rootless `podman` runs containers in a user namespace, so a file owned
  by the container-side uid would arrive owned by an id the workload cannot see —
  reported as `nobody`, and unreadable no matter what the mode says. cornus asks
  the runtime for its id map and owns the file accordingly, so a `user: "1000"`
  workload reads its own credential. The data directory becomes traversable
  (`0711` — walkable, not listable) on those runtimes, which is what lets the
  runtime reach the file at all; the secrets stored there stay `0600`.
- **`incus` declines `file`, and the reason is timing rather than permissions.**
  incus records an instance's id map **on the instance**, and a credential file
  has to be written before the instance exists — it reaches the workload as a
  disk device in the create request. The daemon exposes no id-map base of its
  own, so there is nothing to ask beforehand. `env` and `endpoint` work there and
  are the ones to use.

::: warning A host backend has no Secret to hide an `env` delivery in
On kubernetes an `env` delivery is materialized into a Secret and referenced with
`secretKeyRef`, never a pod-spec literal. A host backend has no such indirection —
the value lands in the container's configuration and is readable by anyone who can
talk to the daemon (`docker inspect`). That is inherent to the delivery *kind*, not
to any one backend. Prefer `file` or `endpoint` for short-lived or high-value
secrets where they are available.
:::

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
| `github-cli` | your local `gh auth login` | runs `gh auth token`; `hostname` for GitHub Enterprise, `user` to pick an account |

### Delivery kinds

`deliveries[].kind` defaults to `endpoint`.

- **`endpoint`** — the credential is served from a loopback HTTP endpoint inside
  the workload's network namespace: by the caretaker on kubernetes, by the server
  itself on a host backend. `provider: generic` (default) serves the native contract
  (`GET /credentials/<name>` yielding `{"values":{...},"expiration":"..."}`),
  advertised to the app via `CORNUS_CREDENTIALS_URL` /
  `CORNUS_CREDENTIAL_<NAME>_URL`. `provider: aws-imds` renders the credential in
  the shapes an unmodified AWS SDK expects — see
  [Source a credential from AWS STS](#source-a-credential-from-aws-sts) below.
  The auth-injecting providers (`anthropic-proxy`, `openai-proxy`,
  `github-proxy`) go further still and hold the credential themselves, so the
  container never receives it at all.
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

Declare a `credentials:` block; the secret is minted on your machine and delivered by the caretaker (on kubernetes) or by the server itself (on a host backend), never entering the image, spec, or pod spec.

```yaml
name: app
image: localhost:5000/app:v1
credentials:
  sources:
    - name: db
      backend: static                              # produce the secret on the client
      config: { username: app, password: s3cret }  # non-secret config for other backends
      deliveries:
        - { kind: endpoint, provider: generic }        # GET $CORNUS_CREDENTIALS_URL -> JSON
        - { kind: file, path: /creds/db.json, format: json }
```

- Needs a foreground `cornus deploy --server` session (the client answers fetches for the workload's lifetime, so `--detach` rejects it).
- `deliveries[].kind` is `endpoint` (default), `file`, or `env`; a workload may fetch only the credential names its own session declared.
- All three kinds work on kubernetes and on the host backends; the one gap is `file` on `incus`, which is refused for a reason rather than unimplemented. See the [support matrix](#how-it-works).

**See also:** [deploy spec](/reference/deploy-spec)

## Proxy an LLM API or inject an API key into a workload

The `anthropic-proxy` and `openai-proxy` endpoint providers go one step further
than serving the credential: the caretaker runs a loopback reverse proxy to the
vendor API and **injects the auth header itself**, so the workload calls the LLM
with no key of its own. It sets `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` on the
app — plus a *placeholder* `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`, because SDKs
and CLIs refuse to start when their key variable is missing even though the proxy
is what actually holds the credential — strips any client-sent auth, and adds the
real credential per request. So a
coding-agent workload can ride **your own** Claude Code / Codex login without the
secret ever entering the container.

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliveries:
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
      deliveries:
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

## Give a workload GitHub API access from your own `gh` login

The `github-cli` source runs `gh auth token` on your machine, and the
`github-proxy` endpoint provider injects that token into the workload's GitHub
**REST API** calls — so the container makes authenticated calls while holding no
token at all.

```yaml
credentials:
  sources:
    - name: gh
      backend: github-cli
      ttl: 1h                                # gh reports no expiry; see below
      deliveries:
        - kind: endpoint
          provider: github-proxy             # sets GITHUB_API_URL; injects the header
          # upstream: https://ghe.corp/api/v3   # GitHub Enterprise Server
```

The token is read from wherever `gh` keeps it — the OS keyring on most machines,
so this works where reading `~/.config/gh/hosts.yml` would not. `gh` also honors
`GH_TOKEN` / `GITHUB_TOKEN` (and `GH_ENTERPRISE_TOKEN` for other hosts), so the
same spec works unchanged in CI. Config keys: `hostname` (GitHub Enterprise),
`user` (pick between logged-in accounts), `command`, `timeout`, `key`.

If `gh` is not on your `PATH`, or is installed under another name, set
`CORNUS_GH_BIN` on the machine holding the deploy session — that is where the
token is minted, so that is where the variable is read. It adapts a shared spec
to one machine without editing the spec; an explicit `config.command` is a
deliberate choice by whoever wrote the spec (a wrapper that must not be bypassed,
say) and still wins over it. Precedence: `config.command`, then `CORNUS_GH_BIN`,
then `gh`.

```sh
CORNUS_GH_BIN=/opt/homebrew/bin/gh cornus deploy --server -f app.yaml
```

Set an explicit `ttl:`. `gh auth token` reports no expiry, so the 5-minute
default re-runs `gh` — and possibly touches your keyring — every five minutes per
replica. An hour is plenty for a token with no expiry.

### This is the REST API, not git

`git clone` and `git push` do **not** ride this proxy: git-over-HTTPS goes
straight to `github.com:443` and is unaffected. Nor does the `gh` CLI itself work
against it — `gh` takes a hostname rather than a base URL and always speaks
HTTPS, so it cannot be pointed at the plaintext loopback sidecar. For either
case, deliver the token itself instead and accept that it enters the container:

```yaml
deliveries:
  - { kind: endpoint }                                   # GET $CORNUS_CREDENTIALS_URL -> JSON
  - { kind: file, path: /run/secrets/gh-token, format: raw }
```

### Pointing your client at the proxy

Only `@actions/github` reads `GITHUB_API_URL` on its own. Every other client
needs one line:

| Client | |
| --- | --- |
| Octokit (JS) | `new Octokit({ baseUrl: process.env.GITHUB_API_URL })` |
| PyGithub | `Github(base_url=os.environ["GITHUB_API_URL"])` |
| go-github | `c := github.NewClient(nil); c.BaseURL, _ = url.Parse(os.Getenv("GITHUB_API_URL") + "/")` |

For go-github use `BaseURL` directly, **with a trailing slash** — *not*
`WithEnterpriseURLs`, which appends `api/v3/` to any host that does not look like
`api.github.com`, so a loopback proxy address would become
`https://api.github.com/api/v3/...` and 404.

`GITHUB_GRAPHQL_URL` is set alongside `GITHUB_API_URL` for the default upstream.
It is deliberately **omitted** for a GitHub Enterprise `upstream`: GHES serves
REST under `/api/v3` and GraphQL under the sibling `/api/graphql`, which a single
proxy cannot reach, and advertising a wrong URL would send the client to GHES
with no credential.

No `GITHUB_TOKEN` is set in the container, on purpose — a placeholder there would
be picked up by `gh`, by git credential helpers, and by any script calling
`api.github.com` directly, turning "no credential" into a confusing `401` far
from its cause. If a client insists the variable exist, set a dummy yourself in
the spec's `env:`; the proxy strips whatever the client sends.

### Two things to get right

**Keep `hostname` and `upstream` in step.** The source's `hostname` is resolved
on your machine and the delivery's `upstream` on the deploy path; nothing checks
that they agree. A `github-cli` source with `hostname: ghe.corp` and a default
`upstream` sends a valid GitHub Enterprise credential to `api.github.com`. Always
configure the pair together.

**Mind the scope.** A `gh auth login` token typically carries `repo` (read/write
to every private repository you can reach), `read:org`, and often `workflow`, and
anything running in the pod can use the loopback proxy as you, with no way to
narrow it — a much wider blast radius than an LLM key. A runaway loop also burns
your own rate limit. For anything but a workload you fully trust, mint a
fine-grained PAT and deliver it with `static` / `env` / `exec` instead.

Not covered: `uploads.github.com` (release assets) is a separate host; absolute
URLs inside response *bodies* are not rewritten (`Link` and `Location` headers
are, so pagination and redirects stay on the proxy); and a GitHub Enterprise
instance behind a private CA needs that CA in the caretaker image or via
`SSL_CERT_FILE`.

**See also:** [deploy spec](/reference/deploy-spec)

## From a Compose file

A Compose service declares the same block as `x-cornus-credentials:`, so a whole
stack — the agent, its database, its cache — comes up with one `cornus compose
up` and the agent still rides your own login.

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

- The block is the bare source list above, or the spec's object form (`sources:`
  holding the same list) — a spec's block pastes in unchanged.
- Delivery fields take Compose's snake_case spelling (`well_known`, `env_var`,
  `value_key`); the spec's camelCase spellings work too. A key that is neither is
  an error, not a silently ignored field.
- The declaring service holds a deploy-attach session for its lifetime, and the
  `up` line names the reason (`brokering credentials`). Under
  `cornus compose up -d` the project's background agent holds it — so unlike
  `cornus deploy --detach`, a detached compose `up` supports credentials.
- The host backends do not implement credential delivery: they either refuse the
  deploy or warn and ignore the block.
- A project-level `x-cornus-credentials:` block is the default for every service
  that declares none; a service block overrides it wholesale. Each inheriting
  service holds its own session.

**See also:** [cornus compose](/cli/compose)
