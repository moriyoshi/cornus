# Egress

A remote workload's outbound traffic normally leaves from wherever the runtime
sits. Client-side egress instead routes it through the machine that deployed the
workload — for a VPN, a corporate proxy, a SASE gateway, or an air-gapped cluster
where only the caller's side has a sanctioned egress path. It rides the live
`cornus deploy --server` session and its per-pod caretaker sidecar. Its companion
feature, handing a workload a caller-minted secret, is
[credential brokering](/guides/credentials); for the *inbound* direction — giving
a workload a public hostname — see [Ingress](/guides/ingress).

## How it works

### Modes

Client-side egress routes a remote container's egress through a client-side
vantage point. It has three modes of increasing transparency, opt-in via the
deploy spec's `egress:` block, Compose's portable `x-cornus-egress:` extension,
or `cornus deploy --egress`.

| Mode | Backends | Mechanism |
| --- | --- | --- |
| `env` | all | Propagate the caller's own proxy env vars (`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` / `ALL_PROXY`, resolved from the OS) into the container. Needs the proxy reachable from the container; no relay. |
| `proxy` | kubernetes, dockerhost, containerd | The caretaker runs an HTTP CONNECT proxy and SOCKS5 on loopback and the app's proxy env vars point at it; each connection is tunneled back through the server to the terminus. |
| `transparent` | kubernetes, dockerhost, containerd | All app TCP is captured by an nftables redirect and recovered via `SO_ORIGINAL_DST`, so apps that ignore proxy vars are covered too. |

In Compose, use the `x-cornus-egress` extension at the project level or per
service (a service block completely overrides the project default). The `x-`
prefix keeps the file valid for standard Compose tooling, which ignores `x-*`
keys.

```yaml
x-cornus-egress:
  mode: proxy
  default: cluster
  rules:
    - pattern: "*.corp.example"
      route: client

services:
  worker:
    image: registry.example/worker:v1
    # This service inherits the project-level policy.
```

### Routing: four routes

Each destination resolves to exactly one route:

| Route | Meaning |
| --- | --- |
| `client` | Relay to the client-side network. Needs a live deploy-attach session. |
| `gateway` | Relay to a durable egress node (the Cornus server itself). Works with `--detach`. |
| `cluster` | Egress directly from the pod's own network — dialed locally, never relayed. |
| `deny` | Drop the connection. |

`default` applies to unmatched destinations and defaults to `cluster`, so
enabling egress never silently diverts in-cluster traffic — the caller opts
destinations *out* to the client or gateway. The relay modes (`proxy`,
`transparent`) need a live session, so `cornus deploy --detach` rejects a
`client` route. For durable detached egress, route only to
`gateway` / `cluster` / `deny`; the server is the gateway and requires operator
opt-in with `CORNUS_EGRESS_GATEWAY=1`. A distinct `gateway:` URL is intentionally
unsupported and rejected.

The policy is re-evaluated at every hop — the caretaker classifies, the server
re-checks (a compromised pod cannot upgrade its own routing), and the client
re-checks as a final guard — so all three agree.

**See also:** [deploy spec](/reference/deploy-spec), [cornus deploy](/cli/deploy)

## Route a remote workload's outbound traffic through the caller network

Send a workload's egress through your machine's network for a VPN, corporate proxy, or air-gapped cluster.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --egress proxy
```

- `--egress` modes are `env` (propagate the caller's proxy env vars, every backend, no relay), `proxy` (caretaker forward proxy relayed back through the server), or `transparent` (nftables redirect, covers apps that ignore proxy vars).
- A `client` route needs a live deploy-attach session. Direct `cornus deploy --detach` therefore rejects it; `cornus compose up -d` keeps the session in the background agent. In Compose use the `x-cornus-egress:` extension.

**See also:** [cornus deploy](/cli/deploy)

## Route only specific destinations through the caller

Add ordered, first-match routing rules so only chosen destinations leave via the client.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route '*.corp.example=client' \
  --egress-route '10.0.0.0/8=cluster'
```

- Each rule is `PATTERN=ROUTE` where route is `client`, `gateway`, `cluster`, or `deny`; `--egress-route` is repeatable and the first match wins.
- Patterns match the destination host (glob), a CIDR, and/or an explicit port (e.g. `api.example.com:443`).

**See also:** [cornus deploy](/cli/deploy)

## Set the default egress route

Choose where destinations no rule matches go; the default is `cluster`, so enabling egress never silently diverts in-cluster traffic.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-route 'api.internal=client' --egress-default deny
```

- `--egress-default` is one of `cluster` (default), `client`, `gateway`, or `deny`.
- The `client` route needs a live session; for durable detached egress route only to `gateway` / `cluster` / `deny`.

**See also:** [cornus deploy](/cli/deploy)

## Use a PAC-style policy script for egress

Instead of an ordered rule list, the routing decision can be a **PAC-compatible
JavaScript program** — a `FindProxyForURL(url, host)` function, so an existing
corporate PAC file drops in. It is set as `script:` on the egress spec (or
`--egress-pac` on the CLI) and, when present, supersedes `rules`. It is evaluated
by a sandboxed pure-Go JS engine with the standard pure PAC builtins
(`shExpMatch`, `dnsDomainIs`, `isInNet`, ...), a bounded per-call deadline, and
**fail-closed-to-`deny`** on error or timeout. The runtime has no ambient
authority: no `require`, no live network or DNS I/O (a name resolves only to the
destination IP the caller already knows), and deterministic `Date` / random, so
evaluation is reproducible across the caretaker, server, and client evaluation
points.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-pac ./egress.pac
```

```js
function FindProxyForURL(url, host) {
  if (dnsDomainIs(host, ".corp.example")) return "PROXY client";
  if (shExpMatch(host, "*.blocked.example")) return "DENY";
  return "DIRECT";
}
```

The `FindProxyForURL` return string maps to a route as follows (the first
`;`-separated directive is used):

| PAC return | Route |
| --- | --- |
| `DIRECT` | `cluster` (connect directly from the pod's network) |
| `DENY` / `BLOCK` | `deny` |
| `CLIENT` / `CLUSTER` / `GATEWAY` | that route (Cornus extension) |
| `PROXY client` / `PROXY gateway` / `PROXY cluster` | that route |
| `PROXY host:port` (a concrete proxy) | `client` — the client holds the real proxy and applies it |
| empty, null, or unrecognized | `default` |

The same program can be set inline in the spec:

```yaml
egress:
  mode: proxy
  default: cluster
  script: |
    function FindProxyForURL(url, host) {
      if (dnsDomainIs(host, ".corp.example")) return "PROXY client";
      if (shExpMatch(host, "*.blocked.example")) return "DENY";
      return "DIRECT";
    }
```

**See also:** [deploy spec](/reference/deploy-spec), [cornus deploy](/cli/deploy)
