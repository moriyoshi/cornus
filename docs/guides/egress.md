# Egress

Task-oriented recipes for routing a remote workload's outbound traffic through the
caller's network — for a VPN, a corporate proxy, a SASE gateway, or an air-gapped
cluster. For the model behind them see [client-side egress](/topics/egress) and
the [deploy spec](/reference/deploy-spec). To hand a workload a caller-minted
secret instead, see the [Credentials](/guides/credentials) guide.

## Route a remote workload's outbound traffic through the caller network

Send a workload's egress through your machine's network for a VPN, corporate proxy, or air-gapped cluster.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --egress proxy
```

- `--egress` modes are `env` (propagate the caller's proxy env vars, every backend, no relay), `proxy` (caretaker forward proxy relayed back through the server), or `transparent` (nftables redirect, covers apps that ignore proxy vars).
- A `client` route needs a live deploy-attach session. Direct `cornus deploy --detach` therefore rejects it; `cornus compose up -d` keeps the session in the background agent. In Compose use the `x-cornus-egress:` extension.

**See also:** [client-side egress](/topics/egress), [cornus deploy](/cli/deploy)

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

**See also:** [client-side egress](/topics/egress), [cornus deploy](/cli/deploy)

## Set the default egress route

Choose where destinations no rule matches go; the default is `cluster`, so enabling egress never silently diverts in-cluster traffic.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-route 'api.internal=client' --egress-default deny
```

- `--egress-default` is one of `cluster` (default), `client`, `gateway`, or `deny`.
- The `client` route needs a live session; for durable detached egress route only to `gateway` / `cluster` / `deny`.

**See also:** [client-side egress](/topics/egress), [cornus deploy](/cli/deploy)

## Use a PAC-style policy script for egress

Replace the rule list with a PAC-compatible `FindProxyForURL` program, so an existing corporate PAC file drops in.

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

- `--egress-pac` supersedes `--egress-route`. Returns map as `DIRECT` -> `cluster`, `PROXY client` / `PROXY gateway` -> that route, `DENY` -> drop, no match -> `--egress-default`.
- The script is sandboxed (no `require`, no live I/O) and fails closed to `deny` on error or timeout.

**See also:** [client-side egress](/topics/egress), [cornus deploy](/cli/deploy)
