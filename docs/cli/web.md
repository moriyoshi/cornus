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
ingress settings on both sides of a request, configuration files, streaming logs, a
tiled workspace of file browsers and interactive exec terminals, and a metrics
dashboard over the server's built-in observability store. It also co-hosts an MCP
server for agent clients.

The BFF runs on the client and uses the selected connection profile exactly like
other client commands. Project views use the Compose files passed to this command;
without a discovered or explicit file, server workload views still work while
project views remain empty.

The UI has no authentication. In the default mode it only listens on loopback:
`--addr` must use `localhost` or a loopback IP literal; wildcard and non-loopback
addresses are rejected unless you pass
[`--allow-non-loopback`](/guides/web-ui#binding-off-host). With
[`--publish-in-conduit`](/guides/web-ui#one-browser-proxy-setting-for-the-ui-and-the-workloads)
it binds no listener at all and is reached only through the SOCKS5 conduit, which is
itself loopback, so the no-auth boundary is unchanged either way.

See [The browser UI](/guides/web-ui) for what each screen does, how the file
explorer and workspace behave, the metrics dashboard, the MCP tool surface, and the
recipes behind the flags below.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | Loopback listen address. Port `0` chooses an available port. Mutually exclusive with `--publish-in-conduit`. |
| `--allow-non-loopback` | — | `false` | Permit binding `--addr` to a wildcard or non-loopback address. See [Binding off-host](/guides/web-ui#binding-off-host). |
| `--allow-host` | — | loopback names | Host header value to serve under, in addition to the loopback spellings. Repeatable. |
| `-H`, `--host` | `CORNUS_HOST` | profile, then `http://localhost:5000` | cornus server endpoint. |
| `-f`, `--file` | — | Compose discovery | Compose file(s). Repeatable. |
| `--env-file` | — | `.env` discovery | Env file(s) used for Compose interpolation. Repeatable; replaces default discovery. |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose directory name | Project name. |
| `--open` | — | `false` | Open the UI in the default browser after the listener starts. |
| `--local-root` | — | project + bind sources | Extra directory the file explorer can browse, as `[LABEL=]DIR[:ro]`. Repeatable. See [Browsing directories the project does not mention](/guides/web-ui#browsing-directories-the-project-does-not-mention). |
| `--frontend` | `CORNUS_WEB_FRONTEND` | embedded assets | Detached frontend development-server URL. Non-BFF requests are reverse-proxied there while the real BFF stays on the same origin. |
| `--mcp` / `--no-mcp` | — | `true` | Co-host an MCP (Model Context Protocol) server at `/.cornus/mcp` for agent clients. `--no-mcp` disables it. See [MCP endpoint for agent clients](/guides/web-ui#mcp-endpoint-for-agent-clients). |
| `--mcp-stdio` | — | `false` | Serve only the MCP server over stdin/stdout instead of binding an HTTP listener, for agent clients that launch a command. Binds no port. Mutually exclusive with `--publish-in-conduit`. |
| `--publish-in-conduit` | — | `false` | Host the UI inside the background agent and publish it in the shared SOCKS5 conduit instead of binding a local port. See [One browser proxy setting for the UI and the workloads](/guides/web-ui#one-browser-proxy-setting-for-the-ui-and-the-workloads). |
| `--publish-name` | — | suffix apex of the conduit it joins (e.g. `cornus.internal`) | Conduit host name to publish the UI under. Implies `--publish-in-conduit`. |
| `--publish-port` | — | `80` | Conduit port the published name answers on. |
| `--conduit` | `CORNUS_CONDUIT` | join the existing conduit | SOCKS5 conduit selector (bare `socks5`, or `socks5://host:port[?suffix=SUFFIX]`) for `--publish-in-conduit`. Naming an address or a suffix **pins** those settings. |

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

Give the file explorer a directory the project does not mention:

```sh
cornus web --local-root ~/scratch --local-root notes=~/wiki:ro
```

Serve only the MCP endpoint over stdio, for an agent client that launches a command:

```sh
cornus web --mcp-stdio -f compose.yaml
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

See [The browser UI](/guides/web-ui), [`cornus compose`](/cli/compose),
[`cornus daemon`](/cli/daemon), and the
[connection configuration reference](/reference/connection-config).
