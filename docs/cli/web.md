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
configuration files, streaming logs, a [tiled workspace](#workspace) of file
browsers and interactive exec terminals, and a
[metrics dashboard](#metrics-dashboard) over the server's built-in observability
store. The BFF also exposes a workload stats stream for clients.

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

One tool goes the other way. `project_apply` re-deploys the loaded project — the
equivalent of `cornus compose ... up -d`, so the standard Compose reconcile and
background-agent behavior remains authoritative — and it has no counterpart in the
UI. The UI accompanies the CLI rather than replacing it, so re-deploying belongs in
the terminal you already have open; an agent driving MCP has no such terminal.

Agents also get the server's [flight record](/cli/activity), which is what answers
"what went wrong" after the fact rather than "what is true now": an `activity_read`
tool with the same `since`/`kind`/`unfinished` filters as the CLI, and a
`cornus://activity/unfinished` **resource** — the set of things the server and its
caretakers began and never finished. The resource form is the useful one: a client
can attach it like a file, so an agent asked about a misbehaving deployment starts
out already knowing the last server died mid-flight. Both carry `liveInstance`
alongside the records, without which the serving process's own open lifetime reads
as a crash. Following (`cornus activity --follow`) stays CLI-only for the same
reason the log stream does.

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

## Metrics dashboard

The **Metrics** screen charts what the server's [built-in observability
store](/guides/observability) has recorded. It needs no instrumentation in your
workloads and no configuration in the UI — but it does need the store, so start the
server with `--obs`:

```sh
cornus serve --obs
```

Without it every observability route answers `501` and the screen says so, naming
the flag rather than reporting an error.

The **Scope** switch beside the page title chooses what the dashboard is about:
**Workloads** (CPU, memory, memory limit, network I/O, disk I/O, processes) or
**Server** (the cornus process's own CPU, memory, Go heap, goroutines, threads, file
descriptors, network I/O, and its cumulative build and deploy counts).

One filter row narrows it:

| Control | Effect |
| --- | --- |
| **Range** | Last 15 minutes / hour / 6 hours / 24 hours. The step and the refresh interval follow the range, so a 24-hour window is not re-read every 15 seconds. |
| **Workload** | Narrows the workload panels to one deployment. Workloads scope only. |

Every panel carries its current figure, one line per series (per replica, per CPU
mode, per I/O direction), and a **Table** toggle that reads the same series as
last/min/max/average — so no value is reachable only by hovering. Hovering or
focusing a chart and using the arrow keys moves a crosshair that reads out every
series at that instant.

Cumulative counters (`container_cpu_time`, `container_network_io`,
`container_disk_io`, `process_cpu_time`, `cornus_server_network_io`) are
differentiated into per-second rates in the browser, and a decrease is treated as a
counter reset rather than as negative traffic. A metric this deploy backend never
reports — `container_cpu_usage` outside Kubernetes, or network and disk I/O on
Kubernetes — shows as "nothing has reported this yet", which is what the store
itself answers.

A panel draws at most eight series, the number of reliably distinguishable colors
in the palette, and states how many it is withholding. Filter to one workload, or
read the table, to reach the rest.

The scope travels in the URL — `/metrics?workload=shop-web&range=6h` — so a view
can be linked to and shared, and an unknown value folds back to the default rather
than emptying the page.

::: tip The same data from the command line
The dashboard queries the same store as [`cornus observe
metrics`](/cli/observe#cornus-observe-metrics), which takes arbitrary PromQL and can
reach the metrics your own workloads export. The dashboard covers what Cornus
records for you.
:::

### Charts where you already are

The same panels appear next to the things they describe, so the usual question —
"is this one busy?" — does not require a trip to the dashboard:

- **Each project and workload section on the Overview** carries a two-panel strip:
  CPU and memory for everything under that heading, over the last hour. A project
  strip covers that project's deployments and no others. **All metrics →** opens
  the dashboard already scoped to the same thing.
- **A workload's own page** carries a **Metrics** section — the page lays out
  instances, spec, metrics, and logs one after another — with the full workload
  panel set for that deployment alone, and its own range control.

The strips appear only when the server has a store; without `--obs` the Overview is
unchanged rather than growing an explanation under every heading.

A CPU panel in these views merges the two backend spellings —
`container_cpu_time` on host backends, `container_cpu_usage` on Kubernetes — into
one chart, since they are the same quantity in the same unit. The full dashboard
keeps them as separate panels, where an empty one names the backend it belongs to.

A stopped deployment still charts the window behind it. What a workload is doing
*now* is the status badge's job; the chart's job is what it did.

## Workspace

**Workspace** is one tiled screen holding two kinds of pane: a file browser over
the unified namespace of local mounts and running containers, or an interactive
terminal on a workload. Tiles split, stack as tabs, and rearrange the same way
whatever they hold, and the layout survives a reload.

It opens as a single file browser at the mount list. From inside a running
workload, **Open in a terminal** (`prefix t`) opens a shell **in the directory you
are browsing** — the folder on screen, not a row you have selected inside it — and
the pane is placed by pointing at a tile the way every other new pane is. A terminal
is a place to stand rather than something done to a row, which is why this one
command ignores the selection that **Open** and **New pane** both read.
The command is always listed and says why when it cannot run: at the mount
list there is no workload named yet, a local folder has no container to attach to,
and a stopped workload says so by name.

**Open** puts the selected row in a pane of its own — the editor for a text file,
the viewer for an image, a second listing for a folder. It is one command for all
three; the palette names the row it will open, with a trailing slash when that row
is a folder (`Open "logs/"…`). `Ctrl+Enter` (`Cmd+Enter` on a Mac) runs it without a
prefix, and on a file so do Enter, a double-click, and a click on the name.

Every route asks **where the pane should go** by lighting the tiles with placement
targets: press Space for a tab on the tile you are on, an arrow (or `hjkl`) to split
beside it, Esc to change your mind. Plain Enter on a *folder* still descends into it
in place — the modifier is what says "not here, somewhere I will point at". Open is
always listed inside a mount and says why when it cannot run: nothing selected, more
than one row, or a file neither the editor nor the viewer can show.

If you always give the same answer, say so once instead. **Settings → Workspace → New
pane placement** offers *Ask where it goes* (the default), *Always side by side*, and
*Always as a tab*; the latter two are those same two answers made standing, so every
command that creates a pane skips the prompt and places it. Nothing becomes reachable
or unreachable either way — the setting only chooses which of the prompt's own answers
is taken. **Split pane** is unaffected: it already says which disposition it makes and
only asks which edge.

::: warning The working directory is a preference, not a guarantee
It is sent as the exec's working directory, which the docker, containerd,
bare-host and Incus backends honour. Kubernetes cannot express one — `PodExecOptions`
has no such field — so a terminal on a Kubernetes workload starts wherever the image
puts you.
:::

## Terminal shell discovery

Opening a terminal on a workload does not guess a shell — it finds one. The BFF
runs a probe inside the running container and connects to the best interactive
shell the image actually has, so an image with `bash` gives you `bash` and an image
with only busybox still gives you a shell.

Candidates are tried in this order, and the first one present wins:

1. the shell named by the workload's own `entrypoint:` or `command:`, when that is
   a shell — the image author's choice, and the one candidate already evidenced;
2. the workload's `x-cornus-shells:` list (see
   [Compose support](/cli/compose#interactive-shell-candidates));
3. the `shells:` list on the selected [connection profile](/reference/connection-config);
4. the browser's own list, under **Settings -> Terminal -> Shell candidates**.

The lists are concatenated rather than replaced, so a more specific source raises
its entries to the front without removing the fallbacks. Each entry is a command
string, not a pre-split argument list: `/bin/busybox sh` is one entry and is split
the same way Compose splits `command:`.

The default browser list is, in order: `/bin/zsh`, `/usr/bin/zsh`, `/bin/bash`,
`/usr/bin/bash`, `/bin/dash`, `/usr/bin/dash`, `/bin/ash`, `/usr/bin/ash`,
`/bin/sh`, `/usr/bin/sh`, `/busybox/sh`, `/bin/busybox sh`, `/usr/bin/busybox sh`.

When no candidate is present — a distroless or scratch image — the pane says so and
asks for a command to run instead of failing with a generic connection error. A
pane remembers the shell it settled on, so reopening or reloading it does not probe
again.

Probing costs one exec round trip per candidate the image does *not* have, and
stops at the first that runs: a shell that starts reports on every candidate at
once. Results are cached per workload for 30 seconds.

The `shells:` profile field is treated as security-sensitive, because it names a
binary that gets executed inside your workload. A per-project
`cornus-context.yaml` supplies it only with `--trust-context-file`; an
auto-discovered one has it stripped.

## File editing

The editor is restricted to the resolved Compose files, env files, and client
configuration file. Arbitrary paths and traversal spellings are rejected.

Editing a Compose file does not re-deploy anything: run `cornus compose up -d`
yourself when you want the change applied. The UI has no apply button — it is a
companion to the CLI, not a second front door to it. (Agent clients get the
operation as the `project_apply` MCP tool; see above.)

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
