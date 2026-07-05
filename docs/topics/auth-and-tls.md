# Authentication and TLS

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

## Bearer authentication

Bearer authentication turns on as soon as at least one verifier is configured.
When enabled, every request needs a valid `Authorization: Bearer <token>` except
`/healthz` and `/readyz` (always open) and, if anonymous pull is enabled,
`GET` / `HEAD` under `/v2/*`. Cornus only **verifies** tokens; it does not mint
them. Three verifier kinds can be combined — a request is accepted if any of them
verifies the token.

```sh
# 1. Opaque shared secret (level 0, zero dependencies):
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# 2. Symmetric JWT (HS256). Use a secret of at least 32 bytes:
CORNUS_JWT_HS256_SECRET="$(openssl rand -hex 32)" cornus serve

# 3. Asymmetric JWT (RS256 or ES256) verified with a PEM public key. The key
#    type selects the algorithm (RSA -> RS256, ECDSA -> ES256); HS256 is never
#    accepted against a public key (algorithm-confusion-safe):
CORNUS_JWT_PUBLIC_KEY=/etc/cornus/jwt-pub.pem cornus serve

# 4. JWKS with kid selection + rotation, from a file (hot-reloaded) or a URL
#    (cached, refetched on TTL and, rate-limited, on an unknown kid):
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve
```

Optional JWT claim checks are enforced only when set: `CORNUS_JWT_ISSUER` must
match the token `iss`, `CORNUS_JWT_AUDIENCE` must match the token `aud`. `exp`
and `nbf` are always validated with a one-minute leeway, and tokens with
`alg: none` or an unexpected algorithm are rejected. The full env-var list is in
[server env vars](/reference/server-env-vars).

### Minting JWTs

Because the server only verifies, use `cornus token issue` to mint the JWTs it
accepts, signing with the same material the server verifies against. A token's
`scope` decides its reach: `api` (or an empty scope) is a full credential;
`caretaker` is restricted to `/.cornus/v1/caretaker/attach` only.

```sh
# Symmetric (HS256) -- the server verifies with the same secret:
cornus token issue --sub ci-bot --scope api --ttl 1h \
  --hs256-secret "$CORNUS_JWT_HS256_SECRET"

# Asymmetric -- mint with a private key; the server holds only the public key:
cornus token issue --sub pod-x --scope caretaker --ttl 720h \
  --private-key ./jwt-priv.pem      # server: CORNUS_JWT_PUBLIC_KEY=./jwt-pub.pem
```

When minting for a JWKS verifier, stamp the matching key id with
`--kid <id>`. See [`cornus token`](/cli/token) for the full flag set.

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

## mTLS client-certificate identity

When serving TLS, Cornus can also authenticate callers by a **client
certificate** — an additional method alongside bearer tokens, not a replacement.
Point `--tls-client-ca` (or `CORNUS_TLS_CLIENT_CA`) at a PEM CA bundle:

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

A presented client certificate must chain to that CA; its verified
`Subject.CommonName` becomes the caller identity. Presenting a cert stays
**optional** (the listener uses `VerifyClientCertIfGiven`), so `/healthz`,
`/readyz`, and bearer-only clients keep working without one — but a cert that is
presented must verify. A verified client certificate is a **full credential** and
takes **precedence** over any bearer token on the same request. Configuring
`CORNUS_TLS_CLIENT_CA` turns auth on by itself.

The identity a caller authenticates as — an mTLS CommonName or a JWT `sub` — is
unified: both feed the same per-identity authorization below. An opaque static
token (`CORNUS_AUTH_TOKEN`) carries **no** identity and is treated as anonymous.

## Per-identity authorization policy

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

## Registry posture and anonymous pull

When auth is enabled, `/v2/*` requires auth for every verb. To allow
unauthenticated pull (`GET` / `HEAD`) while still requiring auth for push/delete:

```sh
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve   # 1/true/yes/on
```

An explicit `pull` rule in `CORNUS_API_POLICY` wins over
`CORNUS_REGISTRY_ANONYMOUS_PULL` (with a startup warning when both are set). With
no rule mentioning `pull`, registry pull is governed by authentication, not the
policy, so the two do not conflict.

## The caretaker credential

The per-pod caretaker only ever reaches `/.cornus/v1/caretaker/attach`, so it is given a
**separate, scoped** token rather than a full one — the server accepts it on the
caretaker endpoint only and rejects it on the client API and the registry, so a
sidecar credential read out of a pod spec cannot deploy, build, exec, or push.
Set it alongside the client auth when running the kubernetes backend under auth;
the backend injects it into the mount/hub sidecars automatically:

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

The scoped caretaker credential can be either an opaque `CORNUS_CARETAKER_TOKEN`
string or a `caretaker`-scoped JWT minted with `cornus token issue --scope
caretaker` — so a JWT-only server (no static token at all) still supports k8s
live mounts. To keep the token out of the pod spec, store it in a Kubernetes
Secret and point Cornus at it with `CORNUS_CARETAKER_TOKEN_SECRET`; the sidecar
then sources the token via `secretKeyRef` at runtime.

See [`cornus serve`](/cli/serve) for the server flags and
[server env vars](/reference/server-env-vars) for the complete configuration
surface.
