# Security and authentication

Cornus's HTTP API (`/v2/*`, `/.cornus/v1/*`) ships with **no authentication by
default**. With no auth configured, anyone who can reach the port can push and
pull images, run builds, and create deployments. Run Cornus only on a trusted
network, behind an authenticating reverse proxy, or with the built-in bearer
auth below enabled. Every security control on this page is **opt-in and
zero-cost when off**: with none of the relevant env vars set, the server behaves
exactly as it did before, with no per-request cost.

TLS serving is available in-process with `--tls-cert` / `--tls-key` (or
`CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`), but that provides transport encryption,
not caller authentication.

## How it works

### Bearer authentication

Bearer authentication turns on as soon as at least one verifier is configured.
When enabled, every request needs a valid `Authorization: Bearer <token>` except
`/healthz` and `/readyz` (always open) and, if anonymous pull is enabled,
`GET` / `HEAD` under `/v2/*`. Cornus only **verifies** tokens; it does not mint
them. Three verifier kinds can be combined — a request is accepted if any of them
verifies the token: an opaque shared secret, a symmetric or asymmetric JWT key,
and a JWKS key set.

Optional JWT claim checks are enforced only when set: `CORNUS_JWT_ISSUER` must
match the token `iss`, `CORNUS_JWT_AUDIENCE` must match the token `aud`. `exp`
and `nbf` are always validated with a one-minute leeway, and tokens with
`alg: none` or an unexpected algorithm are rejected. The full env-var list is in
[server env vars](/reference/server-env-vars).

### Caller identity

The identity a caller authenticates as — an mTLS CommonName or a JWT `sub` — is
unified: both feed the same per-identity authorization policy. An opaque static
token (`CORNUS_AUTH_TOKEN`) carries **no** identity and is treated as anonymous.

### Client side

The Cornus CLIs and `pkg/client` read `CORNUS_TOKEN` and send it as
`Authorization: Bearer <token>` on the `/.cornus/v1/*` calls, the archive `PUT`, and the
WebSocket attach handshakes (deploy attach, build, exec):

```sh
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

For external OCI clients hitting `/v2/*` with auth enabled, `cornus push` sends
`CORNUS_TOKEN` as a registry bearer credential. Stock `docker` / `podman` /
`crane` log in with plain `docker login`: the registry accepts HTTP Basic on
`/v2/*` where the password is the token (the static token or a JWT) and the
username is ignored, and its 401 challenge is `Basic realm="cornus"`, so the
standard login flow works with no token service:

```sh
docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"
```

**See also:** [cornus serve](/cli/serve), [server env vars](/reference/server-env-vars)

## Require a static bearer token

Turn on bearer auth with a single opaque shared secret.

```sh
# Server: enforcement turns on as soon as a verifier is configured.
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# Client: sent as Authorization: Bearer <token> on /.cornus/v1/* and /v2/*.
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

- `/healthz` and `/readyz` stay open; every other request needs the token.
- A static token carries **no identity** and is treated as anonymous, so it cannot satisfy a per-identity policy (see below). For stock OCI clients: `docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"`.

**See also:** [cornus serve](/cli/serve)

## Mint JWTs for clients

The server only verifies tokens; use `cornus token issue` to mint the JWTs it accepts, signing with the same material.

```sh
# Symmetric (HS256): the server verifies with the same secret.
export CORNUS_JWT_HS256_SECRET="$(openssl rand -hex 32)"   # >= 32 bytes
cornus token issue --sub ci-bot --scope api --ttl 1h --hs256-secret "$CORNUS_JWT_HS256_SECRET"

# Asymmetric: mint with a private key; the server holds only the public half.
cornus token issue --sub pod-x --scope caretaker --ttl 720h --private-key ./jwt-priv.pem
#   server side: CORNUS_JWT_PUBLIC_KEY=./jwt-pub.pem cornus serve
```

- `--scope api` (or empty) is a full credential; `--scope caretaker` is restricted to `/.cornus/v1/caretaker/attach`.
- `--sub` becomes the caller identity for the policy below. `--iss` / `--aud` must match `CORNUS_JWT_ISSUER` / `CORNUS_JWT_AUDIENCE` when those are set.
- The key type selects the algorithm (RSA -> RS256, ECDSA -> ES256); HS256 is never accepted against a public key, so the setup is algorithm-confusion-safe.

**See also:** [cornus token](/cli/token)

## Verify tokens against a JWKS endpoint

Verify asymmetric JWTs against a published key set, with `kid` selection and rotation.

```sh
# Remote JWKS (cached, refetched on TTL and, rate-limited, on an unknown kid):
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve

# Local JWKS file (hot-reloaded on change):
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
```

- Only asymmetric algorithms are accepted; the token's `kid` header selects the key. When minting, stamp the matching id with `cornus token issue --kid <id> --private-key key.pem ...`.
- `exp` / `nbf` are always validated (one-minute leeway); `alg: none` or an unexpected algorithm is rejected.

**See also:** [cornus token](/cli/token)

## Enable mTLS and derive identity from the client cert

When serving TLS, Cornus can also authenticate callers by a **client
certificate** — an additional method alongside bearer tokens, not a replacement.
Point `--tls-client-ca` (or `CORNUS_TLS_CLIENT_CA`) at a PEM CA bundle.

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- A presented cert must chain to `--tls-client-ca`; its verified `Subject.CommonName` is the identity. Presenting a cert stays **optional** (the listener uses `VerifyClientCertIfGiven`, so `/healthz`, `/readyz`, and bearer-only clients still work), but a presented cert must verify.
- A verified client cert is a full credential and takes **precedence** over any bearer token on the same request. Setting `--tls-client-ca` (or `CORNUS_TLS_CLIENT_CA`) turns auth on by itself.

**See also:** [installation](/introduction/installation)

## Authorize actions per identity

`CORNUS_API_POLICY` restricts which identities may perform which API actions. It
is a JSON object mapping identity to a list of allowed actions; an entry may use
`"*"` to allow all actions.

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

| Action | Covers |
| --- | --- |
| `deploy` | create/delete a deployment plus its mutating lifecycle/attach actions (implies `exec`) |
| `exec` | exec/attach into a running deployment (an `exec`-only entry grants a shell without deploy rights) |
| `build` | `POST /.cornus/v1/build` |
| `push` | registry writes under `/v2/*` (image push and delete) |
| `pull` | registry `GET` / `HEAD` — opt-in: enforced only once a rule mentions `pull` explicitly (`"*"` does not count) |
| `gc` | the destructive `POST /.cornus/v1/gc` reclamation endpoint |

Unset allows everything; once configured, a caller must be listed for the action
(or `"*"`), and an **empty identity is denied (fail closed)** — so the policy
requires an identifying credential (a JWT `sub` or an mTLS CommonName; the opaque
static token and anonymous callers are denied). Malformed JSON is a hard startup
error. Read/GET endpoints are not gated except registry pull, and only when a
rule opts it in.

**See also:** [server env vars](/reference/server-env-vars)

## Allow anonymous registry pulls while protecting writes

Keep push, build, and deploy behind auth but let anyone pull images.

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- This opens only `GET` / `HEAD` under `/v2/*`; every write verb still needs a credential. The flag accepts `1`/`true`/`yes`/`on`.
- An explicit `pull` rule in `CORNUS_API_POLICY` wins over this flag (with a startup warning when both are set). With no `pull` rule, registry pull is governed by authentication, so the two do not conflict.

**See also:** [registry and storage](/guides/registry)

## Understand the scoped caretaker credential

The per-pod caretaker only ever reaches `/.cornus/v1/caretaker/attach`, so it is
given a **separate, scoped** token rather than a full one. Set it alongside the
client auth when running the kubernetes backend under auth; the backend injects
it into the mount/hub sidecars automatically.

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

- The server accepts the caretaker token on the caretaker endpoint only and rejects it on the client API and the registry, so a sidecar credential read out of a pod spec cannot deploy, build, exec, or push.
- It can be an opaque `CORNUS_CARETAKER_TOKEN` or a `caretaker`-scoped JWT (`cornus token issue --scope caretaker`), so a JWT-only server (no static token at all) still supports k8s live mounts. To keep it out of the pod spec, store it in a Kubernetes Secret and point at it with `CORNUS_CARETAKER_TOKEN_SECRET`; the sidecar then sources the token via `secretKeyRef` at runtime.

**See also:** [server env vars](/reference/server-env-vars)
