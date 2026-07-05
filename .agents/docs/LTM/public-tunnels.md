# Public Tunnels (`cornus tunnel`)

## Summary

`cornus tunnel <name> <port>` exposes a deployed workload to the public internet
through a hosted tunnel, returning a public URL instead of the local listener that
`cornus port-forward` gives. The server hosts the tunnel in-process and bridges each
inbound connection to the workload via `deploy.Backend.ForwardPort` — the same
byte-bridge port-forward uses, so it reaches unpublished ports on any deploy backend
(see [[port-forwarding]] and [[deploy-backend-contract]]). The backend is pluggable
behind `pkg/tunnel`; ngrok, ssh, cloudflare, and tailscale are default-linked and
selected by `CORNUS_TUNNEL_BACKEND` (2026-07-08; tailscale added 2026-07-10). The CLI's
credential handling is deliberately conservative (never argv-only, never silently retained
longer than necessary) and the `ssh` backend supports forwarding the caller's local
`ssh-agent` into the server-side handshake via a small reusable tunnel side-channel.

## Key Facts

- CLI: `cornus tunnel <name> <port>` (`cmd/cornus/tunnel.go`, `TunnelCmd`). The client
  injects the tunnel credential into the server's already-authenticated endpoint
  (`POST /.cornus/v1/deploy/{name}/tunnel`, gated on the `deploy` API-policy action) — the
  server cannot know the credential beforehand.
- The credential never has to cross argv or shell history: `--authtoken-file FILE` (mutually
  exclusive with `--authtoken`) reads it from a file, and the client-side kong env binding is
  the generic `CORNUS_TUNNEL_AUTHTOKEN` (not an ngrok-specific name), matching the same
  variable name the server already reads for its own default-credential fallback.
- `cornus tunnel --forward-agent` (ssh backend only) forwards the caller's local `ssh-agent`
  into the server-side SSH handshake over a dedicated, purpose-tagged WebSocket channel — see
  "SSH-agent forwarding" below.
- `pkg/server` depends ONLY on the `pkg/tunnel` interface. Concrete backends are
  blank-imported in `cmd/cornus/main.go`
  (`_ "cornus/pkg/tunnel/{ngrok,ssh,cloudflare,tailscale}"`), so a backend can be
  swapped without touching the server.
- `CORNUS_TUNNEL_BACKEND` selects the backend (default `ngrok`). `CORNUS_TUNNEL_AUTHTOKEN`
  is an optional server-side default credential.
- Two provider shapes behind the seam: `Provider`/`Session` (listener model, the backend
  yields a `net.Listener` and the manager `Accept`s) and `UpstreamProvider`/`UpstreamSession`
  (`StartUpstream(...upstreamURL)`, for backends that only proxy to a local upstream).
- Tunnels are torn down on `DELETE /.cornus/v1/deploy/{name}` and on server shutdown
  (`closeResources` -> `tunnelManager.closeAll`).
- Tailscale Funnel ships as a `tailscale` CLI **subprocess** backend (`UpstreamProvider`),
  NOT the in-process tsnet listener — adding `tailscale.com` to go.mod would collide with
  cornus's pinned `k8s.io/*` versions across the whole module (see Pitfalls). The CLI
  subprocess sidesteps the version graph entirely (2026-07-10).

## Details

### The `pkg/tunnel` seam (`pkg/tunnel/tunnel.go`)

- `Credential struct` — the injected per-tunnel credential (authtoken / SSH key PEM /
  password, depending on backend).
- `Provider interface` — listener model; opens a `Session`.
- `Session interface` — has `Accept() (net.Conn, error)`, `URL()`, `Close()`.
- `UpstreamProvider interface` — `StartUpstream(ctx, cred, opts, upstreamURL)` for
  backends that can only forward to a local upstream URL, yielding an `UpstreamSession`.
- `CredentialOptional interface` — an optional marker a backend implements to declare it
  is anonymous, so the endpoint skips the token-required 400.
- `Factory func() (any, error)` and `Open(name string) (any, error)` return `any`; the
  server type-switches on `Provider` vs `UpstreamProvider`. `Register(name, Factory)`
  populates the registry; `Backends()` lists registered names.
- `ListenerSession struct` — an adapter that wraps a `net.Listener` + a `PublicURL` string
  into a `Session` (`URL`, `Accept`, `Close`), used by listener-model backends.

### Server manager and endpoint (`pkg/server/deploy_tunnel.go`)

- `tunnelManager` (`newTunnelManager(backend, defaultToken)`) holds live sessions and
  severs in-flight `ForwardPort` bridges on teardown.
- `start(backend, name, token, port, proto)` type-switches on the opened backend:
  - `Provider`: opens the `Session` and uses `session.Accept` directly.
  - `UpstreamProvider`: stands up a local **shim listener** (`net.Listen("tcp",
    "127.0.0.1:0")`) that bridges to `Backend.ForwardPort`, then hands its address to the
    backend as `"http://"+shim.Addr().String()` via `StartUpstream`; `accept = shim.Accept`.
    The shim is closed as part of session teardown.
- `serve(...)` accept-loop bridges each accepted `conn` to `backend.ForwardPort(ctx, name,
  port, "tcp", conn)`.
- `credentialOptional()` consults the `tunnel.CredentialOptional` marker; if the backend is
  anonymous, the endpoint does not require an injected token.
- `stop(name)` / `teardown(name, ts)` close one session (its backend tunnel and any shim
  listener); `closeAll()` tears down every live tunnel on server shutdown.
- `handleDeployTunnel(w, r, backend, name)` is the HTTP handler for
  `/.cornus/v1/deploy/{name}/tunnel` (POST create, GET status, and DELETE-driven teardown),
  gated on the `deploy` API-policy action (see [[auth-and-security]]).

### Backends

- **ngrok** (`pkg/tunnel/ngrok`, `Provider`, default): built on `golang.ngrok.com/ngrok/v2`,
  in-process, NO subprocess. Fails fast on a missing token.
- **ssh** (`pkg/tunnel/ssh`, `Provider`, default-linked, light): SSH remote-forward via
  `golang.org/x/crypto/ssh` `client.Listen` — no new heavy dependency. Works with
  sish/serveo/pinggy/localhost.run and plain sshd. The injected credential is an SSH key
  PEM or password. URL comes from the service's session banner
  (`CORNUS_TUNNEL_SSH_URL_FROM_SESSION`) or a `{port}` template
  (`CORNUS_TUNNEL_SSH_URL_TEMPLATE`). Server-side config env vars:
  - `CORNUS_TUNNEL_SSH_ADDR` (required, host:port), `CORNUS_TUNNEL_SSH_USER`,
    `CORNUS_TUNNEL_SSH_BIND` (remote bind, default `0.0.0.0:0`).
  - Host-key verification is **fail-closed**: it errors unless one of
    `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` (file path), `CORNUS_TUNNEL_SSH_HOSTKEY`
    (authorized_keys-format line / pinned key), or `CORNUS_TUNNEL_SSH_INSECURE=1`
    (dev-only opt-out) is set.
- **cloudflare** (`pkg/tunnel/cloudflare`, `UpstreamProvider`, default-linked, NO compile
  dependency): shells out to `cloudflared tunnel --url <shim>` (quick tunnel), parses the
  `*.trycloudflare.com` URL from stderr; `Close` kills the subprocess. Binary overridable
  via `CORNUS_TUNNEL_CLOUDFLARED_BIN`.
- **tailscale** (`pkg/tunnel/tailscale`, `UpstreamProvider`, default-linked, NO compile
  dependency): shells out to `tailscale funnel <shim-port>` in the foreground, which proxies
  `https://<node>.ts.net/` to the shim and prints the public URL; the backend parses the
  `*.ts.net` URL and `Close` kills the subprocess (which tears the Funnel config down). Binary
  overridable via `CORNUS_TUNNEL_TAILSCALE_BIN`. `CredentialOptional` — the node joins the
  tailnet out-of-band (`tailscale up` + the funnel node attribute in the tailnet ACL policy),
  so cornus injects no per-tunnel credential. LIMITATION: Funnel serves one config on the
  node's HTTPS port (443) at a time, so concurrent tunnels on a single node conflict (a
  Funnel/node property, not a cornus bug) — fine for the ad-hoc dev/test use case. A
  Kubernetes Helm deployment can run `tailscaled` as a sidecar rather than requiring a custom
  image or an interactive host `tailscale up` — see "Tailscale Helm sidecar" below.

### Credential-safety CLI (`cornus tunnel`)

`--authtoken-file FILE` (mutually exclusive with `--authtoken`) reads the credential from a
file instead of argv/shell history, via a pure, testable `resolveAuthToken(token, tokenFile
string) (string, error)` helper (`cmd/cornus/tunnel.go`, `cmd/cornus/tunnel_test.go`). The
client-side kong env binding on `--authtoken` was renamed from the ngrok-specific
`NGROK_AUTHTOKEN` to the generic `CORNUS_TUNNEL_AUTHTOKEN`, since the same flag now covers
ssh/cloudflare/tailscale too — deliberately reusing the exact variable name the server already
reads for its own default-credential fallback (`CORNUS_TUNNEL_AUTHTOKEN`, read by
`pkg/server/deploy_tunnel.go`'s `newTunnelManager`), since it is the same kind of value in two
different processes' environments, not a naming collision. `resolveAuthToken` still falls back
to `os.Getenv("NGROK_AUTHTOKEN")` when none of `--authtoken`/`--authtoken-file`/the new env var
is set, so existing recipes/muscle memory keep working. Guides recommend a server-side shared
credential for the ssh backend (the handshake happens server-side, so a per-caller personal key
rarely makes sense) rather than passing a private key on the command line.

### SSH-agent forwarding (`--forward-agent`, ssh backend only)

The `ssh` tunnel backend's outbound SSH handshake happens on the **server**
(`pkg/tunnel/ssh`'s `Start` dials the relay from `cornus serve`, not from the client), so
authenticating with a client-held `ssh-agent` (a passphrase-protected key, a hardware key, a
centrally managed identity) needed a way to reach across that boundary. The mechanism is a
small, deliberately generic tunnel side-channel, not an SSH-agent-specific pipe: `GET
/.cornus/v1/deploy/{name}/tunnel/channel/{purpose}` (WS upgrade via the existing
`wire.AcceptConn`/`wire.DialConn` single-stream helpers already used by exec/attach/
port-forward) plus a client `Client.TunnelChannel(ctx, name, purpose)` helper. Only the
`"ssh-agent"` purpose is recognized today; the same endpoint/method is reusable for a future
purpose string.

The server's `tunnelManager` keeps a `channels map[string]map[string]*pendingChannel` registry
(`pkg/server/deploy_tunnel.go`): opening the channel registers a pending connection and blocks
until a matching tunnel-start POST claims it, a 30s TTL elapses, or the request's context is
cancelled (`registerChannel`/`claimChannel`/`waitForChannel`/`dropChannel`). `waitForChannel`
polls (50ms ticks, 10s cap, overridable via a package `var` for fast tests) to cover the
ordinary race between the two independent HTTP requests — the channel dial happens first,
client-side, then the tunnel-start POST. `tunnel.Credential` gained an `Agent agent.Agent`
field (`golang.org/x/crypto/ssh/agent`) alongside the existing `AuthToken string`; backends
that don't care (ngrok/cloudflare/tailscale) simply ignore it. `pkg/tunnel/ssh`'s
`authMethods(token string, ag agent.Agent)` builds an ordered `[]ssh.AuthMethod`: agent signers
first (`ssh.PublicKeysCallback`) when forwarded, then the existing PEM-or-password logic from
`token` — at least one of the two is still required. `tunnelManager.start` gained an `ag
agent.Agent` parameter threaded from `handleDeployTunnel`'s POST handler, which (when
`req.ForwardAgent`) calls `waitForChannel` and wraps the claimed conn with
`agent.NewClient(conn)`.

Security-relevant design choices, established as this project's credential-handling norm and
worth reusing for any future forwarding feature:
- `--forward-agent` is opt-in only, never inferred from `SSH_AUTH_SOCK` being set.
- `ForwardAgent: true` against a non-`ssh` backend is a hard 400, not a silent no-op.
- `ForwardAgent: true` with no channel ever arriving is a hard 400 after the bounded wait, not
  a silent fallback to token auth and not a hang.
- The claimed channel is released immediately after `tunnelManager.start` returns, bounding the
  forwarded-agent exposure window to the SSH handshake attempt itself, not the tunnel's full
  lifetime.
- CLI help text and the docs explicitly warn this carries the same trust exposure as `ssh -A`:
  while the channel is open, the server can ask the forwarded agent to sign *any* challenge it
  chooses, not only the relay's — which matters more than usual given cornus's own multi-tenant
  RBAC model ([[auth-and-security]]).

This closes a gap that had briefly been documented as an explicit non-goal (the same trust
tradeoff was the reason cited for not building it); the side-channel mechanism above resolves
it without changing that tradeoff — it only bounds its exposure window.

ja/zh doc sync for this feature (`docs/guides/tunnels.md`'s ssh section, `docs/cli/tunnel.md`'s
flag row/example) was deliberately deferred (English only).

### Tailscale Helm sidecar

`deploy/helm/cornus`'s opt-in `tailscale:` values block runs a `tailscaled` sidecar for the
`tailscale` tunnel backend, since `sudo tailscale up` is an interactive host command that
doesn't make sense run by hand against an ephemeral pod. `statefulset.yaml` gains: an
initContainer that `cp`s the `tailscale` CLI out of the `ghcr.io/tailscale/tailscale` sidecar
image onto a shared `emptyDir` (`tailscale-bin`), so the cornus container itself needs no
custom image; the `tailscaled` sidecar in **userspace networking mode** (`TS_USERSPACE=true`
— Funnel only proxies a local port through the control connection, needing neither
`NET_ADMIN` nor a TUN device); a state dir on its own `emptyDir`, not persisted across
restarts (`values.yaml`'s doc comment calls out that `authKeySecret` should be a reusable,
ephemeral-tagged key as a result); and env/volumeMount wiring on the cornus container itself
(`CORNUS_TUNNEL_BACKEND=tailscale`, `CORNUS_TUNNEL_TAILSCALE_BIN=/var/lib/tailscale-bin/tailscale`,
`TS_SOCKET` pointing at the shared control-socket `emptyDir`). A render-time `fail` guards
`tailscale.enabled` without `tailscale.authKeySecret`. Validated with `helm lint`/`helm
template` (rendered pod spec inspected directly) only — not yet verified against a live
cluster, so whether Funnel actually works over the shared socket in userspace mode is
unconfirmed beyond what the chart renders.

### Why ngrok, not Microsoft Dev Tunnels

Dev Tunnels was the original ask but was rejected on two grounds: the Dev Tunnels Go SDK
is client/connect-only (no host impl — hosting lives only in the C#/TS/Rust SDKs and the
closed `devtunnel` CLI), and decisively the Dev Tunnels EULA (`aka.ms/devtunnels/tos`)
§1(a)/§5(f) scope use to "Microsoft Visual Studio and successor Microsoft products" and
forbid combining it with your app "for others to use" — so a third-party product
bundling/hosting it is outside the grant. ngrok's ToS instead permits embedding the agent
to offer tunnels to your users (as "Customer Licensees") when you maintain the ngrok
account (or, with prior written consent, when users bring their own).

## Files

- `pkg/tunnel/tunnel.go` (+ `tunnel_test.go`) — the seam: `Provider`/`Session`,
  `UpstreamProvider`/`UpstreamSession`, `Credential`, `CredentialOptional`,
  `ListenerSession`, `Factory`/`Register`/`Open`/`Backends`.
- `pkg/tunnel/ngrok/ngrok.go` — in-process ngrok backend.
- `pkg/tunnel/ssh/ssh.go` (+ `ssh_test.go`) — SSH remote-forward backend, fail-closed host-key
  verification, `authMethods(token, agent.Agent)` agent-then-token auth ordering.
- `pkg/tunnel/cloudflare/cloudflare.go` — `cloudflared` quick-tunnel subprocess backend.
- `pkg/tunnel/tailscale/tailscale.go` (+ `tailscale_test.go`) — `tailscale funnel` subprocess
  backend (URL/target parsing unit-tested; no subprocess in tests).
- `pkg/server/deploy_tunnel.go` (+ `deploy_tunnel_test.go`) — `tunnelManager`, shim
  listener, `handleDeployTunnel` endpoint, the `channels` pending-channel registry
  (`registerChannel`/`claimChannel`/`waitForChannel`/`dropChannel`) backing
  `--forward-agent`.
- `cmd/cornus/tunnel.go` (+ `tunnel_test.go`) — `TunnelCmd`, `--authtoken-file`,
  `resolveAuthToken`; blank-imports the four backends in `cmd/cornus/main.go`.
- `pkg/e2e/harness.go` — the `tunnel(name, port, server?, proto?)` builtin.
- `e2e/scenarios/deploy-tunnel.star` — the opt-in ngrok E2E scenario.
- `e2e/scenarios/deploy-tunnel-tailscale.star` — the opt-in tailscale-Funnel E2E scenario
  (`CORNUS_TUNNEL_TAILSCALE_E2E`).
- `deploy/helm/cornus/templates/statefulset.yaml`, `deploy/helm/cornus/values.yaml` — the
  opt-in `tailscale:` Helm values block and `tailscaled` sidecar wiring.
- `docs/guides/tunnels.md`, `docs/cli/tunnel.md` — per-backend setup recipes and the
  `--forward-agent`/`--authtoken-file` CLI reference (see [[user-reference-docs-site]] for the
  docs-site restructuring that produced this Guide).

## Test Coverage

- Unit: `pkg/tunnel` registry (Register/Open/unknown + `Provider`/`UpstreamProvider`
  type-switch). `pkg/tunnel/ngrok` registration + missing-token fast-fail + opt-in live
  test gated on `NGROK_AUTHTOKEN` (skipped by default, keeps `go test ./...` offline).
  `pkg/tunnel/ssh` auth selection, **fail-closed host-key** verification, URL/template/
  boundPort helpers. `pkg/tunnel/cloudflare` quick-tunnel URL parsing from a captured
  transcript (no subprocess).
- `pkg/server`: manager-bridges-to-backend (net.Pipe visitor -> echo via `ForwardPort`),
  endpoint POST/GET/DELETE + validation, server-default-token, `deploy`-action authz
  (403 stranger / past-gate for ci-bot), and `TestTunnelManagerUpstreamShim` (a fake
  `UpstreamProvider` dials the shim -> bridge to `ForwardPort`).
- E2E: the `tunnel(name, port, server?, proto?)` harness builtin backgrounds
  `cornus tunnel`, parses the printed public URL, returns it, and kills it on teardown;
  `predeclared()`/`predeclaredNames()` kept in sync (`TestPredeclaredNamesInSync`).
  `e2e/scenarios/deploy-tunnel.star` deploys nginx with an UNPUBLISHED `:80`, opens a
  tunnel, and `http_get`s the public `https://` URL asserting nginx — exercising the full
  CLI -> server -> ngrok relay -> server -> backend -> container path. It is opt-in
  (skipped unless `NGROK_AUTHTOKEN` is set, and on the `local` target — mirroring
  `TestNgrokLive` gating), added to the Makefile `SCENARIOS` list, and documented in the
  TESTING.md builtin reference. Run the real path with
  `NGROK_AUTHTOKEN=… make e2e-docker`.
  `e2e/scenarios/deploy-tunnel-tailscale.star` is the same proof on the tailscale backend:
  it boots the server with `serve(env={"CORNUS_TUNNEL_BACKEND": "tailscale"})`, opens a
  Funnel to the unpublished `:80`, and asserts a `*.ts.net` URL reaches nginx. It is
  anonymous (no injected credential), so it gates on `CORNUS_TUNNEL_TAILSCALE_E2E` (a
  joined, Funnel-enabled tailnet node is required — hence a plain opt-in flag, not a
  credential). Run it with `CORNUS_TUNNEL_TAILSCALE_E2E=1 make e2e-docker`.
- SSH-agent forwarding: `pkg/tunnel/ssh/ssh_test.go`'s `TestAuthMethodsAgent` (agent-only and
  agent+token cases, using `agent.NewKeyring()`). `pkg/server/deploy_tunnel_test.go`:
  `TestTunnelChannelWaitAndClaim` (the claim race), `TestTunnelChannelDroppedOnCancel`
  (cleanup on context cancellation, standing in for the real 30s TTL), `TestDeployTunnelForwardAgent`
  (end-to-end over HTTP, proving the provider receives a non-nil `Credential.Agent`),
  `TestDeployTunnelForwardAgentNoChannel`, `TestDeployTunnelForwardAgentWrongBackend`,
  `TestDeployTunnelChannelUnknownPurpose`.
- Credential-safety CLI: `cmd/cornus/tunnel_test.go` covers `resolveAuthToken`'s precedence
  (`--authtoken` / `--authtoken-file` mutual exclusion / `CORNUS_TUNNEL_AUTHTOKEN` /
  legacy `NGROK_AUTHTOKEN` fallback).
- Tailscale Helm sidecar: validated with `helm lint` and `helm template` only (rendered pod
  spec inspected directly); no live-cluster run.

## Pitfalls

- **Tailscale Funnel ships as a CLI subprocess, NOT tsnet.** tsnet fits the listener model
  perfectly (`ListenFunnel` returns a `net.Listener`), but adding `tailscale.com` to go.mod
  forces `k8s.io/*` v0.32.1 -> v0.34.0 (and buildkit/containerd churn) across the WHOLE
  module: Go's MVS is build-tag-agnostic, so a `//go:build tunnel_tailscale` tag would gate
  *compilation* but not the module version graph. cornus deliberately pins those k8s
  versions (baseline: buildkit v0.18.2, containerd v1.7.24, k8s v0.32.1), so tsnet is
  unacceptable as a side effect of an optional backend. The shipped backend (2026-07-10)
  therefore shells out to the `tailscale funnel` CLI instead — zero compile dependency, the
  same route the cloudflare backend takes. A tsnet-based in-process backend remains possible
  only via a separate Go module/plugin.
- **Funnel is single-config-per-node on port 443.** Unlike cloudflare quick tunnels (each
  subprocess gets a unique `*.trycloudflare.com` URL), `tailscale funnel` binds the node's
  one HTTPS port, so two concurrent tailscale tunnels on the same node conflict. This is a
  Funnel property; the backend does not try to multiplex.
- **SSH host-key verification is fail-closed by design.** Omitting all of
  `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` / `CORNUS_TUNNEL_SSH_HOSTKEY` /
  `CORNUS_TUNNEL_SSH_INSECURE` is an error, not a silent MITM-vulnerable connect.
- **The credential is client-injected, never server-known.** The server hosts the tunnel
  but the authtoken/key rides in on the authenticated `POST /.cornus/v1/deploy/{name}/tunnel`;
  a `CredentialOptional` backend is the only way the endpoint tolerates a missing token.
- **`UpstreamProvider` backends leak without shim teardown.** The shim listener stood up
  for the upstream model must be closed as part of session teardown, else its accept
  goroutine is reachable by neither `stop` nor `closeAll`.
- **Never zero a Go string's backing bytes via `unsafe` for "secure credential wiping."** A
  defense-in-depth attempt to scrub a consumed credential's memory (`Credential.Zero()` /
  `zeroString`, applied only to the ssh backend's synchronous one-shot handshake and only for
  request-owned tokens — the ngrok backend's `Agent` was confirmed via source review to retain
  its own copy of the authtoken for reconnection, so it was deliberately excluded) crashed the
  server test suite with an unrecoverable `fatal error: fault` (SIGSEGV,
  `runtime.memclrNoHeapPointers`) the moment a zeroed token happened to be a Go string literal.
  Root cause: Go string literals live in read-only rodata memory (sometimes interned), so
  writing to a literal's backing bytes via `unsafe.Slice(unsafe.StringData(s), len(s))` is an
  immediate OS-level memory-protection fault — and `fatal error: fault` is **not** a recoverable
  panic; `defer`/`recover()` cannot stop it, so any accidental literal anywhere in the call
  chain (a test, a default value, a future refactor) crashes the entire server process, not just
  one request. That risk (an unauthenticated DoS triggerable by a coincidental string literal)
  is strictly worse than the problem being solved, so the whole feature was reverted rather than
  patched around. Go 1.26 ships an experimental `runtime/secret` package
  (`secret.Do(f func())`, erases stack/register/heap allocations used during `f`'s call tree
  once unreachable) that would be aliasing-safe, but it is gated behind
  `//go:build goexperiment.runtimesecret`, needs `GOEXPERIMENT=runtimesecret` at build time, and
  carries no Go 1 compatibility promise — not worth opting the whole toolchain build into for
  one call site; kept the plain, un-zeroed `string` credential flow. **Takeaway:** never mutate
  a Go string's backing bytes via `unsafe.StringData`/`unsafe.Slice` without an ironclad
  guarantee the string cannot be a literal or otherwise-interned value anywhere in its
  provenance — the failure mode is an unrecoverable process crash, not a soft error, and the
  invariant is easy to violate by accident.
