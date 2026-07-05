# Client Connectivity and Service Exposure

## Summary

Everything the cornus CLI does to reach a remote server, reach a workload's ports, and expose
those ports — locally, publicly, or through a background agent. Four subsystems share one spine:
the `deploy.Backend.ForwardPort(ctx, name, port, proto, conn)` byte-bridge (the same method
serves local port-forwards and public tunnels), the client-go SPDY `pods/portforward`
subresource (reused by workload forwards, the direct-to-pod dialer, and the in-cluster Service
forward), and a single `clientconn.Resolver` that turns a kubeconfig-style connection profile into
`{Endpoint, Token, TLS, Dialer, Conduit, RegistryHost}` for every command. The default posture on
cluster profiles is **direct-to-pod with the developer's own kube credentials; the server proxy is
the last resort**, because the server's ServiceAccount usually lacks the pod subresource RBAC.

## Included Documents

| Document | Focus |
|----------|-------|
| [remote-cluster-connection-ergonomics.md](./remote-cluster-connection-ergonomics.md) | Connection profiles (`pkg/clientconfig`), the one `clientconn.Resolver`, `svcforward` auto port-forward to the in-cluster Service, TokenRequest-minted SA creds (`pkg/kubeauth`), client TLS through REST + every WS dial, server-advertised registry host, `via-server` toggle. |
| [port-forwarding.md](./port-forwarding.md) | `cornus port-forward` + the shared `pkg/portfwd` auto-forward engine on all four client surfaces; `Backend.ForwardPort` per backend; direct-to-pod `pkg/kubefwd` with server-proxy `Fallback`; UDP framing. |
| [public-tunnels.md](./public-tunnels.md) | `cornus tunnel` public exposure through `pkg/tunnel` (`Provider` vs `UpstreamProvider` seam, shim listener); ngrok / ssh / cloudflare backends behind `CORNUS_TUNNEL_BACKEND`; fail-closed SSH host keys; Tailscale-deferred rationale. |
| [client-daemon-and-conduit.md](./client-daemon-and-conduit.md) | The unified `cornus daemon agent` (supervisor tree, `agentproc` flock lifecycle, control socket, refcounted per-server conns, idle-exit) and the selectable session conduit mode (per-port forward / SOCKS5 split-tunnel / none) behind `clientconduit.Conduit`. |
| [client-side-egress.md](./client-side-egress.md) | Client-routed workload egress, proxy-aware caller dialing, session/gateway lifecycle, host companions, and policy validation. |

## Stable Knowledge

### The shared transport spine

- **`deploy.Backend.ForwardPort(ctx, name, port, proto, conn)`** is the single byte-bridge to a
  named deployment's container port. `cornus port-forward` and `cornus tunnel` both terminate on
  it (tunnel just puts a hosted relay in front); it reaches ports the workload never published, on
  every backend. Forced implementations exist in dockerhost, kubernetes, containerd, and the
  server's `fakeBackend`. It rides the exec/attach raw-tunnel wire shape — no new transport was
  invented for forwarding or tunneling.
- **SPDY `pods/portforward`** is reused three times with the identical `spdy.RoundTripperFor` ->
  `spdy.NewDialer` handshake (error stream + data stream, shared request id + `PortHeader`): the
  kubernetes backend's server-side `ForwardPort`, the client-side direct-to-pod dialer
  (`pkg/kubefwd`), and the in-cluster Service forward (`pkg/svcforward`, via the higher-level
  `portforward.NewOnAddresses`). All work from an out-of-cluster kubeconfig with NO sidecar. TCP
  only — pods/portforward has no UDP.
- **One resolver, all commands.** `cmd/cornus/internal/clientconn.Resolver{ConfigFile,Context}` ->
  `Conn`. It lives in an INTERNAL package (not `main`) because `cornus compose` is a separate
  package that cannot import `main`. Built once after `kong.Parse` and passed to
  `ctx.Run(&cli, resolver)`; kong injects it by type into any command's (or nested subcommand's)
  `Run`. Precedence chains it centralizes: Endpoint = flag > context server > auto port-forward;
  Token = `CORNUS_TOKEN` env > kube-auth mint > static profile token; Conduit mode = flag >
  `CORNUS_CONDUIT` > profile > `port-forward`; ViaServer = flag > `CORNUS_VIA_SERVER` > profile.

### Direct-to-pod default, server proxy fallback (the load-bearing 2026-07-08 decision)

On a cluster profile the client does NOT tunnel workload forwards / logs through the server by
default. The server runs under its ServiceAccount, which usually lacks `pods/portforward` (and
`pods/log`) RBAC, so a server-proxied forward silently fails. Instead:

- `clientconn.Conn.Dialer()` returns `kubefwd.Fallback{Primary: direct, Secondary: proxy}` for a
  cluster profile (`KubeCluster` set), the plain proxy client otherwise.
- `pkg/kubefwd` dials the workload pod directly with the developer's kubeconfig (resolved via the
  shared `kubeclient.FirstPod` `cornus.app`-label lookup, fresh SPDY per call). `Fallback` tries
  direct then proxy, only on a PRE-TRAFFIC error and NEVER on `ctx.Err()`, so no bytes are
  duplicated across the two dials.
- The `via-server` tri-state toggle (flag > `CORNUS_VIA_SERVER` > profile `*bool`) forces the
  server-routed path when you WANT it. It is applied at the use sites, not by nulling
  `KubeCluster` in `Resolve`, so `mintKubeToken` still mints the cluster token even when transport
  routes through the server.

### Connection profiles and credential bridging

- Profiles (`pkg/clientconfig`) are a kubeconfig-style file (`File`/`Context`/`TLS`/`PortForward`/
  `KubeAuth`), 0600 under a 0700 dir, cross-platform `DefaultPath()` (honors `$XDG_CONFIG_HOME`,
  else `os.UserConfigDir()`). Managed by `cornus config` (get-contexts / current-context /
  use-context / set-context / delete-context / view). `set-context` has merge-on-edit semantics
  (only provided flags overwrite). First-context creation offers (TTY-gated, injectable
  `confirmSetDefaultContext`) to set it current.
- **Two distinct credentials, bridged by TokenRequest.** The kube API credential authenticates the
  port-forward SETUP to the API server; the cornus credential authenticates THROUGH the tunnel.
  They are not interchangeable (different verifiers/audiences). `pkg/kubeauth` mints a short-lived
  audience-scoped SA token via the TokenRequest API from the developer's kube access; in-cluster
  cornus validates it through its EXISTING JWKS/audience path with zero server code change.
- **Auto port-forward to the Service** (`pkg/svcforward`): a port-forward-only profile opens the
  `kubectl port-forward` equivalent to the in-cluster Service for the command's lifetime and points
  the client at `http://127.0.0.1:<local>`. Resolves via the Service's ENDPOINTS (already encode
  pod readiness + named-port numbers), not a raw pod list. `set-context -n <ns>` can `Discover` the
  client-facing cornus Service (label `app.kubernetes.io/name=cornus` then `app=cornus`, excluding
  the headless hub) and store it — eager, fail-loud.
- **Client TLS everywhere.** `client.WithTLSConfig(*tls.Config)` applies to BOTH the REST transport
  AND every WebSocket dial (exec/attach/portforward/build/deploy-attach), via TLS-aware dialers
  `wire.DialConnControlHeaderTLS` / `DialControlHeaderTLS` and a trailing `*tls.Config` threaded
  through `buildwire.Serve`/`deploywire.Serve`. A port-forwarded Service is plain HTTP, though —
  the forward bypasses the cluster-edge TLS and a server cert never matches `127.0.0.1`, so
  `resolveConn` sets `http://<local>` for the pf path (the profile token still rides over it).
- **Pull-ref host is decoupled from the client endpoint.** An image's identity is its REPOSITORY
  PATH; the HOST is a per-vantage rendezvous detail. `registryHostFor` resolves override
  (`--registry` / `CORNUS_REGISTRY` / profile) > server `/.cornus/v1/info` advertise > `client.Host()`;
  `Conn.RegistryHost` is NEVER rewritten by the port-forward. The server push-redirects a build
  push whose target equals the advertised host to the co-located registry over loopback. Only
  NodePort / LoadBalancer auto-advertise; ClusterIP is opt-in (`CORNUS_ADVERTISE_REGISTRY`) because
  node containerd uses host DNS, not CoreDNS. See [[registry-and-storage]] and
  [[kubernetes-deploy-synthesis]].

### Auto-forward on every client surface (`pkg/portfwd`)

`DeploySpec.Ports[].Host` means "reachable on the client at `127.0.0.1:<host>`" on ALL backends
(dockerhost additionally publishes on the deploy host). `portfwd.Start(ctx, dialer, name, ports,
opts...)` returns a `Group`: one local listener per TCP mapping, one tunnel per accepted connection.
Four surfaces publish through it automatically, each with `--no-forward-ports`:

- `cornus deploy --server` — starts on the first `Ready` event, guarded by `sync.Once` (NOT
  `e.Status != nil`, which repeats).
- `cornus compose up` foreground — port services hold the process alive like mount services
  (behavioral change: a mount-free `up` used to always return; scripts should use `-d`). A
  genuine foreground exit (Ctrl-C) now also deletes the mount-free deployments it created,
  so terminating `up` stops everything it brought up (`removeDeployments`); the `-d` /
  `--no-forward-ports` non-blocking returns still leave them running for `down`.
- `cornus compose up -d` — port services handed to the per-project background helper
  (`daemonService.ForwardPorts`/`ForwardOnly`, `daemonResponse.Forwards`).
- `cornus daemon docker` — each container's PortBindings after `waitReady`; `docker run -p 8080:80`
  behaves like local Docker.

### Public tunnels (`pkg/tunnel`)

- `cornus tunnel <name> <port>` returns a public URL. `pkg/server` depends ONLY on the `pkg/tunnel`
  interface; concrete backends are blank-imported in `main.go`. `CORNUS_TUNNEL_BACKEND` selects
  (default `ngrok`).
- Two provider shapes: `Provider`/`Session` (listener model — backend yields a `net.Listener`, the
  manager `Accept`s) and `UpstreamProvider`/`UpstreamSession` (`StartUpstream(...upstreamURL)` for
  backends that only proxy a local upstream — the server stands up a **shim listener** on
  `127.0.0.1:0` bridging to `ForwardPort` and hands its address as the upstream). The credential is
  client-injected on the authenticated `POST /.cornus/v1/deploy/{name}/tunnel`, never server-known; a
  `CredentialOptional` backend is the only way the endpoint tolerates a missing token.
- Backends: **ngrok** (in-process, no subprocess, default), **ssh** (remote-forward via
  `x/crypto/ssh`, fail-closed host-key verification, works with sish/serveo/plain sshd),
  **cloudflare** (shells out to `cloudflared tunnel --url <shim>`, parses the trycloudflare URL).

### The unified daemon agent (`cornus daemon agent`)

- One background process per user (control socket `$XDG_RUNTIME_DIR/cornus/agent.sock`, override
  `CORNUS_AGENT_DIR`), merging the former per-project `cornus daemon mounts` and standalone
  `cornus daemon docker` daemons. Built on an in-house supervisor tree
  (`cmd/cornus/internal/supervisor`: `Service`/`Policy`(RemoveOnExit|Restart)/idle hook, each child
  `recover()`-isolated — errgroup would cancel siblings). Process lifecycle in `agentproc`
  (flock-serialized spawn with a double-check under lock, `Discover`/`EnsureRunning`/`Stop`, state
  file). Session core in `clientagent` (`Project`/`svcSession`/`specFingerprint` + `Agent` owning
  the socket, a supervisor root, and refcounted per-server `connState` = shared `Conn` + `Conduit`).
- **Env-frozen at spawn**: the agent reads NO `CORNUS_*` tri-state vars itself. Clients pre-resolve
  token / via-server / conduit into a `connSpec` (incl. an ABSOLUTE config path via
  `Resolver.AbsConfigPath`) and send it; `Resolver.ResolveWith(server, token)` resolves each
  client's token explicitly.

### Session conduit modes (`pkg/clientconduit`, `pkg/socks5`)

- Three modes behind one `clientconduit.Conduit` (`Banner`/`Add`/`Close`): `port-forward` (default,
  per-service binds via `portfwd`), `socks5` (one split-tunnel proxy), `ModeNone`
  (`--no-forward-ports`). Client-side only — reuses `portfwd.Dialer.PortForward`; SOCKS5 CONNECT is
  TCP, aligned with all backends.
- `pkg/socks5` is hand-rolled no-auth + CONNECT (deliberately NOT `armon/go-socks5`). `Router`
  resolves the CONNECT subject against ordered `{pattern -> replacement}` rules (first match ->
  `service:port` tunneled; no match -> direct `net.Dial` = the "split"). Default rule strips a
  `.cornus.internal` suffix; sed-style `\1` backrefs.
- `dockerproxy.New(WithConduit(...))` lets ONE SOCKS5 proxy span docker containers AND compose
  services by name. In socks5 mode `docker run -p` has no local listener — reach the container
  through the proxy (this is why `cornus daemon docker` was previously port-forward-only; the
  unified agent is where it resolved).

### SOCKS5 local-resolution address-family fallback

`socks5://` resolves the destination on the Cornus client before asking the proxy to connect;
`socks5h://` sends the hostname to the proxy for remote resolution. The local-resolution path must
try every address returned by `net.DefaultResolver.LookupIPAddr`, not select `ip[0]`. Resolver order
varies across hosts: dual-stack CI can return `::1` before `127.0.0.1`, while a target or proxy may
only support IPv4. `socksDial` therefore calls the context-aware `proxyDial` once per resolved
address until one succeeds. Remote-resolution behavior is unchanged.

Tests inject the resolver through the package-level `lookupIP` seam. The deterministic dual-stack
case forces `[::1, 127.0.0.1]`, verifies SOCKS ATYP attempts `[4, 1]`, and proves fallback succeeds.
Tests that use `localhost` directly must not assume one address family or a stable resolver order.

## Operational Guidance

- To reach an unpublished workload port from a laptop: `cornus port-forward` for a one-off, or rely
  on the automatic `Ports[].Host` forward on a deploy/compose/docker session. To reach it from the
  public internet: `cornus tunnel`.
- Adding a client command that talks to the server: take a `*clientconn.Resolver` in `Run`, call
  `Resolve`/`Require`, `defer cn.Cleanup()`, and dial via `cn.Dialer(...)` for forwards. Do not
  hardcode `http://localhost:5000` (compose/daemon lost that as a default; it is now only a
  fallback so a profile can win). kong resolves bindings at RUN time — a clean `go build` does NOT
  prove the resolver is injectable; smoke-run every resolver-wired command.
- Adding a tunnel backend: implement `Provider` (listener model) or `UpstreamProvider` (local-only
  proxy), `Register` it, blank-import it in `main.go`. Do NOT add a heavy dependency to go.mod for
  an optional backend — Go's MVS is build-tag-agnostic, so a build tag gates compilation but not
  the module version graph (the Tailscale lesson).
- Cluster forwards need the developer's kubeconfig for the direct path; a detached `up -d` helper
  must receive `--kube-context`/`--kube-namespace` to rebuild the same `Fallback` dialer, or it
  silently reverts to the RBAC-failing server proxy.

## Files

- `cmd/cornus/internal/clientconn/` — `Resolver`, `Conn` (`Endpoint`/`Token`/`TLS`/`Cleanup`/
  `Dialer`/`ConduitMode`/`RegistryHost`/`ViaServer`), the precedence chains, `ResolveWith`,
  `AbsConfigPath`.
- `pkg/clientconfig/` — kubeconfig-style profile, `Load`/`Save`/`Resolve`, `(*TLS).Config`,
  `Context.Conduit`/`RegistryHost`/`ViaServer`.
- `pkg/svcforward/` — Service -> ready pod/targetPort resolution, SPDY `NewOnAddresses`; `Discover`.
- `pkg/kubeauth/` — TokenRequest `Token`/`mint`. `pkg/kubeclient/` — `Load`, `FirstPod`.
- `pkg/portfwd/` — shared auto-forward engine (`Start`, `Group`, `WithStrictBind`, warn-and-skip).
- `pkg/kubefwd/` — client-side direct-to-pod dialer (`podConn`, `Fallback`).
- `cmd/cornus/portforward.go`, `pkg/api.PortForwardConfig`/`PortForwardAck`, `pkg/client.PortForward`,
  `pkg/server` `portforward` action + `handleDeployPortForward`, `pkg/deploy.Backend.ForwardPort`
  (+ dockerhost/kubernetes/containerd impls).
- `pkg/tunnel/` (`tunnel.go` seam + `ngrok`/`ssh`/`cloudflare`), `pkg/server/deploy_tunnel.go`
  (`tunnelManager`, shim, `handleDeployTunnel`), `cmd/cornus/tunnel.go`.
- `cmd/cornus/internal/supervisor/`, `cmd/cornus/internal/agentproc/`,
  `cmd/cornus/internal/clientagent/`; `pkg/clientconduit/`, `pkg/socks5/`; `cmd/cornus/socks5.go`,
  `daemon.go`, `commands.go`; `pkg/dockerproxy/` (`New(WithConduit)`, `containerRecord`, `Proxy.Close`).
- `pkg/wire/` TLS-aware dialers; `pkg/client.WithTLSConfig`; `pkg/server` `GET /.cornus/v1/info`
  (`ServerInfo{RegistryHost,RegistryScheme}`), `localPushTarget`; `pkg/deploy/kubernetes`
  `RegistryAdvertiser`.

## Tests

- Units: `TestPortForwardWS`, dockerhost `TestForwardPortEchoes`/`RejectsUDP`, `TestParsePortSpec`;
  `pkg/portfwd`, `pkg/kubefwd` (`Fallback` primary/secondary/no-fallback-on-cancel, kubeconfig
  load+error caching), `pkg/tunnel` registry + ssh fail-closed host-key + cloudflare URL parse,
  `pkg/server` tunnel bridge + `TestTunnelManagerUpstreamShim`, `pkg/clientconfig`, `pkg/client`
  `TestClientTLSConfig`, `pkg/svcforward` Endpoints resolution, `pkg/kubeauth` TokenRequest reactor,
  `pkg/kubeclient`, `clientconn` `TestViaServerEnabled`/`TestResolveConn*`, `supervisor`/`agentproc`
  fully unit-tested (`-race`), agent `TestIdleCheckHonorsInflightAndWork`/`TestReapDockerReleasesRefs`,
  `pkg/socks5` against `x/net/proxy` + `TestProxyRejectsEmptyDomain`.
- E2E: `deploy-portforward.star`, `deploy-autoforward.star`, `deploy-tunnel.star` (opt-in,
  `NGROK_AUTHTOKEN`), `connection-profile.star`, `incluster-portforward.star` (svcforward + A/B
  via-server), `incluster-kubeauth.star` (TokenRequest auth + no-token negative control),
  `registry-advertise.star`, `agent.star` (compose `up -d` + `dockerd_up` on one agent). See
  [[testing-ci-and-quality-synthesis]].

## Pitfalls

- **The server proxy is a FALLBACK on cluster profiles, not the primary path** — the server SA
  usually lacks `pods/portforward` / `pods/log` RBAC, so a server-tunneled kube forward silently
  fails. Dial the pod directly with the developer's kubeconfig first.
- **`Fallback` must not fall back on `ctx.Err()` and only fires pre-traffic** — else a cancelled
  forward retries, or direct-conn bytes get duplicated onto the proxy conn. Cache the kubeconfig
  LOAD ERROR too, or a broken kubeconfig is re-read every dial.
- **Bind failures and incapable-path UDP are warn-and-skip, never fatal** on the auto-forward
  surfaces — a client+server on one host collides with dockerhost's own `0.0.0.0:<host>` publish
  (EADDRINUSE on the `127.0.0.1` bind). An all-skipped group must close IMMEDIATELY or foreground
  `compose up` hangs holding nothing. `Group.Close` must shut down directly and sever accepted
  conns (ctx-watcher alone deadlocks strict-bind cleanup; draining lets one long conn hang exit).
- **`command` vs `entrypoint` differs per backend** — dockerhost appends `command` after ENTRYPOINT
  (Docker `Cmd`); kubernetes maps it to `container.command` (REPLACES entrypoint). Prefer an image
  whose default CMD serves for target-agnostic scenarios. (Full contract in
  [[testing-ci-and-quality-synthesis]] and [[deploy-backends-synthesis]].)
- **A TCP accept succeeds before the per-connection tunnel is up** — readiness must be an
  HTTP-level `http_get(retry=...)`, not a bare dial.
- **`PortMapping.Host` is still ignored for direct exposure on kubernetes** (ClusterIP only) — the
  automatic port-forward is the deliberate answer, not publishing host ports there.
- **kong forbids a global flag duplicating a subcommand flag** — a global `--config` panicked
  (CaretakerCmd already declares `--config`); it is `--config-file`. Flags and command names are
  separate namespaces (`config` the command did not collide).
- **SSH host-key verification is fail-closed by design** — omitting all of
  `CORNUS_TUNNEL_SSH_KNOWN_HOSTS`/`_HOSTKEY`/`_INSECURE` is an error, not a silent MITM.
  `UpstreamProvider` backends leak the shim listener without explicit teardown.
- **Tailscale Funnel cannot be an optional backend as-is** — adding `tailscale.com` forces
  `k8s.io/*` v0.32.1 -> v0.34.0 across the whole module; a build tag does not gate the version
  graph. Needs a separate module/plugin or a CLI subprocess.
- **The agent is env-frozen at spawn.** Two clients with different `KUBECONFIG`s but the same cornus
  context share the first client's kube resolution — keep `KUBECONFIG` stable, or use a static
  `CORNUS_TOKEN`. Serialize `compose up -d` / `down` of the SAME project (they race the conn/conduit
  refs). `down` no longer force-kills a wedged agent — use `cornus daemon stop`.
- **The pull-ref host must not be the client endpoint** — a port-forward endpoint is
  `127.0.0.1:<ephemeral>`, unpullable by the node; `registryHostFor` prefers the server `/.cornus/v1/info`
  advertise and `Conn.RegistryHost` is never rewritten by the port-forward.
- **Never hand only the first locally resolved address to a downstream connector.** SOCKS proxies,
  tunnels, and similar connectors inherit a hidden address-family dependency if they receive
  `ip[0]`. Try the full resolver result or delegate name resolution downstream.

## Session Conduits And Reconciliation

`--conduit`, `--conduit-mode`, and `CORNUS_CONDUIT` accept a bare mode or a `socks5://host:port[?suffix=SUFFIX]` URL. A bare value changes only mode and retains profile listen/suffix; URL fields override only the fields they name. `socks5h` is accepted as a synonym because Cornus resolves SOCKS5 service names remotely.

`.shared[:port]` identifies an agent-shared SOCKS5 facility. An ordinary URL is session-local and defaults to an ephemeral loopback bind when address is absent. Profiles must not persist session-local URLs. The agent keys session-local conduits by session identity, returns their bound-address banner through the control response, and keeps shared and session-local proxies isolated while allowing fully-qualified deployment names through either proxy.

The client agent is a level-triggered `Project.Apply`/`Remove` reconciler. `mountController` owns deploy-attach sessions; `exposureController` owns port-forward listeners or SOCKS5 aliases. Separate fingerprints preserve a healthy mount for port-forward-only changes and recreate/re-expose it for workload changes. Every desired service is exposed through the appropriate conduit, including mounted port-less aliases.

`cornus config set-context` loads bare context documents from repeatable `--from-file` and `--from-file-override`; precedence is stored context (only with `--merge`), base files, flags, then override files. The default is replace. `config view --export` emits the matching bare document, uses `0600` for `--output-file`, and supports `--redact`. Strict decoding intentionally rejects a whole config document.

Open follow-ups: audit `daemon docker` banner display for session-local conduits and add a detached `up -d` shared/session-local coexistence E2E.

## Client-Side Egress

`client-side-egress.md` extends this synthesis on the outbound side: a held deploy-attach session can carry workload egress back to the caller, and the caller must dial through its own `NO_PROXY`, `ALL_PROXY`, or `HTTP(S)_PROXY` policy. Relay modes are session-bound; only gateway, cluster, and deny policies can detach safely.

Session conduits expose inbound service access, whereas client egress carries outbound workload traffic. Keep them separate at the policy layer even though both share client-agent lifetime and proxy-aware dialing. A distinct gateway URL is intentionally rejected; `gateway` means the Cornus server.
