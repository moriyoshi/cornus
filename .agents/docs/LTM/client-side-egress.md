# Client-Side Egress

## Summary

Client-side egress routes a deployed workload's outbound traffic through the developer network for VPN, corporate-proxy, SASE, or air-gapped destinations. It keeps route policy and authorization explicit across Kubernetes, dockerhost, and containerd.

## Key Facts

- `api.EgressSpec` supports environment-only proxy configuration and relay-backed `proxy` or `transparent` modes. Relay modes need a deploy-attach session unless the server is an enabled gateway terminus.
- `client` routes use `pkg/clientproxy.Dialer`, honoring `NO_PROXY`, `ALL_PROXY` (SOCKS5), and `HTTP(S)_PROXY` (CONNECT). `socks5` resolves locally; `socks5h` forwards names to the proxy.
- The `gateway` route is the Cornus server. A separate `EgressSpec.Gateway` URL is rejected until operator gateway-node forwarding has a defined contract.
- Kubernetes folds egress, DNS, hub, mounts, and credentials into one caretaker. Relay modes cannot use a detached-primary network because the caretaker needs `/.cornus/v1/caretaker/attach`.

## Details

### Routing and lifecycle

Policy routes destinations to `client`, `gateway`, `cluster`, or `deny`; scripts still require a live session. `api.EgressSpec.NeedsRelay` is the lifecycle predicate: compose foreground and agent paths must hold deploy-attach sessions for relay egress even with no mounts or published ports. Stateless deploy is valid only for policies using `gateway`, `cluster`, or `deny`.

Detached Compose routing must make the same decision before deployment. `needsBackgroundAgent`
returns true when any service has a local mount or `Egress.NeedsRelay()`, and also accounts for
SOCKS5 and published-port lifetimes. Omitting relay egress causes `runForeground(detached=true)` to
deploy on a held session and immediately tear that session down, removing the workload before a
waiter can observe it. The background agent is therefore part of the correctness contract, not
merely a convenience for detached output.

`CORNUS_EGRESS_GATEWAY` enables detached server-terminus egress. `CORNUS_EGRESS_POLICY`, when set, is a fail-closed JSON ceiling: workload policy cannot exceed it. The server carries the route in `wire.OpenEgress`, keeps session-held relay handling separate from the sessionless gateway path, and drops client-routed traffic without a session.

### Backend realization

Kubernetes injects the role through `deploymentWithAttachments` and rejects relay modes on a `Default` detached network. Dockerhost and containerd implement `deploy.EgressBackend` and run a per-replica caretaker companion in the workload network namespace. Dockerhost uses `NetworkMode: "container:<app-id>"`; containerd joins the pinned netns under `/run/cornus/netns/...`. Companions are hidden from status/list output and reaped before app instances.

Transparent mode grants `NET_ADMIN`; `caretaker.EgressRole.SetupRedirect` programs shared `pkg/netredirect` rules and marks its own upstream sockets. Loopback and UDP remain exempt. Enforcing proxy and egress are mutually exclusive because both intercept traffic.

Compose uses `x-cornus-egress` at service or project scope. A service block completely overrides the project default, project defaults are copied per inheriting service, and the `x-` prefix preserves standard Compose-tool compatibility.

### Observability and validation

`caretaker.egress.connections` records route and protocol. `caretaker.egress.bytes` covers CONNECT, SOCKS, transparent, and absolute-form HTTP forwarding; the HTTP path requires explicit write counting. The harness supplies `egress_proxy()` and `egress_proxy_hits()` plus `compose_up_bg(env=...)` for caller-proxy testing.

The caretaker startup probe calls `egressReady`, which dials `127.0.0.1:<ListenPort>` after relay
setup. Proxy mode binds loopback and transparent mode binds all interfaces, so both are reachable at
loopback. A zero listen port means no listener is expected and is immediately ready. This prevents
the app from starting before outbound interception is usable.

## Files

- `pkg/api/deploy.go` - egress spec, validation, and relay predicate.
- `pkg/deploywire/serve.go`, `pkg/server/egress_relay.go` - client and gateway relay paths.
- `pkg/clientproxy/dialer.go` - caller proxy-aware dialer.
- `pkg/caretaker/`, `pkg/netredirect/` - role runtime, redirect setup, and metrics.
- `pkg/deploy/{kubernetes,dockerhost,containerdhost}/` - backend injection and companions.
- `pkg/compose/` - `x-cornus-egress` translation and defaults.

## Test Coverage

- `go test ./...` covers policy validation, gateway routing, detached-network rejection, proxy-aware dialing, and the relay-to-client-proxy path.
- `make e2e-check` resolves egress scenarios; run relay behavior in the dind/kind runner with `make e2e-container E2E_TARGETS="docker kube"`.
- `deploy-egress-proxy.star` and `deploy-egress-transparent.star` are live-verified on kube through
  the privileged containerized kind runner. They prove the caretaker listener, relay session, and
  route differential end to end.

## Pitfalls

- A sessionless policy containing `client` or a script is invalid.
- Do not silently accept an unused distinct gateway URL.
- Do not bypass caller proxy configuration in the client relay backing.
- Meter partial writes on the plain HTTP forwarding path.
