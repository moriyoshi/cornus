# Security and authentication

Cornus's HTTP API (`/v2/*`, `/.cornus/v1/*`) ships with **no authentication by
default**. With no auth configured, anyone who can reach the port can push and
pull images, run builds, and create deployments. Run Cornus only on a trusted
network, behind an authenticating reverse proxy, or with the built-in bearer
auth below enabled. Every security control on this page is **opt-in and
zero-cost when off**: with none of the relevant env vars set, the server behaves
exactly as it did before, with no per-request cost.

That applies to a plain `cornus serve`, because the default listen address is
**`:5000` — every interface**. It has to be: a containerized caretaker dials
back to the server for client-local 9P mounts, client-side egress, credential
delivery, and workload telemetry, and the host's `127.0.0.1` is not the
container's, so a loopback-only bind would break those features. Treat the bind
and turning on auth as one decision.

If you do **not** need workloads to reach the server, restrict it to this
machine with `--addr 127.0.0.1:5000` (`CORNUS_ADDR=127.0.0.1:5000`); the server
notes the restriction in its startup log. See
[Listen address and exposure](/cli/serve#listen-address-and-exposure).

TLS serving is available in-process with `--tls-cert` / `--tls-key` (or
`CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`), but that provides transport encryption,
not caller authentication.

## How it works

### Bearer authentication

Bearer authentication turns on as soon as at least one client verifier is configured.
When enabled, every request needs a valid `Authorization: Bearer <token>` except
`/healthz` and `/readyz` (always open) and, if anonymous pull is enabled,
`GET` / `HEAD` under `/v2/*`. Cornus verifies client tokens and exposes no
general HTTP token-minting service. Three verifier kinds can be combined — a
request is accepted if any of them verifies the token: an opaque shared secret,
a symmetric or asymmetric JWT key, and a JWKS key set.

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

When client authentication is enabled, Cornus also creates an independent
installation signing key (`CORNUS_INSTALLATION_SECRET`, or
`$CORNUS_DATA/installation.key`) for its own dataplane. It mints 15-minute
`registry:push` credentials for in-process builds and 15-minute
`registry:pull` credentials for dockerhost, containerd, bare, and Incus pulls.
Kubernetes receives a namespace-scoped `cornus-registry-pull` pull Secret with
a 12-hour credential refreshed every four hours. Credentials are emitted only
for the server's own advertised/loopback registry hosts and never for a
third-party registry. The installation key does not enable client auth, is not
accepted as a client credential, and is never exposed by an HTTP endpoint.

The Helm chart provisions this installation key as a shared Secret and retains
the live value across upgrades with Helm `lookup`. Consequently a bare
`helm template` (which cannot query a cluster) displays a fresh random value on
each render; that is cosmetic render output, not rotation of an installed key.

### Inter-replica forwarding

When client authentication is enabled together with a distributed hub store
(`CORNUS_HUB_REDIS` or `CORNUS_HUB_STORE=kube`), each server replica creates an
ECDSA P-256 private key at `$CORNUS_DATA/peer.key` with mode `0600`. Only the
public half is published through the hub store. Redis keeps it under the
replica's heartbeat TTL; the Kubernetes store makes it owned by the replica's
Lease, so a departed replica's key follows the same liveness lifecycle as its
routing records. Auth-off and in-memory single-replica servers create no peer
key.

For `/.cornus/v1/hub/forward`, `/.cornus/v1/mount/forward`, and
`/.cornus/v1/cred/forward`, a sender without `CORNUS_AUTH_TOKEN` mints a cached
five-minute ES256 JWT. Its scope is `peer`, and its `sub` and `kid` are both the
replica id. The receiver resolves `kid` through the hub store, accepts ES256
only, and requires `sub == kid`. The `peer` scope reaches exactly those three
forward endpoints; it cannot call the client API, read or write the registry,
or attach as a caretaker.

An explicit `CORNUS_AUTH_TOKEN` retains absolute precedence and is sent
unchanged. This preserves mixed-version rolling upgrades, because an older
replica does not understand the `peer` scope. The peer credential is therefore
active only in JWT-, JWKS-, or mTLS-only multi-replica configurations that had
no inter-replica credential before.

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

## Use SSH public-key sessions

SSH-key authentication stores only authorized public keys on the server. A
client signs a server-issued, purpose-bound challenge and receives a short-lived
JWT with a fixed Cornus SSH-key issuer and audience. The server's installation
secret signs these sessions; operator JWT keys remain independent.

Enable a writable single-server key store, retrieve its local enrollment code,
and enroll a client key:

```sh
# Server (the default unauthenticated posture does not change unless this is set):
CORNUS_AUTH_KEYSTORE=file cornus serve

# Run locally as the server uid, or through ssh/docker exec/kubectl exec:
code=$(cornus auth enrollment-code)

# Client:
cornus auth enroll --server https://cornus.example \
  --identity-file ~/.ssh/id_ed25519 --name laptop --code "$code"
```

Successful enrollment rotates the code. Runtime keys live in
`<CORNUS_DATA>/auth/authorized_keys` with mode `0600`. For declarative or
multi-replica installations, set newline-separated `CORNUS_AUTHORIZED_KEYS` and
`CORNUS_AUTH_KEYSTORE=none`; enrollment then returns `409` and directs operators
to the environment setting. The Helm chart selects `none` automatically when
`replicas > 1`.

Store the signer selector in a connection profile:

```sh
cornus config set-context prod --server https://cornus.example \
  --key-auth-identity-file ~/.ssh/id_ed25519 --key-auth-name laptop
cornus config use-context prod
cornus auth keys
```

The profile stores a path and public fingerprint, never private-key material.
Ordinary commands mint one-hour `api` sessions and cache them privately; the
maximum requested lifetime is 24 hours. `CORNUS_TOKEN` still has highest
precedence, followed by key auth, kube auth, and a static profile token.
`key-auth` and `kube-auth` cannot be combined in one profile.

`POST /.cornus/v1/auth/enroll` and `POST /.cornus/v1/auth/token` use the same
two-step handshake: the first unsigned request returns `401` with a stateless
challenge, then the client signs it and retries the same endpoint. The challenge
and proof bind the public key and all enrollment or token request fields, so a
signed request cannot be altered in transit. Only those two exact routes are
exempt when key auth is enabled. `GET` and `DELETE /.cornus/v1/auth/keys`
require a full credential. RSA uses SHA-2 signatures; RSA/SHA-1 and DSA are
rejected. Deleting a key blocks new sessions, while already-issued stateless
sessions expire naturally.

**See also:** [cornus auth](/cli/auth), [Connection config](/reference/connection-config)

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

- `--scope api` is a full credential; `--scope registry:push` grants registry reads and writes; `--scope registry:pull` grants registry reads only; `--scope caretaker` is restricted to `/.cornus/v1/caretaker/attach`. Scopes are an allowlist and fail closed: a token that names none of these — including one carrying no `scope` claim at all — is refused everywhere with a 401 explaining why, and `cornus token issue` will not mint one.
- A `scope` claim decides only when **you hold the signing key** — the installation secret, `CORNUS_JWT_HS256_SECRET`, or `CORNUS_JWT_PUBLIC_KEY`. Tokens verified through a JWKS are a third party's, so their `scope` grants nothing on its own; see [scope mapping](#map-third-party-claims-to-cornus-scopes) below.
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
- A JWKS verifier authenticates callers but grants them nothing by itself. Pair it with a **scope map**, below.

**See also:** [cornus token](/cli/token)

## Map third-party claims to cornus scopes

A JWKS points at **someone else's** key set: you can verify their tokens, but you
cannot mint one. So a `scope` claim on such a token is that issuer's assertion,
not yours — honouring it would let any issuer you trust to prove *identity* also
grant itself *authority*, by emitting `scope: api` or by using the word "scope"
for something else entirely in its own vocabulary.

Cornus therefore decides what a third party's token may reach from a **scope
map** you write. It is also the only way to grant anything to a Kubernetes
ServiceAccount token, which has no `scope` claim and cannot be given one.

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

- Each rule's `match` is a **conjunction** — every claim must match. Widen a
  policy by adding a rule; narrow one by adding a claim to a rule.
- Matchers are `equals`, `prefix`, `suffix`, `glob` (`path.Match`, where `*` does
  not cross `/`), `any_of`, and `contains` (for a JSON array or a
  space-separated string). There is deliberately **no regular expression**: an
  unanchored pattern in an allowlist grants more than its author read it as.
- A claim is looked up by its **literal name first** (`kubernetes.io/serviceaccount/namespace`
  is one claim, not a path), then as a dotted path into nested objects
  (`kubernetes.io.pod.name`).
- A malformed map, an unknown scope, a rule with an empty `match`, or a matcher
  naming no test **stops the server at startup**. A policy that silently failed
  to load is worse than a server that refuses to start.
- `CORNUS_JWT_DEFAULT_SCOPE=api` stands for a single catch-all rule and is
  appended **behind** the map, so explicit rules keep deciding. Use it when every
  token a verifier accepts really is a full credential — the point is that this is
  now *stated*, not inferred from a missing claim.
- A token's own `scope` claim is still matchable like any other claim, so an
  issuer you have deliberately configured to emit cornus scopes can be honoured —
  explicitly: `match: {iss: {equals: "https://idp.example.com"}, scope: {contains: "registry:pull"}}`.

**See also:** [server environment variables](/reference/server-env-vars), [remote clusters](/guides/remote-clusters)

## Exchange a third-party token for a Cornus credential

`POST /.cornus/v1/auth/exchange` implements [OAuth 2.0 Token Exchange
(RFC 8693)](https://www.rfc-editor.org/rfc/rfc8693): present a token Cornus can
verify, receive a short-lived Cornus credential that names its scope. The
endpoint appears only when a JWT or JWKS verifier is configured.

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

It is the same policy as the direct path, applied once instead of per request:
the subject token goes through the same verifiers, the same scope map decides,
and the credential that comes back is Cornus-minted and states its own scope — so
nothing on the request path has to infer anything.

- **`scope` may only narrow.** Omit it and you get exactly what the map granted.
  Ask for less and you get less. Ask for anything the map did not grant and the
  request is refused with `invalid_scope` — a request parameter can never exceed
  policy.
- **`caretaker` and `peer` are never issued**, whether the map grants one or a
  client asks to narrow into one. Both are non-client credentials: `caretaker`
  belongs to the sidecar that presents it on the direct path, and `peer` is a
  server-to-server credential verified against a key published in the hub store. A
  client is on neither side of those.
- Tokens live one hour and carry the issuer and audience `cornus:exchange`, so
  they are distinguishable from operator-issued tokens in an audit trail. Each
  exchange logs one line naming the subject, the rule that matched, and the scope
  issued — one record per credential rather than one per request.
- Delegation (`actor_token`) is refused rather than ignored.

**See also:** [scope mapping](#map-third-party-claims-to-cornus-scopes)

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
| `activity` | `GET /.cornus/v1/activity` (the server activity flight record) |
| `observe` | observability ingest, query, and Grafana-proxy endpoints |
| `tunnel` | create and operate a public ingress tunnel (also implied by `deploy`) |

Unset allows everything; once configured, a caller must be listed for the action
(or `"*"`), and an **empty identity is denied (fail closed)** — so the policy
requires an identifying credential (a JWT `sub` or an mTLS CommonName; the opaque
static token and anonymous callers are denied). Malformed JSON is a hard startup
error. Most read-only endpoints have no separate action gate, but the activity
log and observability surfaces are gated as shown above. Registry pull is gated
only when a rule explicitly opts it in. Authentication, when enabled, still
applies independently to every endpoint except the documented health/readiness
and anonymous-pull exemptions.

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
