# cornus hub

Join the cornus workload-to-workload overlay as a spoke from anywhere (for
example a developer laptop).

## Synopsis

```sh
cornus hub [flags]
```

## Description

`cornus hub` connects this host to the cornus overlay as a spoke, reusing the
caretaker hub role. Services this host offers are registered for delivery — the
hub relays inbound traffic to this spoke, which dials the local target, so a
NAT'd host need not be reachable by the hub. Services this host reaches bind a
local loopback listener that funnels into the hub. At least one `--register` or
`--reach` entry is required.

The connection is resolved from `--server`, otherwise from the selected
connection profile — including its token / kube-auth and automatic port-forward
(see [`cornus config`](/cli/config)). The resolved token rides the caretaker's
WebSocket handshake and the profile's TLS material rides the dial. Flags are
validated before the connection is resolved, so bad flags never start a
port-forward. It runs until `Ctrl-C` (or `SIGTERM`).

See [the workload hub](/guides/hub) for the overlay model.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | Hub URL (`ws(s)://` or `http(s)://`) of the cornus server. Falls back to the selected connection profile. |
| `--identity` | — | — | This spoke identity (used for hub policy). |
| `--register` | — | — | Offer a local service to the overlay: `name=host:port` (relayed to this spoke via delivery). Repeatable. |
| `--reach` | — | — | Reach an overlay service: `name=listen_ip:port` (binds the local listener). Repeatable. |

## Examples

Offer a locally running service to the overlay:

```sh
cornus hub --identity laptop --register api=127.0.0.1:8080
```

Reach an overlay service on a local loopback port:

```sh
cornus hub --identity laptop --reach db=127.0.0.1:5432
```

Do both at once:

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```
