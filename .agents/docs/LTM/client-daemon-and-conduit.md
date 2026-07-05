# Client Session Conduit Modes and the Unified Daemon Agent

## Summary

Two tightly-coupled client-side subsystems. (1) A selectable **session conduit mode** lets a `--server` client expose remote workloads either through per-port forwards (the default) or through one client-side SOCKS5 split-tunnel proxy, both hidden behind a single `clientconduit.Conduit` interface. (2) A **unified background agent** (`cornus daemon agent`) merges the former per-project `cornus daemon mounts` and standalone `cornus daemon docker` daemons into one long-lived per-user process reached over a single control socket, built on an in-house supervisor-tree framework. The two intersect at `dockerproxy.New(WithConduit(...))`, so one shared SOCKS5 proxy can span both docker containers and compose services by name. See [[compose-cli]], [[dockerd-proxy]], [[port-forwarding]], [[public-tunnels]].

## Key Facts

- (2026-07-18) A THIRD destination kind joins tunnel-inward and direct-egress: a **published local
  name**. `socks5.Result` is now a `Kind` sum (`KindDirect`/`KindService`/`KindLocal`); `Router.locals`
  (a `host:port` -> `LocalDialer` table, guarded by the existing `aliasMu`) is consulted BEFORE the
  rules, so a published name is unshadowable by any rule. `Conduit.AddLocal(ctx, host, port,
  LocalDialer)` publishes/withdraws it. The agent uses this for `cornus web --publish-in-conduit`:
  `web-serve` hosts the web BFF on a `pkg/memlisten` addressless listener and publishes it as
  `cornus.internal` in the SHARED conduit, so one browser proxy setting reaches workloads and the UI.
  Liveness is a held control connection (kernel closes fd on SIGKILL -> agent reaps); `idleCheck` counts
  `len(a.webs)`. See [[web-ui]].
- (2026-07-18) Security: `socks5.Start` refuses a non-loopback bind unless `WithAllowNonLoopback`
  (`cornus socks5 --allow-non-loopback`, `Config.Socks5AllowNonLoopback`), and off-host wraps the direct
  dialer to refuse loopback/link-local targets — closing a pre-existing open-proxy hole
  (`CORNUS_CONDUIT=socks5://0.0.0.0:1080`).


- Conduit is client-side only — no server or API changes. It reuses `portfwd.Dialer.PortForward(name, port, "tcp")`, which reaches a deployment by name on any of the three backends; SOCKS5 CONNECT is TCP, aligned with all backends.
- Three modes behind one interface: `port-forward` (default, per-service binds), `socks5` (one split-tunnel proxy), and `ModeNone` (`--no-forward-ports`).
- One agent process per user; control socket at `$XDG_RUNTIME_DIR/cornus/agent.sock`, override with `CORNUS_AGENT_DIR`.
- The agent is env-frozen at spawn: it reads no `CORNUS_*` tri-state variables itself. Clients pre-resolve `CORNUS_TOKEN` / `CORNUS_VIA_SERVER` / `CORNUS_CONDUIT` into a `connSpec` and send it, including an ABSOLUTE config path.
- Conduit mode precedence resolves flag > `CORNUS_CONDUIT` > connection profile > `port-forward` (mirrors `ViaServer`).
- `go.mod`: only `golang.org/x/net` was promoted to a direct require (the E2E SOCKS5 client); no new deps for the agent work.

## Details

### Conduit modes (`pkg/clientconduit`, `pkg/socks5`)

`clientconduit.Conduit` is the single interface both modes sit behind: `Banner()`, `Add(name, ports)`, `Close()`.

- Port-forward conduit binds per service. `Add` ties each group to the per-call ctx so a stopped service self-closes. `portForwardConduit.Add` drops each `*portfwd.Group` from its tracking slice on the group's ctx-cancel, so a long-lived shared agent conduit does not accumulate dead groups over docker run/rm and compose up/down cycles.
- SOCKS5 conduit runs one proxy; its `Add` is a no-op.

`pkg/socks5` is a hand-rolled no-auth + CONNECT proxy (deliberately NOT `armon/go-socks5`):

- `Router` resolves the CONNECT `host:port` subject against ordered `{pattern -> replacement}` rules, first match wins. A match rewrites to `service:port` and is tunneled through the port-forward dialer; no match falls through to a direct `net.Dial` (this is the "split" in split-tunnel).
- The default rule strips a service-host suffix (`.cornus.internal`). Replacements accept sed-style `\1` backrefs (translated to Go `$1` by `translateReplace`) and can remap the port.
- `Proxy` lifecycle mirrors `portfwd.Group`.
- A zero-length CONNECT domain (host `""`) is rejected, not dialed as the proxy host's own `:port`.

Config lives in the connection profile: `clientconfig.Context.Conduit` = `Mode` + `Socks5{Listen, ServiceHostSuffix, Resolve[]}`. Set via `cornus config set-context --conduit-mode / --socks5-listen / --socks5-service-host-suffix / --socks5-resolve`. `clientconn.Conn.ConduitMode` / `ConduitConfig` do the precedence resolution above.

Surfaces: `cornus deploy --server`, `cornus compose up` (foreground and `-d`), and a standalone `cornus socks5`. An explicitly-requested socks5 conduit that fails to start (bind conflict, bad `CORNUS_CONDUIT`) fails the deploy rather than silently downgrading to no conduit; the failure aborts the session immediately via `stop()` from the Ready callback so it is reported even when the outcome surfaces as a cancel (e.g. Ctrl-C), not swallowed until teardown.

### The unified agent (`cornus daemon agent`)

Built on an in-house lifecycle framework rather than hand-rolled `recover()` / signal scatter.

**Framework — `cmd/cornus/internal/supervisor`**: a supervisor tree. `Service` / `ServiceFunc`, `Policy` (`RemoveOnExit` | `Restart`-with-backoff), and `Add` / `AddSystem` / `Remove` / `Count` / `SetIdleHook`. Each child is isolated by `recover()` (the correct primitive here; `errgroup` would cancel all siblings on one failure).

**Process lifecycle — `cmd/cornus/internal/agentproc`**: `Discover` / `EnsureRunning` (flock-serialized spawn via `daemonize`, with a double-check under the lock), `Listen`, `Stop` (control-then-`signalAndWait`), and the state file. Fully unit-tested.

**Agent core — `cmd/cornus/internal/clientagent`**: the reusable session core (`Project` / `svcSession` / `specFingerprint`, moved out of composecli, with the socks5 special-case collapsed into `conduit.Add`) plus `Agent`. `Agent` owns one control socket, a supervisor root, and per-server `connState` (a shared `Conn` + `Conduit`, refcounted by `connKey` / `conduitKey`). Control actions: `ping`, `up`, `down`, `docker-serve`, `docker-stop`, `web-serve`, `web-stop`, `status`, `stop` (protocol v3). `web-serve` is the one action that HOLDS its control connection (liveness), handled in `handle` rather than `dispatch`. Idle-exit runs through the supervisor idle hook. New hidden commands: `cornus daemon agent` plus `daemon status` / `daemon stop`. `clientconn.Resolver.ResolveWith(server, token)` lets the frozen-env agent resolve each client's token explicitly.

**Cutover**: `compose up -d` / `down` and `cornus daemon docker` became thin agent clients (ping-to-reuse + `EnsureRunning` + Send). `MountsCmd` was retired to a hidden stub; the per-project socket / state file and the compose `signalAndWait` were removed. This also fixed a pre-existing auth gap — the old mounts daemon ran `client.New(host)` with no token / TLS / kube-auth.

**Conduit-unify**: `dockerproxy.New(WithConduit(...))`. `containerRecord` swapped its `*portfwd.Group` for a generic `cleanup func()`, and `Proxy.Close()` was added. The docker frontend uses the shared per-server conduit, so one SOCKS5 proxy spans docker containers and compose services by name. In socks5 mode `docker run -p` has no local listener — reach the container through the proxy. (`cornus daemon docker` was previously left port-forward-only precisely because SOCKS5 breaks the `docker run -p` localhost-port contract; the unified agent is where that resolved.)

### Durable correctness fixes (from adversarial self-review, 2026-07-08)

- **Cold-start idle race.** The idle timer was armed at spawn while the counted child that keeps the agent alive was added only AFTER a slow `resolve` (kube token mint + svcforward) under `a.mu`, so a first `up` / `docker-serve` taking longer than the idle window could idle-exit the agent mid-request. Fix: an `inflight` counter (`beginRequest` / `endRequest` around `doUp` / `doDockerServe`) and an `idleCheck` that exits only when `inflight == 0 && no projects && no dockers`.
- **Docker frontend crash-orphan.** A `RemoveOnExit` `http.Server` child that exited on its own orphaned the map entry, its conn/conduit refs, and the socket, and a later `docker-serve` on the same socket falsely returned OK. Fix: `reapDocker` on unexpected exit; `docker-serve` on an already-served socket now errors loudly.
- **reapDocker ctx leak.** The self-exit reap released refs but never cancelled the crashed docker child's ctx, parking its `<-ctx.Done()` `srv.Close` watcher until agent shutdown. Fix: `reapDocker` now `sup.Remove`s the (already self-forgotten) token, which cancels the ctx.
- **Shutdown double-release.** `closeAllConns` now also clears `a.dockers`, so a `docker-stop` racing SIGTERM finds nil rather than re-releasing.
- **connSpec cwd divergence.** Clients send an ABSOLUTE config path (`Resolver.AbsConfigPath`), so a relative `--config-file` resolves to the same file regardless of the agent's spawn-frozen cwd.

## Files

- `pkg/socks5/` — hand-rolled no-auth + CONNECT SOCKS5 proxy; `Router`, `Proxy`, `translateReplace`;
  `Kind`/`Result`, `Router.locals` + `RegisterLocal`/`lookupLocal`, `LocalDialer`, `WithAllowNonLoopback` + `loopbackGuard`.
- `pkg/memlisten/` — addressless in-process `net.Listener` (`DialLocal` satisfies `socks5.LocalDialer`); how the agent hands the proxy the web BFF with no bound port.
- `pkg/clientconduit/` — `Conduit` interface (`Add` + `AddLocal`), `portForwardConduit`, socks5 conduit, `ModeNone`; `Config.Socks5AllowNonLoopback`.
- `cmd/cornus/internal/clientagent/web.go` — `web-serve`/`web-stop`, `webFrontend`, `reapWeb`, the held-connection liveness in `handle`, and `agentSelfView`.
- `pkg/clientconfig/clientconfig.go` — `Context.Conduit` (`Mode`, `Socks5{Listen, ServiceHostSuffix, Resolve[]}`).
- `cmd/cornus/internal/clientconn/clientconn.go` — `Conn.ConduitMode` / `ConduitConfig` precedence; `Resolver.ResolveWith`, `Resolver.AbsConfigPath`.
- `cmd/cornus/internal/supervisor/` — supervisor tree, `Service` / `ServiceFunc`, `Policy`, idle hook.
- `cmd/cornus/internal/agentproc/` — `Discover` / `EnsureRunning` / `Listen` / `Stop`, flock spawn, state file.
- `cmd/cornus/internal/clientagent/` — session core (`Project` / `svcSession` / `specFingerprint`) and `Agent` (control socket, `connState`, actions, idle-exit, `reapDocker`).
- `cmd/cornus/socks5.go`, `cmd/cornus/socks5_test.go` — standalone `cornus socks5`.
- `cmd/cornus/daemon.go`, `cmd/cornus/commands.go` — `daemon agent` / `status` / `stop`, retired `MountsCmd` stub, docker/compose thin clients.
- `pkg/dockerproxy/proxy.go`, `containers.go`, `state.go` — `New(WithConduit)`, `containerRecord.cleanup`, `Proxy.Close()`.
- `e2e/scenarios/agent.star` — docker-only agent scenario (in the Makefile `SCENARIOS`).

## Test Coverage

- `supervisor` and `agentproc` are fully unit-tested; concurrency packages are additionally run under `-race`.
- Agent regression tests: `TestIdleCheckHonorsInflightAndWork` (cold-start idle race), the already-serving `docker-serve` error, `TestReapDockerReleasesRefs` (reap releases refs and cancels ctx).
- `pkg/socks5`: E2E-tested with `golang.org/x/net/proxy`'s SOCKS5 client against fake dialers; `TestProxyRejectsEmptyDomain`.
- E2E harness isolates each scenario's agent via a per-harness `CORNUS_AGENT_DIR` set at `serve()` and runs `cornus daemon stop` on teardown (it never touches a real agent). `agent.star` brings up compose `up -d` + `dockerd_up` on ONE agent, checks `daemon status` sees both, and `daemon stop` tears it all down.

## Pitfalls

- The agent resolves kube credentials (token mint, svcforward) with its own spawn-frozen `KUBECONFIG`. Two clients with different `KUBECONFIG`s but the same cornus context share the first client's kube resolution. Keep `KUBECONFIG` stable across a user's sessions, or use a static `CORNUS_TOKEN`. (Still strictly better than the old mounts daemon, which had no auth at all.)
- Concurrent `compose up -d` and `compose down` of the SAME project can race (down releases the conn/conduit while up adds a session). Serialize compose commands per project.
- A partial `compose down <svc>` can stop a shared socks5 proxy that other services still use. Likewise, `web-stop` (or closing a `cornus web --publish-in-conduit` hold connection) can drop the LAST conduit ref and stop the shared proxy out from under compose services that were relying on the web session's ref. Refcounting is correct; the behavior is surprising.
- The agent now hosts the web BFF's unauthenticated surface (exec, persistent terminals, compose-file write + apply) when a UI is published — a concentration of the existing per-command risk into the long-lived per-user process, not a new class.
- `down` no longer force-kills a wedged agent; use `cornus daemon stop` for that. A foreground `daemon docker` killed by SIGKILL leaks the frontend until `daemon stop`.
- The global `a.mu` is held across resolve/bind — a latency concern only, not correctness, for a single-user agent.
- The SOCKS5 handshake sets no I/O read deadline; an idle non-sending client parks a goroutine until proxy teardown (loopback-bound by default).
