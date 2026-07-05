# Securing a server

Cornus's HTTP API (`/v2/*`, `/.cornus/v1/*`) ships with **no authentication by default** — anyone who can reach the port can push, build, and deploy. Every control below is opt-in and zero-cost when off. Run Cornus on a trusted network, behind an authenticating proxy, or with the auth here enabled. For the model in depth see [auth and TLS](/topics/auth-and-tls).

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

**See also:** [auth and TLS](/topics/auth-and-tls), [cornus serve](/cli/serve)

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

**See also:** [cornus token](/cli/token), [auth and TLS](/topics/auth-and-tls)

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

**See also:** [auth and TLS](/topics/auth-and-tls), [cornus token](/cli/token)

## Enable mTLS and derive identity from the client cert

Authenticate callers by a client certificate whose CommonName becomes the caller identity.

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- A presented cert must chain to `--tls-client-ca`; its verified `Subject.CommonName` is the identity. Presenting a cert stays **optional** (bearer-only and probe clients still work), but a presented cert must verify.
- A verified client cert is a full credential and takes **precedence** over any bearer token on the same request. Setting `--tls-client-ca` (or `CORNUS_TLS_CLIENT_CA`) turns auth on by itself.

**See also:** [auth and TLS](/topics/auth-and-tls), [installation](/introduction/installation)

## Authorize actions per identity

Restrict which identities may perform which API actions.

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

- Actions: `deploy` (implies `exec`), `exec`, `build`, `push`, `pull`, `gc`. `"*"` allows all.
- Unset allows everything. Once set, a caller must be listed for the action, and an **empty identity is denied (fail closed)** — so the policy needs an identifying credential (a JWT `sub` or an mTLS CommonName); a static token and anonymous callers are denied. Malformed JSON is a hard startup error.

**See also:** [auth and TLS](/topics/auth-and-tls), [server env vars](/reference/server-env-vars)

## Allow anonymous registry pulls while protecting writes

Keep push, build, and deploy behind auth but let anyone pull images.

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- This opens only `GET` / `HEAD` under `/v2/*`; every write verb still needs a credential.
- An explicit `pull` rule in `CORNUS_API_POLICY` wins over this flag (with a startup warning when both are set). With no `pull` rule, registry pull is governed by authentication, so the two do not conflict.

**See also:** [registry and storage](/guides/registry), [auth and TLS](/topics/auth-and-tls)

## Understand the scoped caretaker credential

The per-pod caretaker (sidecar) reaches only `/.cornus/v1/caretaker/attach`, so give it a separate, scoped token rather than a full one.

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

- The server accepts the caretaker token on the caretaker endpoint only and rejects it on the client API and the registry, so a sidecar credential read out of a pod spec cannot deploy, build, exec, or push.
- It can be an opaque `CORNUS_CARETAKER_TOKEN` or a `caretaker`-scoped JWT (`cornus token issue --scope caretaker`), so a JWT-only server still supports k8s live mounts. To keep it out of the pod spec, store it in a Secret and point at it with `CORNUS_CARETAKER_TOKEN_SECRET`.

**See also:** [auth and TLS](/topics/auth-and-tls), [server env vars](/reference/server-env-vars)
