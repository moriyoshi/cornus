# cornus socks5

Run a client-side SOCKS5 split-tunnel proxy that reaches a cornus server's
workloads by name.

## Synopsis

```sh
cornus socks5 [flags]
```

## Description

`cornus socks5` binds a local SOCKS5 proxy. A `CONNECT` target whose `host:port`
matches a resolution rule — by default, any host bearing the
`--service-host-suffix` (for example `web.cornus.internal`) — is tunneled to
that service through the server's port-forward transport; every other
destination is dialed directly from this machine. It stays in the foreground
until `Ctrl-C` (or `SIGTERM`).

This is the ad-hoc counterpart to the per-session conduit mode selected by
`cornus config set-context --conduit-mode socks5`. It starts from the profile's
SOCKS5 settings, then applies any explicit flag overrides. See
[Remote workflows](/topics/remote-workflows) for how the SOCKS5 conduit relates
to port-forward and the other ways to reach workloads.

The connection is resolved from `--server`, otherwise from the selected
connection profile (see [`cornus config`](/cli/config)).

A socks5 conduit can also reach a workload's declared ingress host by name — see
[Public ingress](/topics/ingress#reaching-an-ingress-from-your-machine-through-the-socks5-conduit).

### Resolution rules

`--service-host-suffix` is the simple case: any host ending in the suffix is
tunneled to the matching service and the suffix is stripped to derive the
service name. `--resolve PATTERN=REPLACE` is the advanced form: rules are
ordered and the first match wins, replacing the suffix default. `PATTERN`
matches `host:port` and `REPLACE` yields `service:port`, with sed-style `\1`
backreferences. `PATTERN` must compile as a regular expression.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | Remote cornus server URL (`http(s)://` or `ws(s)://`). Falls back to the selected connection profile. |
| `--listen` | — | `127.0.0.1:1080` | Local address to bind the SOCKS5 proxy on (or the profile value). |
| `--service-host-suffix` | — | `.cornus.internal` | Host suffix whose `CONNECT` targets are tunneled to the matching service; other hosts egress directly. |
| `--resolve` | — | — | Advanced resolution rule `PATTERN=REPLACE` (repeatable, ordered, first match wins); replaces the suffix default. |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | Route tunneled connections through the cornus server proxy instead of connecting to pods directly with your kubeconfig (cluster profiles only). `--no-via-server` forces the direct path. Overrides `CORNUS_VIA_SERVER` and the profile. |
| `--allow-non-loopback` | — | `false` | Permit binding `--listen` to a non-loopback address (see the security note below). |

## Loopback only, by default

The proxy performs no authentication (it offers the SOCKS5 no-auth method) and
dials arbitrary unmatched destinations from the machine it runs on. Bound to a
non-loopback address it is therefore an **open proxy** for anyone who can route to
it — including a path into that machine's own loopback services. For that reason a
non-loopback `--listen` (for example `0.0.0.0:1080`) is refused unless you pass
`--allow-non-loopback` to state the exposure is intended. Even then, the proxy
refuses to dial loopback and link-local destinations, so it cannot be used to reach
services on the proxy host.

## Examples

Start the proxy on the default address:

```sh
cornus socks5
```

Then point a client at it and reach a service by name:

```sh
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

Bind a custom address and use a different service-host suffix:

```sh
cornus socks5 --listen 127.0.0.1:1085 --service-host-suffix .svc.local
```

Use an advanced resolution rule instead of the suffix default:

```sh
cornus socks5 --resolve '^(.+)\.internal:(\d+)$=\1:\2'
```
