# cornus web

Serve a local browser UI for workloads and Compose projects managed by a cornus server.

## Synopsis

```sh
cornus web [flags]
```

## Description

`cornus web` starts an embedded SolidJS application and a client-side
backend-for-frontend (BFF). The UI shows workload lifecycle and detail, Compose
projects and their `depends_on` graph, client-local mounts, tunnels and forwards,
configuration files, streaming logs, and an interactive exec terminal. The BFF also
exposes a workload stats stream for clients.

The BFF runs on the client because Compose structure, local file sources, and live
background-agent sessions are not part of the server's flattened workload API. It
uses the selected connection profile exactly like other client commands. Project
views use the Compose files passed to this command; without a discovered or explicit
file, server workload views still work while project views remain empty.

The UI has no authentication. In the default mode it only listens on loopback:
`--addr` must use `localhost` or a loopback IP literal; wildcard and non-loopback
addresses are rejected. With `--publish-in-conduit` it binds no listener at all and
is reached only through the SOCKS5 conduit (see below), which is itself loopback, so
the no-auth boundary is unchanged either way.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | Loopback listen address. Port `0` chooses an available port. Mutually exclusive with `--publish-in-conduit`. |
| `-H`, `--host` | `CORNUS_HOST` | profile, then `http://localhost:5000` | cornus server endpoint. |
| `-f`, `--file` | — | Compose discovery | Compose file(s). Repeatable. |
| `--env-file` | — | `.env` discovery | Env file(s) used for Compose interpolation. Repeatable; replaces default discovery. |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose directory name | Project name. |
| `--open` | — | `false` | Open the UI in the default browser after the listener starts. |
| `--frontend` | `CORNUS_WEB_FRONTEND` | embedded assets | Detached frontend development-server URL. Non-BFF requests are reverse-proxied there while the real BFF stays on the same origin. |
| `--mcp` / `--no-mcp` | — | `true` | Co-host an MCP (Model Context Protocol) server at `/.cornus/mcp` for agent clients. `--no-mcp` disables it. |
| `--mcp-stdio` | — | `false` | Serve only the MCP server over stdin/stdout instead of binding an HTTP listener, for agent clients that launch a command. Binds no port. Mutually exclusive with `--publish-in-conduit`. |
| `--publish-in-conduit` | — | `false` | Host the UI inside the background agent and publish it in the shared SOCKS5 conduit instead of binding a local port. |
| `--publish-name` | — | suffix apex (e.g. `cornus.internal`) | Conduit host name to publish the UI under. Implies `--publish-in-conduit`. |
| `--publish-port` | — | `80` | Conduit port the published name answers on. |
| `--conduit` | `CORNUS_CONDUIT` | profile | SOCKS5 conduit selector (bare `socks5`, or `socks5://host:port[?suffix=SUFFIX]`) for `--publish-in-conduit`. |

## One browser proxy setting for the UI and the workloads

When you reach a cornus server's workloads through the SOCKS5 conduit — a browser
whose proxy is set to `cornus socks5` (or `cornus config set-context --conduit-mode
socks5`), resolving `*.cornus.internal` names — the `cornus web` UI is a separate
`http://127.0.0.1:<port>` that needs its own browser setting. `--publish-in-conduit`
removes that split:

```sh
cornus web --publish-in-conduit
```

This hands the UI's backend to the background agent, which serves it on an
in-process listener and publishes it in the **shared** conduit under
`cornus.internal` (the service-host suffix apex). The UI then answers at
`http://cornus.internal/` through the very same proxy that reaches the workloads —
one browser proxy setting for both. It binds no local port, so nothing new is
exposed; the UI is reachable exactly where the proxy is.

The command stays in the foreground and withdraws the name when it exits (or is
killed). If the agent restarts, it re-publishes automatically.

Notes:

- The browser must do **remote** DNS through the proxy (SOCKS5h), so `cornus.internal`
  is resolved by the proxy rather than locally — the same requirement the
  `*.cornus.internal` workload names already have.
- Only `http://` is served at the published name (not `https://`).
- Your workload sessions should use the **socks5** conduit too. If they run in the
  default port-forward mode, the UI still resolves and workloads still resolve by
  their full deployment name, but compose short names (e.g. `web.cornus.internal`
  for a service deployed as `demo-web`) will not — those aliases are registered only
  by socks5-mode workload sessions.
- The conduit settings passed here must match those your workload sessions use, or
  the two proxies collide on one bind address.

## MCP endpoint for agent clients

The same server co-hosts an [MCP](https://modelcontextprotocol.io) (Model Context
Protocol) server at `/.cornus/mcp`, so agent clients — Zed's Agent panel, Claude
Desktop, and others — can drive the same client-side capabilities the UI exposes:
list and act on workloads, read the dependency graph and mounts, tail logs, run a
one-shot command, and read or write the allow-listed Compose/env/config files. It is
on by default; pass `--no-mcp` to disable it.

MCP tools are thin adapters over the exact same logic the UI's BFF uses, so the two
surfaces never drift. Streaming stays UI-only: interactive terminals and live
log/stats streams do not fit MCP's request/response model, so MCP gets a bounded
`logs_tail` (last N lines) and a one-shot `exec_run` (captured stdout/stderr/exit)
instead.

MCP inherits the UI's threat model verbatim: the same loopback/no-auth boundary and
the same DNS-rebinding Host guard. With `--publish-in-conduit` the MCP endpoint is
published in the same SOCKS5 conduit as the UI, which exposes `file_write` and
`exec_run` to conduit users exactly as the UI is already exposed — use `--no-mcp` if
you want a narrower blast radius there.

Most MCP clients launch a command over stdio rather than dial an HTTP URL. For those,
run `cornus web --mcp-stdio`, which serves the identical tool surface over stdin/stdout
and binds no HTTP listener. It reuses the same connection profile and Compose flags
as the browser UI; diagnostics go to stderr so they never corrupt the JSON-RPC stream
on stdout. Register it with a client as, for example:

```json
{
  "command": "cornus",
  "args": ["web", "--mcp-stdio", "-f", "compose.yaml"]
}
```

## File editing and apply

The editor is restricted to the resolved Compose files, env files, and client
configuration file. Arbitrary paths and traversal spellings are rejected. Applying a
project runs the equivalent of `cornus compose ... up -d`, so the standard Compose
reconcile and background-agent behavior remains authoritative.

## Examples

Start on an automatically selected loopback port using the current connection
profile and discovered Compose file:

```sh
cornus web --open
```

Select a remote server and project explicitly:

```sh
cornus web --host https://cornus.example.com:5000 \
  -f compose.yaml -p demo --addr 127.0.0.1:8080
```

Run Vite separately with hot reload while keeping the real BFF on one origin:

```sh
cornus web --frontend http://localhost:5173
```

Publish the UI in the SOCKS5 conduit so one browser proxy setting reaches both the
UI and the workloads:

```sh
cornus config set-context --conduit-mode socks5   # workload sessions use socks5 too
cornus socks5 &                                    # the proxy your browser points at
cornus web --publish-in-conduit                    # UI at http://cornus.internal/
```

See [`cornus compose`](/cli/compose), [`cornus daemon`](/cli/daemon), and the
[connection configuration reference](/reference/connection-config).
