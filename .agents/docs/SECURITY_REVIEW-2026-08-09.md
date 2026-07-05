# Pre-release security design review (2026-08-09)

**Scope.** A design-level review from an adversarial/security-researcher point of view,
conducted **from documentation only** — `docs/` (English tree), `ARCHITECTURE.md`,
`README.md`, and `.agents/docs/` (notably `LTM/auth-and-security.md` and `TODO.md`).
The initial pass read no source. Findings marked *(verify in code)* were doc-derived
inferences stated as questions the code must answer.

**Update (same day): the three blockers and H1 were subsequently verified against the
tree, by reading and by running throwaway tests. All four are CONFIRMED, and two are
worse than the documentation-only pass concluded.** See
[Verification against the code](#verification-against-the-code) at the end for the
evidence and the exact observations. The remaining *(verify in code)* markers below are
still unverified.

**Overall.** The security design is unusually well-reasoned for a pre-1.0 project. The
hard parts that projects normally get wrong — algorithm confusion, third-party scope
authority, fail-open policy loading, scope containment at a token-exchange endpoint —
are all got right, and got right *for stated reasons*. The findings below are mostly
about **defaults**, **surface completeness**, and **what the docs promise versus what
the mechanisms cover**, not about broken cryptography or a wrong model.

---

## Release blockers

### B1. Default posture is unauthenticated remote code execution on every interface

`cornus serve` binds `:5000` on all interfaces with authentication off, and that API can
build images, deploy workloads, `exec` into them, and push to the registry. The docs are
honest about it (`docs/cli/serve.md` "Listen address and exposure", `docs/guides/security.md`
opening, README), and the rationale for the bind is sound (caretakers dial back; the host's
loopback is not the container's).

The risk is not that it is undocumented — it is that this is the shape that gets found by
scanners. Docker's `tcp://` socket, Kubernetes' historical anonymous-auth, unauthenticated
Redis/etcd/Elasticsearch/Jupyter all produced mass compromise from exactly this
configuration: a powerful API, an all-interfaces default, and a warning in the docs. A first
public release converts a documented footgun into a fleet.

Compounding it, `docs/introduction/installation.md` teaches:

```sh
docker run -d --name cornus --privileged -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock ghcr.io/moriyoshi/cornus:latest
```

`-p 5000:5000` publishes on `0.0.0.0`, in front of a container that is privileged and holds
the host Docker socket. That is remote root on the host, from the first page a new user reads —
and it carries none of the warning text that `cli/serve.md` and the README carry.

**Recommendation.**
1. Keep the permissive default for a **loopback** bind. Make a **non-loopback** bind with
   **no verifier configured** refuse to start unless the operator states the intent
   (`--allow-anonymous` / `CORNUS_ALLOW_ANONYMOUS=1`). This preserves every local-development
   flow untouched, and turns the dangerous configuration into a deliberate one. It is the
   same shape as the existing, correct `cornus socks5 --allow-non-loopback` and
   `cornus web --allow-non-loopback` gates — the server is the one surface that does not
   have it.
2. Change the installation and Compose examples to `-p 127.0.0.1:5000:5000` and carry the
   `cli/serve.md` warning box there.
3. Audit the Helm chart / `deploy/k8s/cornus.yaml` for a Service that exposes the API with
   no auth values set, and consider making the chart refuse to render that combination.

### B2. The workload privilege policy does not cover the escalation knobs that matter

The documented default-deny privilege policy gates exactly two things: `spec.Privileged`
and host bind sources (`CORNUS_ALLOW_PRIVILEGED` / `CORNUS_ALLOW_BIND_SOURCES`). But
`docs/reference/deploy-spec.md` accepts, ungated:

| Field | Consequence on dockerhost | On kubernetes |
|---|---|---|
| `capAdd` | `SYS_ADMIN`, `SYS_PTRACE`, `SYS_MODULE`, `DAC_READ_SEARCH` → escape | mapped to `securityContext.capabilities` |
| `securityOpt` | "passes them verbatim" → `seccomp=unconfined`, `apparmor=unconfined` | only well-known ones mapped |
| `devices` | `- /dev/sda:/dev/sda` → raw disk, bypassing the bind-source allowlist entirely | ignored |
| `pidMode: host` | host PID namespace → `/proc/1/root`, ptrace escape | mapped to `hostPID` |
| `ipcMode: host` | host IPC | mapped |
| `sysctls` | namespaced only per the doc, worth confirming | — |

So `CORNUS_ALLOW_PRIVILEGED=false` does not deny privilege; it denies one spelling of it.
`.agents/docs/LTM/auth-and-security.md` states "hostPath binds were already refused outright
by the k8s backend, so `Privileged` was the only remaining user-controlled escalation knob" —
the deploy-spec table contradicts that for `capAdd` and `pidMode` at minimum.

Note that `devices` is the sharpest one: it reaches the host filesystem *without* going
through the bind-source prefix check that was carefully made boundary-correct with
`filepath.Rel`.

**Recommendation.** Extend the policy validation (dockerhost `Policy.validate` and the
kubernetes `checkPrivilege` parity gate) to reject, under the same default-deny:
any `devices` entry; a dangerous-capability denylist in `capAdd`; `securityOpt` values that
weaken seccomp/AppArmor/SELinux or set `no-new-privileges=false`; and `pidMode`/`ipcMode`
of `host`. Either fold them under `CORNUS_ALLOW_PRIVILEGED` or add a finer
`CORNUS_ALLOW_CAPABILITIES` / `CORNUS_ALLOW_DEVICES`. This is cheap, testable, and the one
finding here that is a code change rather than a documentation or default change.
*(verify in code — the doc table is the evidence, not the backend implementations.)*

### B3. Authorization has no resource dimension, and reads are entirely ungated

`CORNUS_API_POLICY` maps identity → *actions*, never identity → *resources*. Two
consequences the docs do not state:

- Any identity with `deploy` can delete, restart, or `exec` into **every** workload on the
  server, including ones deployed by other identities. `exec` as its own action is described
  as enabling "exec-only identities that can shell into a running workload without being able
  to apply or delete one" — which reads as a tenancy control but is a verb control.
- **Pure reads stay open regardless of policy**: logs, stats, status, and — per
  `LTM/auth-and-security.md` — **archive GET**. Archive GET streams files out of a
  container's filesystem. So an authenticated identity with an *empty* action list can read
  every workload's logs (where secrets routinely land) and download other workloads' files.
  With auth off entirely, so can anyone who can route to the port.

The design is coherent if Cornus is single-trust-domain — every authenticated caller is a
mutually-trusting operator. That is a perfectly respectable position. It is just not the
position the docs currently imply.

**Recommendation.** For this release, **state it**: one paragraph in
`docs/architecture/security.md` saying that authenticated clients share one trust domain,
that the API policy restricts verbs and not ownership, and that read endpoints (logs, stats,
archive GET) are governed by authentication alone. Then decide separately whether
`origin.subject`-keyed ownership scoping is a post-1.0 goal. Gating archive GET behind an
action would be a cheap partial improvement in the meantime.

---

## High

### H1. The auto-started builder is an unauthenticated privileged root container

`CORNUS_BUILDER_AUTO` is **on by default**. On the first build a server that cannot
`mount(2)` starts `cornus-builder` `--privileged --network host` — sharing the host Docker
socket in re-export mode — and delegates to `ws://127.0.0.1:5099`. The docs say
"Authorization is still enforced by the delegating server before anything reaches the
builder," which protects the *delegated* path but says nothing about the builder's own
listener. Nothing in the docs indicates the builder requires a credential.

If that port is unauthenticated, any local user (or any container with host networking, or
anything else that reaches `127.0.0.1:5099` — remember the builder runs with `--network host`)
drives a privileged root build engine with the host Docker socket. That is local
privilege escalation to root, arriving automatically on first build.

**Recommendation.** Have the delegating server mint an internal credential for the builder —
the installation-secret machinery (`registry:push` etc.) already exists and is exactly the
right tool — or move the builder to a unix socket. Document whichever it is. *(verify in code.)*

### H2. Identity strings are not qualified by the verifier that asserted them

`server.Identity(r)` collapses an mTLS CommonName, a JWT `sub` from `CORNUS_JWT_HS256_SECRET`,
a `sub` from a JWKS-verified third-party token, an SSH-enrolled name, and an exchanged
token's subject into one flat string, which `CORNUS_API_POLICY`, `CORNUS_HUB_POLICY`, and
`CORNUS_HUB_REGISTER_POLICY` then key on.

So every configured verifier is implicitly trusted to assert *any* identity. Concretely:
`--tls-client-ca` uses `VerifyClientCertIfGiven` against a CA **bundle**, and CommonName is
the identity — so any CA in that bundle can issue `CN=admin` and become the `admin` entry in
the API policy. If an operator points it at a corporate or public-adjacent CA, or at a bundle
with more than one root, impersonation is a certificate request away. The same holds for an
IdP whose `sub` namespace overlaps an operator-minted `sub`.

**Recommendation.** Namespace the identity by its source (`mtls:CN=…`, `jwt:iss#sub`,
`ssh:name`) in the policy key, or at minimum document loudly that (a) the client CA must be a
dedicated CA that issues nothing else, and (b) `sub` namespaces across configured verifiers
must not collide. Consider also supporting a URI SAN rather than CN, since CN-as-identity is
deprecated practice.

### H3. JWT audience validation is optional where it is load-bearing

`CORNUS_JWT_AUDIENCE` is documented as an *optional* claim check. With a JWKS source and no
audience configured, **any** token that issuer minted for **any** relying party authenticates
to Cornus. The Kubernetes case is the worst: a default-audience ServiceAccount token is
mounted into every pod in the cluster, so "verify against the cluster's JWKS" without an
audience means every pod in the cluster is a Cornus caller. Whether it then *gets* anything
depends on the scope map — which is the right second line of defence, but audience is the
first one and it is off.

**Recommendation.** Make `CORNUS_JWT_AUDIENCE` **required** when a JWKS source is configured
(hard startup error, consistent with the project's existing fail-fast-on-misconfiguration
stance), or at minimum a loud startup warning. The docs' own token-exchange example already
does the right thing (`kubectl create token --audience cornus`) — the server should insist on
what its example demonstrates.

### H4. `CORNUS_JWT_DEFAULT_SCOPE` is documented in a neutral register

`CORNUS_JWT_DEFAULT_SCOPE=api` turns "any token any configured verifier accepts" into a full
admin credential. The docs frame it reasonably — "Use it when every token a verifier accepts
really is a full credential — the point is that this is now *stated*, not inferred" — which
is a good argument for its existence. But combined with H3 it is one env var from
whole-IdP-is-admin, and it reads as an ordinary convenience knob.

**Recommendation.** A warning box, a startup log line at `warn` naming what it granted, and
consider refusing the combination `CORNUS_JWT_DEFAULT_SCOPE=api` + JWKS + no audience.

### H5. The egress gateway is a server-side SSRF / open relay when unconfigured

`CORNUS_EGRESS_GATEWAY` marks the server an egress terminus that dials destinations on a
workload's behalf with **no client session**. `CORNUS_EGRESS_POLICY` is described as an
*optional* ceiling. With the gateway on and no policy, a workload reaches everything the
server can reach: the cluster API server, other tenants' services, internal admin panels, and
`169.254.169.254` — which on a cloud node yields the *node's* IAM identity, the Capital One
shape.

The project already made exactly the right call on the client side: `cornus socks5`, when
exposed, "refuses to dial loopback and link-local destinations." The server-side gateway
should not be weaker than the client-side proxy.

**Recommendation.** Require `CORNUS_EGRESS_POLICY` when `CORNUS_EGRESS_GATEWAY` is set, and
apply the socks5 rule — deny loopback, link-local (`169.254.0.0/16`, `fd00:ec2::254`), and by
default RFC1918 — as a floor beneath any policy.

### H6. The web UI / MCP surface: no authentication in front of exec and file write

The docs are commendably direct here ("**anyone who can reach that port gets all of it**"),
and the defaults are right: loopback-only, a Host allow-list, and per `LTM/web-ui.md` the
websocket library's same-origin default is deliberately retained. Three residual concerns:

1. **Loopback is not an authentication boundary on a shared host.** Any local user, any
   container with host networking, any other process on a dev box or jump host reaches
   `exec`, persistent terminals, `file_write`, and the file explorer over the developer's own
   connection profile. Jupyter and `code serve-web` both concluded a token was necessary here.
2. **The rebinding guard fails open in one flag combination.** `--allow-non-loopback` with no
   `--allow-host` "is dropped entirely and startup warns that it is off." A warning is the
   weakest possible response to disabling the only guard on a no-auth exec surface.
3. **MCP disables the SDK's own localhost protection** (`DisableLocalhostProtection`) to
   accommodate the published-conduit Host, relying on `guardHost` instead. That is a
   defensible substitution, but it means the MCP endpoint's rebinding defence is now entirely
   Cornus's, and the MCP Streamable HTTP spec calls for Origin validation on local servers.
   Confirm the **non-WebSocket** BFF and MCP handlers validate `Origin`, not only `Host` —
   the same-origin default cited in the LTM covers the websockets. *(verify in code.)*

**Recommendation.** Ship a bearer token: generated at startup, printed in the URL the command
already prints, overridable with `--token`, `--no-token` to opt out. It costs little and
removes the whole class. Make `--allow-non-loopback` without `--allow-host` a hard error.

### H7. Hub service names are unclaimed by default

`CORNUS_HUB_POLICY` (reach) and `CORNUS_HUB_REGISTER_POLICY` (register) are "each enforced
only when configured." Unconfigured, any spoke can register any name. A malicious workload
registering `db` intercepts every peer's `dial(db)` — and because the caretaker `dns` role
maps the name to a synthetic loopback IP, the victim application has no way to notice: it
made an ordinary connection to an ordinary name and got a working socket.

**Recommendation.** Document the squatting property in `docs/guides/hub.md` next to the policy
paragraph. Consider a default, when client auth is on, of "an identity may register only names
it is the authenticated owner of" (e.g. prefix-matched on the identity), which is more useful
than nothing and less work than a full matrix.

### H8. The peer-forward trust root is the hub store

Inter-replica `peer` credentials are ES256 JWTs validated against a public key **read out of
the hub store**, with `sub == kid`. So write access to Redis or to the `HubEndpoint` CRs is
sufficient to publish an attacker key and mint a valid `peer` credential for the three forward
endpoints — and to rewrite delivery routing. Redis is very commonly deployed with no AUTH and
no TLS.

The docs describe the mechanism precisely but never name the store as a trust root.

**Recommendation.** State the assumption: the hub store is a trusted control-plane component;
`CORNUS_HUB_REDIS` must use AUTH and TLS; the `HubEndpoint` CRs and the replica Leases must be
RBAC-restricted to the Cornus ServiceAccount. Add it to the multi-replica section of
`docs/guides/security.md`.

---

## Medium

- **M1 — No revocation, anywhere.** Deleting an enrolled SSH key blocks new sessions but does
  not revoke issued ones (documented, up to a 24h ceiling); exchanged tokens, `cornus token
  issue` tokens, and mTLS client certs (no CRL/OCSP) have no revocation path at all. The only
  actual revocation primitive is rotating a signing key, which revokes everything. Document
  the model explicitly; consider a `jti`/subject denylist and shorter default TTLs.

- **M2 — No content trust in the build → push → deploy chain.** No signature verification
  anywhere. With the registry open by default, anyone who can reach `:5000` can overwrite a
  tag the deploy engine will pull. Worse, the documented `dockerhost` re-export view
  *recomputes digests* via `docker save`, so "a manifest digest learned from a prior push may
  differ — pull by tag," i.e. digest pinning (which `deploy-spec.md` recommends: "ideally
  digest-pinned") is unusable on that path. Plus `CORNUS_*_INSECURE_REGISTRIES` for plain HTTP.
  State the absence of content trust as a known limitation and note where digest pinning does
  and does not hold.

- **M3 — Credential endpoints are authorized by the network namespace, and that has named
  consequences.** On Kubernetes the loopback credential and LLM/GitHub proxy endpoints are
  reachable by **every container in the pod**, including injected debug/ephemeral containers,
  and by anything that joins the netns. With `aws-imds` + `wellKnown: true` the endpoint binds
  `169.254.169.254`, so **any app-level SSRF becomes credential theft** — and unlike real
  IMDSv2 there is no hop-limit equivalent to blunt it. The credentials guide's "Trust" section
  should say both. (The `github-cli` blast-radius warning already in that guide is a model for
  how to write this.)

- **M4 — `CORNUS_INGRESS_ENFORCE_DOMAIN` defaults to false.** A deploy can declare
  `hosts: [login.corp.example]`. On Kubernetes that creates a real Ingress claiming the name
  cluster-wide and may request a certificate through the configured cluster-issuer; on host
  backends `ingressmux` serves it. Default to enforcing whenever `CORNUS_INGRESS_DOMAIN` is
  set — a workload that wants a host outside the domain is the exception, not the norm.

- **M5 — Tunnels.** `cornus tunnel` publishes a workload port to the public internet with no
  authentication in front of it, and `tunnel` is *implied by* `deploy`, so it is not a separate
  decision. A server-side `CORNUS_TUNNEL_AUTHTOKEN` means any deploy-authorized caller spends
  and exposes through the operator's ngrok/Cloudflare account. `CORNUS_TUNNEL_SSH_INSECURE`
  disables host-key verification. Make `tunnel` a non-implied action; add a warning box to
  `docs/guides/tunnels.md` stating the no-auth-in-front property.

- **M6 — Workload logs are recorded to disk by default.** `CORNUS_OBS_RECORD_LOGS` is on,
  retention 168h, and Grafana datasource APIs are served directly. Logs routinely contain
  secrets, so this is a new at-rest secret store created by default. Document the location,
  permissions, and retention implications; consider making log recording (as distinct from
  metrics) an opt-in.

- **M7 — No resource limits or rate limiting by default.** `CORNUS_MAX_BUILD_CONTEXT_BYTES` is
  unset (unbounded upload), registry uploads appear uncapped, and nothing rate-limits
  authentication failures on the SSH-challenge, token-exchange, or docker-login Basic paths —
  i.e. unlimited offline-speed guessing against `CORNUS_AUTH_TOKEN`. Combined with B1 this is
  trivially exploitable. The JWKS unknown-kid refetch *is* rate-limited, which shows the
  instinct is there; extend it. Ship a default build-context cap and an auth-failure limiter.

- **M8 — `/.cornus/v1/info` is auth-exempt.** It correctly withholds the container id and path
  map, but still discloses the advertised registry host, ingress domain/class/controller, and
  front-door mode to any unauthenticated caller. Worth naming as a fingerprinting surface and
  keeping deliberately minimal.

---

## Documentation and process gaps for a public release

1. **No `SECURITY.md`.** No disclosure contact, no embargo expectations, no supported-versions
   statement, no CVE process. GitHub surfaces its absence on the repo page, and researchers who
   find something will otherwise open a public issue. This is the single cheapest item here.
2. **No consolidated threat model page.** Every piece exists, scattered across
   `architecture/security.md`, `guides/security.md`, and the subsystem pages. One page naming
   the actors (operator, client user, workload, co-tenant, network attacker, image author) and
   the standing assumptions would carry a lot of weight:
   *a build is remote code execution; an authenticated client is an operator; a workload is
   untrusted; the hub store, the client CA, and the configured JWT issuers are trusted; loopback
   is not an authentication boundary.*
3. **No hardening checklist.** `guides/security.md` has all the recipes but the reader must
   assemble them. A "production posture" page an operator follows top to bottom — bind address,
   TLS, verifier, audience, API policy, privilege policy, anonymous pull off, egress policy,
   ingress enforce-domain, hub policies, retention — would be the most-linked page in the docs.
4. **The least-privilege story for the server itself is missing.** The quickstart is
   `--privileged` + `docker.sock`, which is root on the host by construction. Say so plainly,
   and give the reduced variants: a server that does not build, a server that does not use
   `dockerhost`, and what each drops.
5. **No key inventory.** `installation.key`, `peer.key`, the enrollment secret,
   `auth/authorized_keys`, TLS material — locations, modes, what rotation breaks, and what
   sharing across replicas requires. Most of the facts are in the docs; a single table would
   make them operable.
6. **No statement about what lands in logs.** The internal note that `jsonstore` must never log
   credential values is not a user-facing guarantee. A short statement about what the activity
   flight recorder and server logs contain (identities, image refs, env keys?) belongs in the
   observability or activity docs.

---

## What is done well

Worth recording, both because it is true and because it says where *not* to spend review
budget:

- **Algorithm confusion is prevented by construction, not by a check** — each verifier binds
  one key to one exact algorithm set at parse time.
- **The "who holds the signing key" dividing line** for third-party scope, and the refusal to
  honour a JWKS-verified token's own `scope` claim. Most projects conflate proving identity
  with granting authority; this one names the distinction and builds on it.
- **No regular expressions in the scope map**, with the reasoning stated (unanchored patterns,
  catastrophic backtracking). Literal-claim-name-before-dotted-path is the right lookup order.
- **Fail-closed on malformed policy at startup**, applied consistently across API policy, hub
  policies, scope map, and the hub forward CA.
- **Scoped caretaker credential** with `secretKeyRef` sourcing, and scopes as an allowlist that
  refuses to *mint* a credential granting nothing.
- **Token exchange refuses `caretaker` and `peer`** even though containment alone would permit
  narrowing into them. That is precisely the case that gets missed.
- **The confined 9P export** — no `..`, no symlink escape, read-only, `.dockerignore` applied
  before bytes leave the caller.
- **Egress policy re-evaluated at every hop**, so a compromised pod cannot upgrade its routing.
- **`cornus socks5` refusing loopback and link-local when exposed**, and refusing a
  non-loopback bind without an explicit flag.
- **Opt-in pull authz that cannot silently break existing policies** (a `*` wildcard
  deliberately does not count as naming `pull`).
- **Signed releases** — `SHA256SUMS` plus a keyless cosign bundle, with a copy-pasteable
  `cosign verify-blob` including the certificate-identity regexp.

---

## Suggested ordering

**Before tagging:** B1 (bind gate + fix the install examples), B2 (privilege-policy coverage),
H1 (builder credential), H3 (require audience with JWKS), plus `SECURITY.md`.

**Before announcing widely:** B3 (state the trust domain), H5, H6, and the threat-model and
hardening pages.

**Post-1.0 design work:** resource-scoped authorization, revocation, content trust,
issuer-qualified identities.

---

## Verification against the code

Performed the same day, after the documentation-only pass. Method: read the relevant
source, then — for each claim — write a throwaway test that would **pass if the claim were
false**, run it, and confirm the observation is real rather than an artifact of a broken
code path (each test carries a control asserting the mechanism does work where it is
supposed to). All scratch test files were deleted after running; the working tree is
unchanged.

### B1 — CONFIRMED, and the startup logging is inverted

`cmd/cornus/serve.go:24` — `--addr` defaults to `:5000`. There is **no gate and no warning**
for the dangerous combination. `logListenScope` (`serve.go:139-150`) returns early unless
the address is loopback-only, so the server speaks up **only in the safe case**:

```
:5000          -> (silent)
0.0.0.0:5000   -> (silent)
[::]:5000      -> (silent)
127.0.0.1:5000 -> INFO listening on loopback only; clients on other hosts ...
```

Nothing anywhere in `pkg/server` logs at startup that authentication is off
(`auth.enabled()` is consulted only for the peer key and OTLP headers). So a server bound
to every interface with no verifier configured emits no signal at all — the exposure is
discoverable only from `--help` and the docs.

Worse than documented: **`deploy/k8s/cornus.yaml:154` uses `type: NodePort` on
`nodePort: 30500`**, and the StatefulSet sets no `CORNUS_AUTH_*` / `CORNUS_JWT_*` env. The
comment explains the intent (a node must pull built images without a `kubectl port-forward`),
which is reasonable — but NodePort binds on **every node's** addresses, not the node's
loopback, so the shipped quick-start manifest publishes an unauthenticated
build/deploy/exec API cluster-wide and, on a cluster with permissive node firewalling,
beyond it. The Helm chart defaults `service.type: ""` (derived) and auth off.

### B2 — CONFIRMED on every backend, by execution

`hostpolicy.Policy.Validate` reads exactly two fields: `spec.Privileged` and `spec.Mounts`.
Nothing else in the tree gates the remaining knobs — a search for a capability denylist
(`SYS_ADMIN`, `dangerousCap`, `allowedCaps`) finds only comments about cornus's *own*
privilege requirements.

Scratch test against the zero-value (default-deny) `Policy`:

```
ALLOWED capAdd SYS_ADMIN         under default-deny policy
ALLOWED securityOpt unconfined   under default-deny policy
ALLOWED devices raw disk         under default-deny policy
ALLOWED pidMode host             under default-deny policy
ALLOWED ipcMode host             under default-deny policy
ALLOWED all at once              under default-deny policy
```

Controls passed: `Privileged: true` and `Mounts: [{Source: "/"}]` were both denied, so
`Validate` is live and the six results are its real behaviour.

Where they land:

- **dockerhost** — `engine.go:1438-1440` (`CapAdd`, `SecurityOpt`), `:1496-1498` (`Devices`),
  `:1505-1506` (`PidMode`, `IpcMode`), all straight onto the Docker `HostConfig`.
- **containerd / bare** — `internal/hostrun/spec_linux.go:86` (`oci.WithAddedCapabilities`),
  `:102` (`SecurityOpt`), `:115` (`Devices`).
- **kubernetes** — `kubernetes.go:2544` maps `CapAdd` to `securityContext.capabilities.add`
  unconditionally; `:2936`/`:2943` map `pid: host` / `ipc: host` to `HostPID` / `HostIPC`.
  `Devices` is ignored with a warning (`:2959`), and `seccomp=`/`apparmor=` are unmapped
  (`applySecurityOpt`), so kubernetes is narrower — but not closed.

Scratch test against a fake clientset with `allowPrivileged = false`:

```
LANDED capabilities.add = [SYS_ADMIN SYS_PTRACE] (allowPrivileged=false)
LANDED hostPID = true                            (allowPrivileged=false)
LANDED hostIPC = true                            (allowPrivileged=false)
```

Control passed: `Privileged: true` was rejected by `checkPrivilege` in the same run.
`hostPID` plus `SYS_PTRACE` is a textbook container escape, and it passes the gate whose
comment says privileged app containers are what it rejects.

`.agents/docs/LTM/auth-and-security.md`'s line — "hostPath binds were already refused
outright by the k8s backend, so `Privileged` was the only remaining user-controlled
escalation knob" — is therefore wrong and should be corrected in place.

### B3 — CONFIRMED, and broader than the docs suggest

`apiPolicy.Allow(identity, action string)` takes **no resource argument** — there is no
place to express "which deployment", so an identity granted `deploy` reaches every
workload by construction.

`pkg/server/deploy.go:177-200` gates by action and, for `archive`, by *method*: `PUT` needs
`deploy`, `GET`/`HEAD` do not ("copy-in writes; copy-out reads"). `fsop` gates by op, with
`stat`/`list`/`get` explicitly on the open list.

Scratch test with `CORNUS_API_POLICY={"ci-bot":["deploy"],"nobody":[]}`, calling as
`nobody` — an authenticated identity granted **nothing**:

```
UNGATED GET  /.cornus/v1/deploy/web/archive?path=/etc/shadow -> 200
UNGATED HEAD /.cornus/v1/deploy/web/archive?path=/           -> 200
UNGATED POST /.cornus/v1/deploy/web/fsop?op=get&path=...     -> 501   (past the gate; fake backend has no FSOperator)
UNGATED POST /.cornus/v1/deploy/web/fsop?op=list&path=/      -> 501   (ditto)
UNGATED GET  /.cornus/v1/deploy/web/logs                     -> 200
UNGATED GET  /.cornus/v1/deploy                              -> 200
```

Control passed: `POST /.cornus/v1/deploy/web/stop` returned 403 in the same run, so the
policy was loaded and enforcing. The two `501`s are the fake backend declining, i.e. the
request had already cleared authorization.

So under a configured policy an identity with an empty action list can enumerate every
deployment, read every workload's logs, and **tar arbitrary paths out of any container's
filesystem**. That is a documentation-visible design choice ("pure reads"), but "pure read"
is doing a lot of work for an endpoint that is `docker cp` out of somebody else's container.

### H1 — CONFIRMED: the auto-started builder carries no credential

`pkg/build/builderctr/builderctr.go:392-445`. The container create body sets
`"Privileged": true`, `"NetworkMode": "host"`, `RestartPolicy: unless-stopped`, and in the
default re-export mode bind-mounts the **host Docker socket**. Its `Env` is exactly
`["CORNUS_BUILDER_AUTO=false"]` (plus `CORNUS_DEPLOY_BACKEND=dockerhost` in re-export mode).
No `CORNUS_AUTH_TOKEN`, no JWT material, nothing.

So the builder serves a **fully unauthenticated** cornus API on `127.0.0.1:5099` — and
because of `NetworkMode: host` that is the *host's* loopback, reachable by every local
process — backed by a privileged root container holding the host Docker socket. It starts
automatically on first build (`CORNUS_BUILDER_AUTO` defaults on) and survives the server
exiting.

**B2 and H1 compose into a clean local privilege escalation, in the default re-export
mode.** That mode is the one that sets `CORNUS_DEPLOY_BACKEND=dockerhost` and binds the host
Docker socket into the builder, so the builder's deploy API drives the host's daemon. Any
local user reaches `127.0.0.1:5099` and posts a deploy carrying `pidMode: host` + `capAdd:
[SYS_ADMIN, SYS_PTRACE]` (or `devices: [/dev/sda:/dev/sda]`). The builder's own policy is
`PolicyFromEnv`, i.e. default-deny — which stops `Privileged` and host binds and stops
neither of those. Fixing either B2 or H1 breaks the chain; both should be fixed.

In CAS mode (an explicit `--storage`) no socket is mounted, so that exact chain does not
close — but the builder is still an unauthenticated build API running privileged with host
networking, and a build is arbitrary code execution by definition. H1 stands on its own
account in both modes.

### Net effect on the priorities

Nothing in the review is retracted. Two upgrades:

- **B2 should be treated as the first fix**, ahead of B1. B1 is a default that operators can
  correct today with `--addr`; B2 is a control that does not do what its name, its comments,
  its docs, and its LTM entry all say it does — and it is load-bearing precisely because the
  API is unauthenticated by default.
- **H1 moves up to blocker.** An unauthenticated privileged root daemon that starts by
  itself is not conditional on any operator mistake.
