# Security model

Security is layered and **opt-in**: with nothing configured, Cornus is a
pass-through suitable for local development. Each layer below is enabled by
configuration and **fails closed once enabled** — malformed policy is a hard
startup error, an empty identity is denied where identity is required, and a
misconfigured verifier rejects rather than passes. This page explains the
model; the [security and authentication guide](/guides/security) covers the
setup and hardening recipes.

## Authentication

Authentication is a middleware seam around the whole HTTP surface, wired inside
the telemetry handler so rejected requests are still traced. With no verifier
configured it is a pass-through. When enabled, `/healthz` and `/readyz` stay
open; every other route requires bearer auth, except `GET`/`HEAD` under `/v2/*`
when `CORNUS_REGISTRY_ANONYMOUS_PULL` is set. Verifier configuration is
environment-driven:

| Variable | Method |
|---|---|
| `CORNUS_AUTH_TOKEN` | opaque full-access bearer token, constant-time compared |
| `CORNUS_JWT_HS256_SECRET` | HS256 JWT |
| `CORNUS_JWT_PUBLIC_KEY` | RS256/ES256 JWT from a PEM public key |
| `CORNUS_JWT_JWKS_FILE` / `_URL` | JWKS with `kid` selection and rotation (asymmetric only) |
| `CORNUS_JWT_ISSUER` / `_AUDIENCE` | optional registered-claim checks |
| `CORNUS_CARETAKER_TOKEN` | scoped static token accepted only on the caretaker attach endpoint |

JWT verification binds each key to its allowed algorithm set — `alg: none`,
algorithm confusion, and public-key-as-HMAC are all rejected — and stores the
caller identity on the request context for authorization. The server is
verify-only: token issuance (`cornus token issue`) is an operator/CLI action,
not an HTTP minting endpoint. Kubernetes caretaker sidecars get a **scoped**
credential (valid for the caretaker attach endpoint only), sourced from a
Kubernetes Secret, rather than carrying a full-access token into every pod.

The registry additionally speaks `docker login`: on `/v2/*` (only), HTTP Basic
is accepted with the same credentials as the password — the static token or a
JWT — and the username ignored (`docker login -u token -p $CORNUS_TOKEN`),
feeding the identical verifier chain. The registry's 401 challenge is
`Basic realm="cornus"` rather than `Bearer`: Cornus has no token service, so a
Bearer challenge would send docker to a nonexistent token realm, while Basic
makes stock docker/podman retry with the stored login. Non-registry routes
still challenge with `Bearer`, and a caretaker-scoped credential framed as
Basic is still rejected on the registry.

## TLS and mTLS identity

TLS serving is built into `cornus serve` via `--tls-cert`/`--tls-key`, served
through reloading callbacks that re-read the files when their modification time
advances — so an external rotator (cert-manager, Vault, SPIFFE) can renew a
mounted cert in place with no restart.

mTLS client-cert identity is an additional authentication method: a verified
client certificate is a full credential whose CommonName is the caller
identity, taking precedence over a bearer token. The hub uses this same
authenticated identity, so hub reach/register policy keys on a credential the
spoke cannot forge.

## Authorization

Per-identity API authorization sits on top of authentication as a
configure-to-enforce matrix: `CORNUS_API_POLICY` maps identity to allowed
actions (`build`, `deploy`, `exec`, `push`, `pull`, `gc`). Unset means
allow-all; once configured, a caller must be listed for the requested action,
and an **empty identity is denied** — so enforcement effectively requires a JWT
`sub` or an mTLS CommonName. Pure reads (deploy status, logs, registry pull)
stay open by default, governed by authentication rather than per-identity
authorization. Two refinements:

- **`exec` is its own action.** Exec/attach is allowed if the policy allows
  `exec` *or* `deploy` — deploy implies exec, so the action's value is
  exec-only identities that can shell into a running workload without being
  able to apply or delete one.
- **Registry pull authorization is opt-in.** When any rule explicitly mentions
  the `pull` action (a `"*"` wildcard does not count), registry `GET`/`HEAD`
  require it. An explicit pull policy wins over
  `CORNUS_REGISTRY_ANONYMOUS_PULL` — an anonymous caller carries no identity
  and is denied — and the server warns at startup when both are configured.

Separately, the deploy backends enforce a **workload privilege policy** as
defense in depth: the host backends reject `Privileged` and host bind sources
unless `CORNUS_ALLOW_PRIVILEGED` / `CORNUS_ALLOW_BIND_SOURCES` opt them in, and
the kubernetes backend default-denies user-requested privileged workloads while
allowing the Cornus-owned injected sidecars that genuinely need privilege for
kernel 9P mounts or network redirection.

## Trust boundaries

Several boundaries documented with their subsystems are worth collecting in one
place:

- **The remote-build export is read-only and confined.** A remote builder gets
  9P access to exactly the context, dockerfile, and named-context directories —
  no `..`, no symlink escape, no writes, and `.dockerignore` is enforced before
  bytes leave the caller. See
  [the build engine](/architecture/build-engine#the-trust-boundary).
- **Session ids are capabilities.** A deploy-attach session id is unguessable
  and travels inside authenticated streams, never in URLs; the mount relay
  publishes only its digest.
- **Egress policy is re-evaluated at every hop.** The caretaker, the server,
  and the client each check the routing policy, so a compromised pod cannot
  upgrade its own routing; sessionless egress is honored only for the
  operator-gated gateway route. See
  [client-side egress](/architecture/caretaker#client-side-egress).
- **Hub policy keys on verified identity.** Under mTLS a spoke's identity comes
  from the client certificate, not its own declaration. See
  [the hub](/architecture/networking#discovery-and-policy).
- **The in-pod Docker endpoint requires an explicit operator grant.** The
  `docker` caretaker role is enabled only when a dedicated client-scoped token
  Secret is configured, because it grants the workload deploy-engine access.
  See [the Docker endpoint](/architecture/caretaker#the-docker-endpoint).

## Related pages

- [Security and authentication](/guides/security) — configuring every verifier
  and TLS mode, plus hardening recipes.
- [cornus token](/cli/token) — issuing JWTs.
- [Server env vars](/reference/server-env-vars) — the full policy surface.
