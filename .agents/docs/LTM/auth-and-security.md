# Authentication, Authorization, and Security

## Summary

Cornus implements a layered, opt-in security model on top of a default-deny privilege posture: with no auth env set the server is a zero-cost pass-through, but operators can enable bearer authentication (static token, single-key JWT, or JWKS with rotation; plus docker-login Basic on the registry), mTLS client-cert identity, per-identity API/registry authorization (build/deploy/exec/push and opt-in pull), and a scoped caretaker credential with k8s Secret sourcing. An in-process issuer (`cornus token issue`) mints the JWTs the server verifies, and TLS serving certs plus the client CA hot-reload on rotation (cert-manager compatible). The full design rationale lives in `.agents/docs/AUTH_DESIGN_NOTE.md`.

## Key Facts

- Privilege policy is default-deny on the server: `Privileged` containers and host bind mounts are rejected unless opted in via `CORNUS_ALLOW_PRIVILEGED` and `CORNUS_ALLOW_BIND_SOURCES`; the local CLI uses `PermissivePolicy()`.
- Bearer auth is off by default; enabling any of `CORNUS_AUTH_TOKEN`, `CORNUS_JWT_HS256_SECRET`, `CORNUS_JWT_PUBLIC_KEY`, `CORNUS_JWT_JWKS_FILE`/`CORNUS_JWT_JWKS_URL`, `CORNUS_CARETAKER_TOKEN`, or `CORNUS_TLS_CLIENT_CA` turns the middleware on. Off = byte-identical pass-through (same zero-cost-when-off posture as OTel and the hub policy).
- JWT chosen over CWT/opaque because the OCI/Docker registry v2 bearer flow already mandates JWT and CWT breaks stock clients. Library: `github.com/go-jose/go-jose/v4/jwt`.
- Algorithm confusion is impossible by construction: each verifier binds exact allowed algorithms to exactly one key (public key never allows HS256; JWKS allows only asymmetric algs), so `alg:none` and public-key-as-HMAC tokens fail at parse.
- One identity abstraction: `server.Identity(r)` returns the mTLS CommonName or JWT `sub`; it feeds API authz, registry push authz, and hub reach/register policy.
- The caretaker sidecar credential is privilege-separated: a `CORNUS_CARETAKER_TOKEN` (or `caretaker`-scoped JWT) authenticates ONLY `/.cornus/v1/caretaker/attach`, never the client API or registry.
- `CORNUS_API_POLICY` (JSON `{identity: [actions]}`) gates "build", "deploy" (including all mutating item actions and the deploy-attach WebSocket), "exec" (allowed iff the policy allows `exec` OR `deploy`), "push" (registry writes), and — opt-in — "pull" (registry reads, enforced only when some rule explicitly mentions `pull`); unset = allow-all, configured = fail closed for anonymous callers, malformed = hard startup error.
- docker-login works against the registry: HTTP Basic on `/v2/*` with the token/JWT as the password (`docker login -u token -p $CORNUS_TOKEN`); the registry 401 challenge is `Basic realm="cornus"` (safe: crane clients send Bearer regardless).
- TLS serving cert/key and client CA hot-reload on file mtime change (`internal/server/tlsreload.go`), so cert-manager renewals apply without a pod restart; the Helm chart optionally renders a cert-manager `Certificate`.
- `/healthz` and `/readyz` are always unauthenticated; GET/HEAD under `/v2/*` bypass auth when `CORNUS_REGISTRY_ANONYMOUS_PULL` is on — but anonymous-pull never short-circuits authentication, so a credentialed pull still carries its identity; an explicit `pull` policy beats `CORNUS_REGISTRY_ANONYMOUS_PULL` (startup warning when both are set).

## Details

### Layer 0: privilege policy (default-deny, decision-independent)

This layer removes the "unauthenticated remote root by default" property even before any authentication exists.

**dockerhost** (`internal/deploy/dockerhost/policy.go`):
- `Policy{AllowPrivileged, AllowBindPrefixes}` with `validate(spec)`; the zero value is default-deny. Rejects `Privileged` specs and any host bind `Mount.Source` not under an allowed prefix.
- `PolicyFromEnv` reads `CORNUS_ALLOW_PRIVILEGED` (1/true/yes/on) and `CORNUS_ALLOW_BIND_SOURCES` (comma-separated absolute prefixes). `PermissivePolicy` (allow privileged + prefix "/") is for the local CLI, which already owns the host.
- Prefix matching uses `filepath.Rel`, so it is boundary-correct (`/srv/data` does NOT match `/srv/database`) and rejects `..` traversal.
- `dockerhost.Backend` has a `policy` field, functional-options `New(...Option)` + `WithPolicy`; `Apply` validates BEFORE any Docker call — a denied spec never pulls or creates.
- Server wiring (`internal/server/server.go`): `defaultBackendFactory(cfg)` builds the policy from env and ALWAYS appends `cfg.MountsDir()` to the allowed prefixes, so deploy-attach mounts (rewritten to `<DataDir>/mounts/<session>/...`) work without operator opt-in while a raw `POST /.cornus/v1/deploy` with `Source:/` is rejected.
- E2E: `internal/e2e/target.go` sets `CORNUS_ALLOW_BIND_SOURCES=/` in the DockerTarget serve env so `deploy-config.star` still passes; production stays default-deny.

**kubernetes** (parity gate):
- `Backend.allowPrivileged` (set from `CORNUS_ALLOW_PRIVILEGED` via `privilegedAllowedFromEnv` in `New()`; default false; `NewWithClients` leaves it false). `checkPrivilege(spec)` runs at the top of BOTH `Apply` and `ApplyWithMounts`, before any deployment object is built.
- Scoped strictly to the USER spec: Cornus's own injected sidecars (privileged caretaker/mount-agent, net-redirect init container) come from their own container specs, not `spec.Privileged`, so they are unaffected.
- hostPath binds were already refused outright by the k8s backend, so `Privileged` was the only remaining user-controlled escalation knob. Cluster PodSecurity admission is a separate, independent control; this gate is Cornus-side defense in depth.

### Layer 1: authentication (bearer — static token, JWT, JWKS)

`internal/server/auth.go`: an `authenticator` built from env in `New()`, wired as `Server.Handler() = otelHandler(s.auth.wrap(s.mux))` (auth inside otel so 401s are traced). Verifiers are tried static-first, then each JWT verifier:

- Opaque static token: `CORNUS_AUTH_TOKEN`, compared with `crypto/subtle` constant time; carries NO identity (anonymous under a configured API policy).
- HS256 JWT: `CORNUS_JWT_HS256_SECRET`.
- RS256/ES256 JWT: `CORNUS_JWT_PUBLIC_KEY` (PEM).
- Optional `CORNUS_JWT_ISSUER` / `CORNUS_JWT_AUDIENCE` claim checks. `exp`/`nbf` validated with 1-minute leeway (shared `validClaims`, used by both single-key and JWKS paths).
- Each `jwtVerifier` binds its allowed `jose.SignatureAlgorithm` set to exactly one key and hands `jwt.ParseSigned(token, v.algs)` those exact algs — public key means ONLY RS256/ES256, never HS256 — so `alg:none` and HMAC-with-public-key-bytes tokens are rejected at parse.

**JWKS** (`internal/server/jwks.go`):
- `CORNUS_JWT_JWKS_FILE` and `CORNUS_JWT_JWKS_URL` are mutually exclusive and configure a `jwksResolver`.
- File source: reloads on mtime change (rotation for a mounted key set); validated eagerly at startup (fail fast).
- URL source: 5-minute TTL cache; re-fetches on expiry and — rate-limited to once per minute — on an unknown `kid`, so a freshly rotated signing key is picked up without waiting out the TTL while an unknown-kid probe cannot flood the issuer. Lazy first fetch, so a briefly-down issuer at boot does not stop the server.
- Both sources keep the last good key set through a transient read/fetch error.
- `verifyJWKS` allows ONLY asymmetric algs (`jwksAlgs`, no HS*), reads `parsed.Headers[0].KeyID`, selects the JWK by kid via `pickKey` (a token with no kid is accepted only when the set has exactly one key), then validates claims via `validClaims`. Wired into `authenticate` after the single-key verifiers; `enabled()` counts `jwks != nil`. Uses go-jose's existing `JSONWebKeySet`/`JSONWebKey` types (no new dependency).

**Helm values for JWT/JWKS**: the chart's `auth.jwt.{jwksURL, jwksConfigMap, jwksSecret, jwksKey, audience, issuer}` values render the corresponding `CORNUS_JWT_*` envs on the server; the file-based sources (`jwksConfigMap`/`jwksSecret` + `jwksKey`) mount the ConfigMap/Secret read-only and point `CORNUS_JWT_JWKS_FILE` at it. Conflicting combinations fail template rendering (mirroring the server's "set only one source" rule); the defaults render nothing. This makes the kube-auth (TokenRequest) server side turnkey — see [remote-cluster-connection-ergonomics.md](./remote-cluster-connection-ergonomics.md).

**Enforcement paths**:
- `/healthz`, `/readyz` always open. Everything else requires a credential, except GET/HEAD under `/v2/*` when `CORNUS_REGISTRY_ANONYMOUS_PULL` is on. Deny-by-default for any unlisted path.
- Anonymous pull does NOT short-circuit authentication: a request carrying a credential is authenticated even on an anonymous-pull-open path, so credentialed pulls carry identity (this was a real hole — anonymous-pull previously bypassed the verifiers entirely, so credentialed pulls were identity-less and per-identity pull authz was impossible).
- 401 carries `WWW-Authenticate: Basic realm="cornus"` for `/v2/*` (docker-login compatible; crane clients send Bearer regardless of the challenge) and plain `Bearer` for `/.cornus/v1/*`.

**docker-login support**: `/v2/*` accepts HTTP Basic authentication with the token/JWT as the PASSWORD (`docker login -u token -p $CORNUS_TOKEN`; the username is ignored). The extracted password flows through the same verifier chain as a Bearer token, so static-token/JWT/JWKS and caretaker scoping all behave identically under Basic.

**Client side** (`internal/client` + CLIs): `WithToken` option, `CORNUS_TOKEN` env default. The `Authorization: Bearer` header goes on `/.cornus/v1/*` HTTP calls, the archive PUT, and ALL WebSocket attach handshakes (threaded via `http.Header` params on `buildwire.Serve`/`deploywire.Serve` and `wire.DialConnControlHeader`). `cornus push` sends the token as a crane `authn.Bearer`.

**In-process issuer** (`internal/authtoken` + `cmd/cornus/token.go`):
- The server stays verify-only; `cornus token issue --sub --scope --ttl --iss --aud --kid [--hs256-secret | --private-key <pem>]` mints the JWTs it accepts.
- One shared package owns the JWT model so issuer and verifier never drift: `Claims` = go-jose registered claims + `Scope string`; `Issue(IssueOptions)` signs with HS256 (shared secret) or a PEM private key (RS256 for RSA, ES256 for ECDSA), sets sub/iss/aud/iat/nbf/exp(+TTL)/scope, with injectable `Now` for tests. `IssueOptions.KeyID` stamps the JWT `kid` header (`SignerOptions.WithHeader("kid", ...)`), enabling JWKS-verifiable tokens.
- Scope semantics: `authtoken.CaretakerOnly(scope)` — scope is space-separated; a scope naming `caretaker` and not `api` is caretaker-only; empty or containing `api` is full. An unscoped JWT stays a full credential (backward compatible).

### Layer 2: identity (mTLS client certs)

mTLS is an ADDITIONAL authentication method, not a hard TLS gate:

- `--tls-client-ca` / `CORNUS_TLS_CLIENT_CA` (PEM bundle). `Server.Run` sets `tls.Config{ClientAuth: VerifyClientCertIfGiven, ClientCAs: pool}` when serving TLS — probes and bearer-only clients still work without a cert, but a presented cert must verify. An unreadable/unparseable CA is a hard startup error.
- `authenticator.mtls` joins `enabled()`. In `authenticate`, a non-empty `verifiedIdentity(r)` (verified cert CommonName) is returned BEFORE the bearer checks as a full credential — a cert wins over a bearer token; `wrap` stashes it via `withSubject`.
- `server.Identity(r)` is the single accessor (mTLS CN or JWT `sub`) that all authorization checks call.
- Subtlety: `verifiedIdentity(r)` reads `r.TLS` directly and works WITHOUT the auth middleware, whereas `Identity(r)` reads the context subject set by the middleware. Hub identity resolution therefore uses `Identity(r)` with a fallback to `verifiedIdentity(r)` for mTLS-at-TLS-layer-only setups.

### Layer 3: authorization (API policy, hub, registry)

**API policy** (`internal/server/apipolicy.go`):
- `CORNUS_API_POLICY` is JSON `{identity: [actions]}`; `"*"` as an action list entry means all actions.
- nil (unset) = allow-all (dev default unchanged). Configured = only listed identity/action pairs; an EMPTY identity is denied (fail closed), so a configured policy effectively requires a JWT `sub` or mTLS CN — the opaque static token is anonymous and gets denied.
- Malformed JSON is a hard startup error (`loadAPIPolicy`) — never fail open.
- Enforced with 403 before touching backend/engine: `handleBuild` requires "build"; `handleDeployCollection` POST and `handleDeployItem` DELETE require "deploy"; and `handleDeployItem` requires "deploy" for EVERY mutating item action (start/stop/restart/attach and archive PUT). Pure reads (logs, stats, status GET, archive GET) stay open. The mutating-action gate was a review fix: the first cut gated only create/delete, so `POST /.cornus/v1/deploy/{name}/stop` or `.../exec` bypassed the policy.
- **`exec` is its own action**: exec is allowed iff the policy allows `exec` OR `deploy` (deploy implies exec; enables exec-only identities). The gate applies at EVERY exec entry point, including the WebSocket start/resize paths reachable via a leaked exec id — not just exec creation.
- **The deploy-attach WebSocket is `deploy`-gated**: it was previously policy-ungated entirely (a real pre-existing hole — an identity with no actions could open attach sessions); it now requires the "deploy" action like every other mutating deploy surface.

**Hub identity fold**: `handleCaretakerUnified` takes the authoritative hub identity from `Identity(r)` (JWT `sub`, or mTLS CN when bearer auth is on), falling back to `verifiedIdentity(r)` when the auth middleware is not engaged (mTLS terminated at the TLS layer only). Before this, a spoke authenticating with a bearer JWT fell back to its self-declared (spoofable) identity; now hub reach/register policy keys on an unforgeable credential under JWT auth too.

**Registry push authz**: a `registryAuthz` middleware sits in `Server.Handler()` between auth and mux and gates registry WRITES (POST/PATCH/PUT/DELETE under `/v2/*` — blob upload/mount, manifest put, blob/manifest delete) on the `push` action of `CORNUS_API_POLICY`. Reads (pull) are NOT push-gated — pull stays governed by authentication (auth-required or anonymous-pull), so per-identity push authz and anonymous pull do not conflict. Unconfigured policy = pure pass-through.

**Registry pull authz (opt-in)**: per-identity PULL authorization is enforced ONLY when some `CORNUS_API_POLICY` rule explicitly mentions the `pull` action — a wildcard `*` action list does NOT count as mentioning it — so existing policies written before pull authz cannot lock out pulls. When pull authz is active it wins over `CORNUS_REGISTRY_ANONYMOUS_PULL` (a startup warning is logged when both are configured).

### Caretaker credential handling (privilege separation)

The in-cluster caretaker sidecar dials `/.cornus/v1/caretaker/attach`; without a credential, enabling auth on a k8s deployment would 401 the sidecar and break live client-local mounts and the hub overlay.

- `caretaker.Config` has `Token` (`json:"token,omitempty"`); `serverBundle` carries it, `groupByServer` stamps the pod-wide token onto every bundle, and `runCaretakerConn` adds `Authorization: Bearer <token>` to the `/.cornus/v1/caretaker/attach` handshake (one handshake per pod authenticates the whole yamux connection — all `'M'` mount + hub streams ride it).
- **Scoped credential**: `CORNUS_CARETAKER_TOKEN` (server-side env) is a secret DISTINCT from `CORNUS_AUTH_TOKEN`. `authenticate(r, caretakerScope)` accepts it ONLY when path == `/.cornus/v1/caretaker/attach`; on every other endpoint the caretaker branch is skipped, so a leaked sidecar credential can never authenticate the client API or the registry. Full credentials (static token, unscoped JWT, mTLS) authenticate every endpoint including the caretaker one (superset). `enabled()` counts a caretaker-only config, so a caretaker-only authenticator fails the client API closed. A `caretaker`-scoped JWT is treated identically to the static caretaker token.
- The k8s backend injects the caretaker token via `caretakerConfigEnv(cfg)` (single helper marshaling `Config` into the `CORNUS_CARETAKER_CONFIG` env, used by all five sidecar-assembly sites), stamping the token ONLY onto server-bound configs (`len(cfg.Mounts) > 0 || cfg.Hub != nil`) — DNS/proxy-only sidecars never dial the server and never carry it.
- **Secret sourcing** (no pod-spec literal): `CORNUS_CARETAKER_TOKEN_SECRET` ("name" or "name/key", default key "token", parsed by `parseSecretRef`) makes `caretakerConfigEnv` emit a `CORNUS_TOKEN` env with a `secretKeyRef` for server-bound sidecars AND leave `Config.Token` unset. The caretaker's `applyEnvToken(&cfg)` in `Run` overlays `cfg.Token` from the `CORNUS_TOKEN` env when set. Precedence: secret ref > embedded value > none. No new RBAC — the kubelet resolves the secretKeyRef; the Secret must exist in the deploy namespace, and the operator can source the server's `CORNUS_CARETAKER_TOKEN` from the same Secret.
- **JWT-only Kubernetes**: a `caretaker`-scoped JWT placed in the sidecar Secret is verified by the server's normal JWT verifier and accepted only on the caretaker endpoint, so k8s live mounts work under auth with NO static `CORNUS_CARETAKER_TOKEN`.
- Defense in depth: even a valid caretaker connection still needs the unguessable per-mount session id to relay a specific mount, and hub registration is still gated by the register policy.

### Cert-manager support (opt-in) and TLS hot-reload

cert-manager is not a code integration — it issues certs into standard TLS Secrets — so support is two pieces:

- **Cert hot-reload** (`internal/server/tlsreload.go`, the correctness-critical piece): `tlsConfig(cert, key, clientCA)` builds a `*tls.Config` whose `GetCertificate` (`certReloader`) and, for mTLS, `GetConfigForClient` (`caReloader`) re-read the files on mtime change; `Run` serves with `ServeTLS(ln, "", "")` so the cert comes from the callback. Both load eagerly at startup (bad path = hard error); a transient read error mid-rotation keeps serving the last good pair. Provider-agnostic (works for Vault/SPIFFE too). Previously `Run` used `srv.ServeTLS(ln, certFile, keyFile)` and a one-shot CA-pool read, so a renewal required a pod restart.
- **Helm wiring**: `values.yaml` `tls` block — `enabled`, `secretName`, `clientCA` (mTLS), `certManager` (`enabled`, `issuerRef.{name,kind}`, `dnsNames` defaulting to the Service DNS names, `duration`, `renewBefore`). `templates/certificate.yaml` renders a cert-manager `Certificate` only when `tls.enabled && tls.certManager.enabled` (`issuerRef.name` is `required`). `templates/statefulset.yaml` appends `--tls-cert`/`--tls-key` (+`--tls-client-ca` when `clientCA`) to args, mounts the Secret at `/etc/cornus/tls`, adds the Secret volume, and sets probe `scheme: HTTPS`; with tls off it renders byte-identically to before. The raw `deploy/k8s/cornus.yaml` stays plaintext (Helm is the opt-in TLS home).

## Files

- `internal/deploy/dockerhost/policy.go` — dockerhost privilege `Policy`, `PolicyFromEnv`, `PermissivePolicy`
- `internal/deploy/kubernetes` — `Backend.allowPrivileged`, `checkPrivilege`, `caretakerConfigEnv`, `parseSecretRef`
- `internal/server/auth.go` — `authenticator`, verifiers, `authenticate`, `Subject`/`Identity`, enforcement
- `internal/server/jwks.go` — `jwksResolver`, `verifyJWKS`, `pickKey`, `jwksAlgs`
- `internal/server/apipolicy.go` — `CORNUS_API_POLICY` model, `loadAPIPolicy`, action checks
- `internal/server/tlsreload.go` — `tlsConfig`, `certReloader`, `caReloader`
- `internal/server/server.go` — `defaultBackendFactory` policy wiring, `Handler()` middleware chain (otel → auth → registryAuthz → mux)
- `internal/authtoken` — shared JWT `Claims`/`Scope` model, `Issue`, `CaretakerOnly`
- `internal/caretaker/caretaker.go` — `Config.Token`, `applyEnvToken`, bearer header on the attach handshake
- `internal/client` — `WithToken`, `CORNUS_TOKEN`, header threading through `buildwire`/`deploywire`/`wire.DialConnControlHeader`
- `cmd/cornus/token.go` — `cornus token issue`
- `cmd/cornus/commands.go` — local CLI deploys with `PermissivePolicy()`
- `deploy/helm` (`values.yaml`, `templates/certificate.yaml`, `templates/statefulset.yaml`) — opt-in TLS/cert-manager
- `.agents/docs/AUTH_DESIGN_NOTE.md` — layered plan + JWT-vs-CWT-vs-opaque analysis
- README "Security and trust boundary" + "Optional bearer authentication" sections

## Test Coverage

- `internal/deploy/dockerhost/policy_test.go` — privileged + bind allow/deny (docker-socket and root cases), prefix boundary + `..` traversal, env parsing, Apply-enforces-before-daemon (`pulled`/`created` stay empty on deny)
- kubernetes `TestApplyPrivileged` — default-deny rejects; `allowPrivileged = true` yields `SecurityContext.Privileged=true`
- `internal/server/auth_test.go` — verifier and enforcement matrix; `TestCaretakerTokenScope` (caretaker token accepted on `/.cornus/v1/caretaker/attach`, rejected on `/.cornus/v1/deploy`/`/.cornus/v1/build`/`/v2/*`; full token everywhere; caretaker-only authenticator fails client API closed); `TestJWTScopeEnforced` (end-to-end issuer→verifier scope check)
- `internal/server/authz_test.go` — mTLS authenticates-as-CN (fake verified peer cert), cert-wins-over-bearer, no-cert-falls-back-to-bearer; apiPolicy matrix incl. `*`, empty-identity-denied, allow-all-when-unconfigured, malformed-policy hard error; handler-level 403/allow on deploy + build; `TestAPIPolicyGatesMutatingActions`; `TestRegistryPushAuthz`
- `internal/server/jwks_test.go` — file verify + rotation via mtime, URL refetch-on-unknown-kid (rotating httptest server), HS256 rejected, kid/key mismatch rejected
- `internal/server` TLS — `TestCertReloaderPicksUpRotation`, `TestCertReloaderBadPathFailsFast`, `TestTLSConfigMTLSReloadsCA`
- Hub — `TestHubMTLSIdentityIsAuthoritative`, `TestHubJWTIdentityIsAuthoritative` (JWT `sub=web` reaches `echo` while declaring `denied`; `sub=intruder` refused while declaring `web`)
- Caretaker/k8s — `TestGroupByServer` (token stamped/empty), `caretaker_token_test.go` (`caretakerConfigEnv` stamps mounts/hub, NOT dns/proxy), `TestCaretakerConfigEnvSecretRef`, `TestParseSecretRef`, `TestApplyEnvToken`
- `internal/authtoken` — HS256 + ES256 issue/verify roundtrip, scope logic, error paths, `TestIssueStampsKeyID`

## Pitfalls

- The static `CORNUS_AUTH_TOKEN` carries no identity: under a configured `CORNUS_API_POLICY` it authenticates but is denied as an empty identity. Identity-bearing credentials (JWT `sub` or mTLS CN) are required for policy-gated actions.
- Do not gate only deploy create/delete: every mutating deploy-item action (start/stop/restart/exec/attach, archive PUT) must require the "deploy" action, or the policy is bypassable via `POST /.cornus/v1/deploy/{name}/stop` or `.../exec` (this was an actual review-caught bug).
- `verifiedIdentity(r)` vs `Identity(r)`: the former reads `r.TLS` and works without the auth middleware, the latter reads the middleware-set context subject. Hub identity needs `Identity(r)` first with a `verifiedIdentity(r)` fallback — a straight replacement breaks mTLS-without-middleware setups.
- Never let a public-key verifier accept HS256 (or a JWKS verifier any HS* alg): bind exact algorithm sets per key at parse time to preclude algorithm-confusion attacks.
- The k8s `checkPrivilege` gate must apply only to the USER spec — Cornus's own privileged sidecars (caretaker/mount-agent, net-redirect init) are built from separate container specs and must remain unaffected.
- The dockerhost server must always append `cfg.MountsDir()` to the allowed bind prefixes, or deploy-attach (client-local 9P mounts) breaks under default-deny.
- Bind-prefix matching must be boundary-correct (`filepath.Rel`, not string prefix): `/srv/data` must not match `/srv/database`, and `..` traversal must be rejected.
- Serving TLS via `srv.ServeTLS(ln, certFile, keyFile)` reads the cert once — with cert-manager the pod would eventually serve an expired cert. Use `GetCertificate`/`GetConfigForClient` reloaders and `ServeTLS(ln, "", "")`.
- The caretaker token must be stamped only onto server-bound sidecar configs (mounts or hub); DNS/proxy-only sidecars never dial the server and must not carry a credential.
- JWKS URL refetch on unknown kid must be rate-limited (once/min) so an attacker probing with bogus kids cannot flood the issuer; keep the last good key set through transient fetch errors.
- Registry pull must stay authentication-governed, not push-policy-governed, or anonymous pull (`CORNUS_REGISTRY_ANONYMOUS_PULL`) and per-identity push authz would conflict. Per-identity pull authz is a separate, explicit opt-in: it activates only when a policy rule literally names `pull` (never via `*`), so enabling a policy cannot silently break existing pull traffic.
- Anonymous pull must not short-circuit authentication: bypassing the verifiers on anonymous-pull-open paths strips identity from credentialed pulls (an actual pre-existing hole, fixed alongside pull authz).
- Gating only the HTTP creation endpoint is not enough for exec: the WebSocket start/resize paths are reachable via a leaked exec id and must carry the same `exec`/`deploy` check. Likewise the deploy-attach WebSocket needs the `deploy` gate — it was originally policy-ungated entirely.
- Switching the registry 401 challenge to `Basic realm="cornus"` is safe for programmatic clients: crane/go-containerregistry send Bearer regardless of the advertised scheme; the Basic form is what unlocks stock `docker login`.

## Local AI Credential Sources

Credential sources own their store schemas. `claude-code` reads `~/.claude/.credentials.json` (`claudeAiOauth.accessToken`, millisecond expiry) and emits an `oauth_token` for the Anthropic proxy. `codex` reads `~/.codex/auth.json`, preferring top-level `OPENAI_API_KEY` and otherwise using the ChatGPT OAuth access token as an `api_key`; the OAuth mode is not automatically suitable for api.openai.com. The generic `env` source remains the classic OpenAI CLI path.

`pkg/credential/internal/jsonstore` is schema-free and must never log or print credential values. Credential-delivery factories accept upstream configuration, allowing approved gateways and hermetic proxy tests. The server mock-upstream test verifies injected authorization and vendor headers across the real relay path.
