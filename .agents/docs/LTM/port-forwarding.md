# Port Forwarding (`cornus port-forward`)

## Summary

`cornus port-forward` forwards a local port to a container port of a deployment's
first instance, tunneled through a cornus server so it works against any deploy
backend and reaches ports the workload never published. It fills the inner-loop gap
where dockerhost published ports only on the deploy host and the kubernetes backend
silently ignored `PortMapping.Host` (a workload was reachable only cluster-internally).
The implementation reuses the exec/attach raw-tunnel shape end to end and invents no new
transport. On top of the CLI, the shared `pkg/portfwd` engine AUTO-forwards published
ports on every client session surface: `DeploySpec.Ports[].Host` means "reachable on
the client at `127.0.0.1:<host>`" on all backends (dockerhost still additionally
publishes on the deploy host).

## Key Facts

- CLI: `cornus port-forward --server <url> <name> [LOCAL:]REMOTE ...` — forward one or
  more local TCP ports to container ports of the deployment's FIRST instance. `LOCAL:REMOTE`
  or a bare `PORT` (local == remote); `--address` selects the bind address (default
  `127.0.0.1`).
- Backend-agnostic: the forward is tunneled through the cornus server, so it reaches ports
  the workload never published and works on both dockerhost and kubernetes.
- TCP everywhere; UDP on dockerhost and containerd via a `/udp` mapping suffix (e.g.
  `5353:53/udp`). kubernetes stays TCP-only — `pkg/portfwd` probe-detects that and
  warns-and-skips UDP mappings there.
- `pkg/portfwd` is the shared engine: `portfwd.Start(ctx, dialer, name, ports, opts...)`
  binds one local listener per mapping and tunnels each accepted connection through the
  existing `client.PortForward` -> server `portforward` action -> `Backend.ForwardPort`
  path. Four surfaces publish through it automatically, each with a
  `--no-forward-ports` opt-out: `cornus deploy --server`, foreground `cornus compose up`,
  `cornus compose up -d` (daemon-held), and `cornus daemon docker` (per-container).
- One tunnel = one connection (exec's one-per-invocation model): each accepted local
  connection opens its own independent tunnel, so concurrent connections open concurrent
  tunnels. The forward lives only while the CLI process is connected.
- The kubernetes path needs NO sidecar and works from an out-of-cluster kubeconfig — it
  rides the `pods/portforward` SPDY subresource directly.
- **Direct-to-pod is the default for cluster profiles, server-proxy is the last resort**
  (2026-07-08). On kube the server tunnel uses the server's ServiceAccount, which usually
  lacks `pods/portforward` RBAC; the client instead forwards straight to the workload pod
  over `pods/portforward` SPDY with the developer's own kubeconfig (`pkg/kubefwd`), and
  falls back to the server proxy only when the direct dial fails pre-traffic. See
  [[kubernetes-backend]].

## Details

### Wire and server plumbing

The feature threads a raw tunnel from the CLI to a container port, reusing the exact shape
of exec/attach:

- `api.PortForwardConfig{Port, Protocol}` — a newline-JSON preamble, mirroring `AttachConfig`.
- `deploy.Backend.ForwardPort(ctx, name, port, proto, conn)` — a new `Backend` interface
  method that bridges `conn` to the named deployment's container port. Forced
  implementations exist in dockerhost, kubernetes, and the server's `fakeBackend`.
- Server: a `portforward` action on `/.cornus/v1/deploy/{name}/...` (no new route), gated on the
  `deploy` API-policy action like exec/attach. `handleDeployPortForward` =
  `wire.AcceptConn` + `readPreamble` + `backend.ForwardPort`.
- Client: `client.PortForward` mirrors `client.Attach` (`wire.DialConnControlHeader` +
  `writePreamble`, returning the raw `net.Conn`).
- CLI (`cmd/cornus/portforward.go`, registered in `main.go`): one `net.Listen` per mapping;
  each accepted connection opens its own tunnel and `wire.Pipe`s the two ends.
  `signal.NotifyContext` handles Ctrl-C teardown.

### Backend mechanics

- **dockerhost** — `engine.containerIP` does `GET /containers/{id}/json` and decodes
  `NetworkSettings.IPAddress`, falling back to the first per-network IP for containers on a
  user-defined network. `ForwardPort` dials `IP:port` and reuses the existing `bridge`
  splice, which tears down when either side closes (the `CloseWrite` half-close works on the
  real upstream `net.Conn`). It assumes the server can route to the Docker bridge — the same
  locality the backend already relies on. See the dockerhost backend for the surrounding
  engine plumbing.
- **kubernetes** — `ForwardPort` rides the `pods/portforward` SPDY subresource via
  `spdy.RoundTripperFor` + `spdy.NewDialer` (exactly the exec/attach SPDY pattern), creating
  the error and data streams (shared request id + `PortHeader`) and splicing the data stream
  — the same low-level handshake client-go's `portforward.handleConnection` does. This works
  from an out-of-cluster kubeconfig with no sidecar. See `kubernetes-backend.md` for the
  backend as a whole. The same SPDY `pods/portforward` mechanism is reused by `svcforward`
  (the automatic port-forward to the in-cluster cornus Service) documented in
  `remote-cluster-connection-ergonomics.md`.

### Automatic client-side forwarding (`pkg/portfwd`)

`portfwd.Start(ctx, dialer, name, ports, opts...)` returns a `Group`: one local listener
per TCP mapping, one tunnel per accepted connection (exactly the `cornus port-forward`
shape). The four auto-forward surfaces:

- **`cornus deploy --server`** — forwards start on the first `Ready` deploy event,
  guarded by a `sync.Once` (deliberately NOT coupled to `e.Status != nil`), and end with
  the session.
- **`cornus compose up` (foreground)** — services with published ports hold the process
  alive like mount services do (docker-compose-like); Ctrl-C releases the forwards and
  now also deletes the mount-free deployments the foreground `up` created (so terminating
  it stops everything it brought up, like `docker compose up`). This is a behavioral change:
  a mount-free `up` used to always return AND leave its deployments running; scripts that
  want them to persist should use `-d` or `--no-forward-ports` (those non-blocking returns
  do NOT delete).
- **`cornus compose up -d`** — port services are handed to the per-project background
  helper: `daemonService` gained `ForwardPorts`/`ForwardOnly` (`ForwardOnly` = the CLI
  already deployed fire-and-forget, the daemon holds only listeners) and
  `daemonResponse` gained `Forwards` so the CLI prints the daemon-side bound addresses.
- **`cornus daemon docker`** — `Proxy.start` publishes each container's PortBindings
  after `waitReady`; the group lives in `containerRecord.fwd` and is closed exactly-once
  inside `setExited`'s current-session guard (outside the lock — `Close` drains), so
  stop/rm/wait all release it and `docker start` re-publishes. `docker run -p 8080:80`
  behaves like local Docker. `dockerproxy.New` is variadic; `WithoutPortForwards()`
  opts out.

`cornus port-forward` itself is refactored onto the same helper; `WithStrictBind`
preserves its fatal-on-bind-failure contract (auto-forward surfaces are lenient).

Design points:

- **Skips are non-fatal**: a UDP mapping on an incapable path warns+skips, and a local
  bind failure warns+skips — which absorbs the client-and-server-on-one-host case where
  dockerhost's own `0.0.0.0:<host>` publish owns the port (confirmed live: the
  `127.0.0.1` bind DOES collide with docker-proxy's wildcard bind, EADDRINUSE).
- **Empty groups do not hold**: a group whose every mapping was skipped is closed
  immediately — critical in compose foreground, where an all-skipped group must not keep
  `up` from returning (caught live by `compose.star` hanging on the docker target).
- **Teardown severs in-flight tunnels**: `Group.Close` tracks accepted conns and closes
  them rather than draining, so a long-lived connection cannot hang session exit.
  `Close` also calls shutdown directly (not only via the ctx watcher) — the strict-bind
  cleanup path would otherwise deadlock waiting on accept loops nobody unblocks.
- The group's lifetime is tied to the caller's ctx (watcher goroutine), so Ctrl-C paths
  need no explicit Close; mount-session groups in the supervisor are additionally
  released by a `defer cancel()` when a DeployAttach dies on its own.

### Direct-to-pod forwarding vs. server proxy (`pkg/kubefwd`)

For cluster profiles, the client no longer always tunnels through the server
(`client.PortForward` -> WS -> `/.cornus/v1/deploy/{name}/portforward`) — that path runs under
the server's ServiceAccount, which usually lacks `pods/portforward` RBAC. Instead a
client-side dialer forwards straight to the workload pod with the developer's kubeconfig,
using the server proxy only as a fallback.

- `pkg/kubefwd`: `New(kubeContext, namespace) *Dialer` satisfies `portfwd.Dialer`. Its
  `PortForward` rejects UDP (pods/portforward is TCP-only, matching the kube backend),
  lazily loads and caches the kubeconfig (a load ERROR is cached too, so it is not retried
  every dial), resolves the pod via the shared `kubeclient.FirstPod`, and opens a FRESH
  SPDY connection per call — creating the error+data stream pair exactly like client-go's
  `PortForwarder.handleConnection`.
- The data stream is wrapped as a `net.Conn` (`podConn`: Read/Write/Close are real; the
  addr/deadline methods are inert stubs — all that `wire.Pipe` ever touches). `podConn.Close`
  closes the owning SPDY connection. An error-stream goroutine severs the conn on a
  mid-connection forwarding error.
- `kubefwd.Fallback{Primary, Secondary}` tries `Primary` (direct) then `Secondary` (proxy).
  It does NOT fall back on `ctx.Err()`, and only falls back on a PRE-TRAFFIC error, so no
  bytes are ever duplicated across the two dials.
- `clientconn.Conn.Dialer()` returns `kubefwd.Fallback{direct, proxy}` for a cluster
  profile (`KubeCluster` set) and the plain proxy client otherwise. Every client-side
  forward site is wired to it: `cornus port-forward`, `deploy` remote foreground
  (`commands.go` `runRemote`), and compose up foreground (`rt.forwardDialer`).
- The detached `cornus compose up -d` supervisor is covered too: `spawnDaemon` passes
  `--kube-context`/`--kube-namespace` to `daemon mounts`, and `runDaemon` rebuilds the same
  `Fallback` dialer, so direct-pod forwarding survives into the background helper — which
  therefore no longer depends on a live server port-forward for TCP.
- Refactor: the `cornus.app`-label pod resolver was extracted into `kubeclient.FirstPod`
  (wraps `deploy.ErrNotFound`); `pkg/kubelogs` now uses it instead of its own copy.

### UDP forwarding

- CLI mapping suffix: `[LOCAL:]REMOTE/udp` (e.g. `5353:53/udp`); implemented on
  dockerhost and containerd.
- The tunnel reuses the hub's datagram framing: `wire.BridgeDatagramStream` (2-byte
  length framing over the stream conn).
- Per-source flows: each local UDP source address gets its own tunnel, garbage-collected
  after 60s idle.
- A newline-JSON `api.PortForwardAck` is sent on udp dials ONLY (the TCP wire is
  unchanged), so incapable backends reject the dial cleanly instead of black-holing
  datagrams.
- kubernetes stays TCP-only (`pods/portforward` path); `pkg/portfwd` probe-detects the
  rejection and warns-and-skips UDP mappings.

### Why a real port-forward, not honoring `Host`

The kubernetes backend's `service()` builds only a ClusterIP Service, using the container
port for both port and targetPort — so `PortMapping.Host` was silently dropped and a
deployed workload was reachable only cluster-internally. That asymmetry versus dockerhost
(which publishes host ports) is the exact gap this feature fills. A CLI-driven port-forward
that reaches any container port, not a partial fix that honors `Host`, was the right move.

## Files

- `cmd/cornus/portforward.go` — the CLI command (one listener per mapping, per-connection
  tunnels, SIGINT teardown, `LOCAL:REMOTE`/`PORT` parsing incl. `/udp`, `--address`);
  registered in `cmd/cornus/main.go`; refactored onto `pkg/portfwd` with `WithStrictBind`.
- `pkg/portfwd/portfwd.go` + `portfwd_test.go` — the shared auto-forward engine
  (`Start`, `Group`, `WithStrictBind`, warn-and-skip semantics).
- `cmd/cornus/commands.go` — `cornus deploy --server` first-Ready `sync.Once` forward +
  `--no-forward-ports`.
- `cmd/cornus/internal/composecli/{commands,daemon,supervisor}.go` — foreground hold,
  `daemonService.ForwardPorts`/`ForwardOnly`, `daemonResponse.Forwards`.
- `pkg/dockerproxy/{attach,proxy,state,containers}.go` — `containerRecord.fwd`,
  `WithoutPortForwards()`.
- `pkg/api` — `PortForwardConfig{Port, Protocol}` preamble; `PortForwardAck`
  (udp-only newline-JSON ack).
- `pkg/client` — `client.PortForward`.
- `pkg/server` — `portforward` action in the deploy action switch;
  `handleDeployPortForward`.
- `pkg/deploy` — `Backend.ForwardPort` interface method.
- `pkg/deploy/dockerhost` — `engine.containerIP` + `ForwardPort` (bridge splice).
- `pkg/deploy/kubernetes` — `ForwardPort` over the `pods/portforward` SPDY subresource.
- `pkg/kubefwd` (+ `_test.go`) — client-side direct-to-pod dialer (`New`, `Dialer.PortForward`,
  `podConn`, `Fallback{Primary, Secondary}`).
- `pkg/kubeclient` — `FirstPod` (`cornus.app`-label pod resolver, wraps `deploy.ErrNotFound`),
  shared by `pkg/kubefwd` and `pkg/kubelogs`.
- `cmd/cornus/internal/clientconn` — `Conn.Dialer()` returns the `Fallback` for cluster
  profiles, the plain proxy client otherwise.
- `cmd/cornus/commands.go` (`runRemote`), `cmd/cornus/internal/composecli` (`rt.forwardDialer`,
  `spawnDaemon` passing `--kube-context`/`--kube-namespace`, `runDaemon` rebuilding the
  `Fallback`) — the wired forward sites.
- `pkg/e2e/harness.go` — `port_forward` and `free_port` builtins.
- `e2e/scenarios/deploy-portforward.star`, `e2e/scenarios/deploy-autoforward.star` — the
  E2E scenarios (both in the Makefile `SCENARIOS`).

## Test Coverage

- Unit: `TestPortForwardWS` (server preamble + bridge + port decode via an echoing
  `fakeBackend`); `TestForwardPortEchoes` / `TestForwardPortRejectsUDP` (dockerhost — a local
  echo listener stands in for the container, the fake engine inspect returns `127.0.0.1`);
  `TestParsePortSpec` (CLI parsing). The kubernetes `ForwardPort` needs a live API server and
  is skip-gated in units (like exec when `restConfig == nil`).
- E2E: harness builtin `port_forward(name, port, server?)` (`pkg/e2e/harness.go`) — modeled
  on `dockerd_up`, backgrounds `cornus port-forward`, returns the local `127.0.0.1:PORT`
  address, and is torn down at scenario end; registered in `predeclared`/`predeclaredNames`.
  Scenario `e2e/scenarios/deploy-portforward.star` (target-agnostic, skips `local`) deploys
  `nginx:alpine` with NO published ports, forwards a fresh local port to container `:80`, and
  `http_get`s it (200 + body contains `nginx`), plus a concurrent second GET.
- Auto-forward: `pkg/portfwd` unit tests; `-race` clean on portfwd/composecli/dockerproxy;
  composecli supervisor test drives a forward-only session over an injected echo dialer;
  dockerproxy `TestContainerPortForward` (`fakeAttacher.PortForward`: start->dial->echo,
  stop->refused, restart->works).
- E2E `deploy-autoforward.star`: session auto-forward of a published port (with 9P mounts
  on kube) + concurrent connections; `compose.star`'s `http_get` of the compose-published
  port runs on BOTH targets — on kube it rides the daemon-held auto-forward (the real
  proof), on docker it rides Docker's publish.
- Live validations: docker target — nginx with no published ports forwarded and curled
  (200 + nginx body) including a concurrent second GET, 20 concurrent GETs all 200, clean
  SIGINT teardown; `deploy-autoforward.star`, `deploy-portforward.star` (refactored CLI),
  `compose.star`, `dockerd.star` all PASSED. kube target (kind-in-dind containerized
  runner) — `deploy-portforward.star` AND `deploy-autoforward.star` PASSED (unpublished-
  port reach + concurrent conns; session auto-forward with 9P mounts + concurrent conns).
- Direct-to-pod (`pkg/kubefwd`): UDP rejected before the kubeconfig loads; pod resolved by
  label; no-pod yields `ErrNotFound`; kubeconfig load AND load-error caching; `Fallback`
  primary-success, secondary-on-primary-failure, and no-fallback-on-cancel — all with
  stubbed load and `dialPod` seams. `kubeclient.FirstPod` is covered transitively via the
  kubelogs and kubefwd tests.
- Live smoke of the deploy surface: `cornus deploy --server` with a published port
  started the forward on Ready (hitting the documented EADDRINUSE-skip against Docker's
  own publish, deploy unharmed); Ctrl-C printed "torn down" and removed the container.

## Pitfalls

- **`command` maps differently per backend.** dockerhost maps `command` to Docker's `Cmd`
  (appended AFTER the image ENTRYPOINT); kubernetes maps it to the container `command`
  (REPLACES the entrypoint). An image with an ENTRYPOINT (e.g. `hashicorp/http-echo`,
  entrypoint `/http-echo`) run with `command=["/http-echo", ...]` crash-loops with "Too many
  arguments!" on dockerhost. For a target-agnostic scenario, prefer an image whose default
  CMD already serves (the scenario uses `nginx:alpine`, port 80, no `command`). This surfaced
  only under a live E2E run — not by unit fakes or a single manual run.
- **A TCP accept succeeds before the tunnel is up.** The local listener accepts a connection
  before the per-connection tunnel to the container is established, so an E2E readiness check
  must be an HTTP-level `http_get(retry=...)`, not just a successful dial.
- **`PortMapping.Host` is still ignored for direct exposure on kubernetes** (ClusterIP only)
  — port-forward (now automatic on client sessions) is the deliberate answer rather than
  publishing host ports there.
- **Client and server on one host collide with dockerhost's own publish**: docker-proxy
  binds `0.0.0.0:<host>`, so the auto-forward's `127.0.0.1` bind gets EADDRINUSE. This is
  the canonical reason bind failures are warn-and-skip, never fatal, on the auto-forward
  surfaces.
- **An all-skipped forward group must close immediately** or foreground `compose up`
  hangs forever holding nothing.
- **`Group.Close` must shut down directly and sever accepted conns** — relying on the ctx
  watcher alone deadlocks the strict-bind cleanup path, and draining instead of closing
  lets one long-lived connection hang session exit.
- **Start deploy-session forwards on the first `Ready` event via `sync.Once`**, not on
  `e.Status != nil` — status events repeat.
- **The server proxy is a fallback, not the primary path, on cluster profiles**: the
  server's ServiceAccount usually lacks `pods/portforward` RBAC, so a server-tunneled kube
  forward silently fails. The client dials the pod directly with the developer's kubeconfig
  first (`pkg/kubefwd`) and only proxies through the server when the direct dial fails.
- **`Fallback` must not fall back on `ctx.Err()` and only fires pre-traffic** — otherwise
  a cancelled forward would spuriously retry, or bytes already written to the direct conn
  would be duplicated onto the proxy conn.
- **Cache the kubeconfig LOAD ERROR, not just the success** — a broken kubeconfig would
  otherwise be re-read on every dial.
- **The background `up -d` helper needs `--kube-context`/`--kube-namespace`** to rebuild
  the same `Fallback` dialer; without them the detached supervisor falls back to the
  server port-forward for TCP, reintroducing the RBAC failure.
