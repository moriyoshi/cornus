# The browser UI

A local browser UI for the workloads and Compose projects a cornus server
manages, started with [`cornus web`](/cli/web). This guide covers what the UI
itself does; the [command page](/cli/web) is the flag and synopsis reference.

## How it works

`cornus web` starts an embedded SolidJS application and a client-side
backend-for-frontend (BFF). The UI shows workload lifecycle and detail, Compose
projects and their `depends_on` graph, client-local mounts, tunnels and forwards,
the [ingress settings](#ingress-settings-on-the-overview) on both sides of a
request, configuration files, streaming logs, a [tiled workspace](#workspace) of
file browsers and interactive exec terminals, and a
[metrics dashboard](#metrics-dashboard) over the server's built-in observability
store. The BFF also exposes a workload stats stream for clients.

The BFF runs on the client because Compose structure, local file sources, and live
background-agent sessions are not part of the server's flattened workload API. It
uses the selected connection profile exactly like other client commands. Project
views use the Compose files passed to the command; without a discovered or explicit
file, server workload views still work while project views remain empty.

The UI has no authentication. In the default mode it only listens on loopback:
`--addr` must use `localhost` or a loopback IP literal; wildcard and non-loopback
addresses are rejected unless you pass [`--allow-non-loopback`](#binding-off-host).
With [`--publish-in-conduit`](#one-browser-proxy-setting-for-the-ui-and-the-workloads)
it binds no listener at all and is reached only through the SOCKS5 conduit, which is
itself loopback, so the no-auth boundary is unchanged either way.

## Browsing directories the project does not mention

The file explorer's local roots are derived from the Compose project: the project
directory, plus any bind-mount source that resolves outside it. That covers the
common case and nothing else — a scratch directory, a sibling checkout, or a data
drive is perfectly browsable to you and invisible to the explorer, and declaring a
bind mount you do not otherwise want is a poor way to ask for one.

`--local-root` names them directly:

```sh
cornus web --local-root ~/scratch --local-root notes=~/wiki:ro
```

Each value is `[LABEL=]DIR[:ro]`. The label is what the source switcher shows and
defaults to the directory's base name; `:ro` makes the root read-only, and that is
a real refusal on the write endpoints, not a hint. The directory must exist — it is
resolved to an absolute path and checked when the command starts, rather than
failing later on the first listing.

Declared roots are subject to the same confinement as any other: paths are resolved
against the root and cannot escape it, and the filesystem root and kernel
pseudo-filesystems are refused outright (the switcher says which source was refused
and why).

They also work with **no Compose project at all** — `cornus web --local-root DIR` in
a directory with no compose file gives you the explorer over that directory, with
the server's workload views alongside it. Roots named here rank above bind-mount
sources in the switcher, so with no project the first `--local-root` is the default.

With [`--publish-in-conduit`](#one-browser-proxy-setting-for-the-ui-and-the-workloads)
the paths are resolved in YOUR working directory and sent to the background agent
absolute, since the agent's own working directory is frozen elsewhere.

## Binding off-host

Sometimes the browser is not on the machine running `cornus web` — the CLI runs on a
jump host, in a devcontainer whose ports are forwarded from another interface, or on
a workstation you want to reach from a tablet on the same LAN. `--allow-non-loopback`
permits a wildcard or non-loopback `--addr` for those cases:

```sh
cornus web --addr 0.0.0.0:8080 --allow-non-loopback --allow-host box.lan
```

Understand what it hands out. The UI and its MCP endpoint have no authentication and
expose exec, persistent terminals, and file writes against your connection profile,
so **anyone who can reach that port gets all of it**. Prefer an SSH tunnel
(`ssh -L 8080:127.0.0.1:8080 host`) or [`--publish-in-conduit`](#one-browser-proxy-setting-for-the-ui-and-the-workloads),
both of which keep the listener loopback-only. Use this flag when the network in
front of the port is one you already trust, and say so with `--addr` rather than
binding the wildcard when you can.

`--allow-host` names the Host header values the origin answers to, keeping the
DNS-rebinding guard on: pass the LAN IP or hostname you type into the browser
(repeat the flag for several). Requests arriving under any other name are refused
with `421 Misdirected Request` naming the flag that would accept them. If you pass
`--allow-non-loopback` with no `--allow-host` at all, the guard cannot be enforced
against names it was never told, so it is dropped entirely and startup warns that it
is off.

`--allow-non-loopback` and `--allow-host` are mutually exclusive with
`--publish-in-conduit`, which binds no local listener; the published UI answers to
`--publish-name`.

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

You do not have to repeat your workload sessions' conduit settings here. The UI
**joins** whichever shared SOCKS5 conduit the background agent is already running
for this connection, whatever settings it was started with, and only falls back to
its own resolved settings when there is no conduit to join. That is what makes the
one-setting promise hold in practice: a `cornus web` that resolved a slightly
different configuration — most often because your profile enables
[ingress via the conduit](/guides/networking#reach-a-workload-s-ingress-host-through-the-conduit),
which this command has no flags for — used to start a *second* proxy that then
collided with the first on one bind address.

The published name follows the conduit it joined, so with a custom
`--socks5-service-host-suffix` of `.demo.internal` the UI answers at
`http://demo.internal/`, next door to the workloads rather than in a namespace of
its own. `--publish-name` still overrides it outright.

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
- `--conduit` here **pins** an address rather than selecting a conduit: naming one
  (`--conduit socks5://127.0.0.1:1080`, `--conduit socks5://?suffix=.demo.internal`)
  means "use exactly this", which is how you deliberately choose between several
  proxies. A bare `--conduit socks5`, `CORNUS_CONDUIT`, and the profile name no
  address, so the UI goes wherever a proxy is already running — including one
  started by a foreground `compose up` in another terminal.
- If more than one proxy is running, the UI joins the lowest-numbered port and says
  which it passed over; the banner printed at startup always names the address your
  browser must actually point at.

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
the same DNS-rebinding Host guard — including when [`--allow-non-loopback`](#binding-off-host)
widens it, which exposes `exec_run` and `file_write` to the network alongside the UI.
With `--publish-in-conduit` the MCP endpoint is
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
counter reset rather than as negative traffic.

A metric family this deploy backend has no source for is **left out of the
dashboard** rather than drawn as a permanently empty chart — network I/O, disk
I/O, processes, and cumulative CPU on Kubernetes; instantaneous CPU everywhere
else. The server names them in `cornus observe status`
(`metrics.unsupported`), and a line under the filters says which panels are
missing and why, so an absent chart is still answerable. A metric the backend
*can* report but nothing has sent yet keeps its panel and shows "nothing has
reported this yet", which is what the store itself answers.

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

## Ingress settings on the Overview

The Overview carries an **Ingress** section, between the summary cards and the
per-project sections. A request to an ingress host traverses two independent
settings, and the section describes both — the server's is not the client's, and
neither one predicts the other.

**Front door** is what the server advertises (`GET /.cornus/v1/info`):

| Row | Meaning |
| --- | --- |
| Mode | `cluster` when a real ingress controller realizes ingress; `emulated` when the server routes to workloads with its own host/path table, which is what the host backends do. |
| Base domain | `CORNUS_INGRESS_DOMAIN`. Predicts the host a deploy is given when its Compose block names none. |
| Class | `CORNUS_INGRESS_CLASS`, stamped on every Ingress this server creates. |
| Listen | Where an emulated front door is bound (`CORNUS_INGRESS_LISTEN`). Unset means it is reachable only through an [ingress tunnel](/cli/ingress-tunnel). Shown for an emulated front door only. |
| Controller | The in-cluster controller Service a native passthrough port-forwards to. "none discovered" is why a client falls back to emulating ingress itself. |

**This client** is how your own conduit realizes ingress — the
`--ingress-conduit` setting, read from the background agent:

| Row | Meaning |
| --- | --- |
| Mode | `native` tunnels straight to the cluster's controller, which does the routing and serves its own TLS; `emulate` runs a client-side reverse proxy reached through the conduit. |
| Domain | The suffix ingress hosts are derived under; "conduit default" means the conduit's own service-host suffix. |
| Controller | The passthrough target, in native mode. |
| Trust | What a browser has to accept, in emulate mode: the per-host certificates and the fallback CA, which is normally one the conduit generated for this session. This is the row that turns a browser TLS error into an action. Native mode reports none — the real controller serves its own certificate. |

A server that advertises no front door, and a client that routes no ingress, both
say so rather than disappearing. So does a client whose settings could not be read
at all: with no background agent running there is nothing to ask, which the section
reports as unknown rather than as none.

See [Ingress](/guides/ingress) for configuring either half.

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

### The workspace is bigger than the screen

Placing a pane says *where* it goes; what it **costs** is a separate question, and the
answer is that it costs nothing once there is no room left to give.

While there is room, a split is an ordinary split: the pane halves, and the workspace
still fits the screen. When halving would leave a pane narrower than **40rem** or shorter
than **20rem** — about the point where a listing or a terminal stops being worth reading —
the split stops taking space and starts making it, and the screen becomes a window onto a
workspace now wider (or taller) than it is.

What it does is one rule in three steps: the workspace is **multiplied by the golden ratio**
along the axis you split; everything already open shares **two thirds** of the result, keeping
exactly the proportions it had; and the **new pane gets the remaining third**. So no two panes
change their relationship to each other — they all grow by the same small factor to make room —
and the new pane arrives at a size set by the workspace rather than by whichever pane you
happened to split. If that third would be under the 40rem floor, the new pane gets the floor
instead. Panes along the
edge it grew at stretch to meet it; everything else stays exactly where it was.

**Move pane** is measured the same way, against the tile it lands next to: dropped beside a
pane with room to spare it simply shares that space, and only when halving would go under the
floor does the workspace grow to take it.

The two floors differ because a pane is not square and neither is a screen: 40rem is a
listing with its columns intact or an 80-column terminal, while 20rem is about eighteen
terminal rows, and asking the same of both axes would demand far more height than any
ordinary display has to give.

So the workspace behaves like any tiling window manager until it runs out of room, and
only then starts scrolling. On a 2560px display the first splits all divide; on a 1080p
one, side-by-side divides and so does the first top/bottom split, with the second
extending; on a 1280px laptop side-by-side already extends, because half of it is under
the width floor to begin with.

So the workspace scrolls, and it follows you: focusing a pane brings its tile into view,
scrolling the least that makes it fully visible. A pane opened off to the left arrives on
screen without the rest of the layout appearing to jump. **Choose a pane** (`prefix s`) walks
the view along with it — the arrows step from tile to tile and the workspace pans to keep the
highlighted one in sight, while the focus stays where it was until you press Enter. Esc leaves
the view exactly where the walk started, so looking around costs nothing.

Tab is the walk's other route. Where the arrows step from tile to tile, Tab steps from pane to
pane — every one of them, in the order the list has them and the numbers count them, wrapping at
the end, with Shift+Tab going back. That takes it through the *tabs* of a stacked tile as well,
which is the one move no direction can make: stacked tabs share a place on screen, so there is
no arrow that tells them apart. Press Tab as many times as you like without looking anything up,
or press the digit if you already know the number.

Turn on *Settings → Workspace → Mini map in the pane chooser* and the chooser draws the whole
workspace above its list — one rectangle per pane, in the shape and the proportions the
workspace really has. The rectangle you are walking is highlighted, the pane holding the focus
wears its filled number, and an accent frame shows how much of the workspace is on screen and
where. On a workspace several screens wide that frame is the only place the app says so. Click
a rectangle to go to that pane, the same as clicking its row.

A tile holding several tabs gets a number for each of them, since they all share one place on
screen — and each number is its own target, which is the only way to reach a background tab by
pointing at it. The numbers wrap down the rectangle, so a tall tile holds several rows of them
rather than one line and an apology. Only when the rows run out too are the rest replaced by an
ellipsis, so a cell never understates how many tabs a tile has. The numbers kept are the ones
around the tab that tile is currently showing, so walking the panes with Tab always moves a
number you can see.

It is off by default because it is worth having only once the workspace has outgrown the
display: on a layout that fits, the tiles are already in front of you and a picture of them is
a picture of what you can see. With it on, the map appears once there are at least two tiles,
and the frame only once there is something off screen.

**Pin the pane chooser** and it stops being a mode. The pin in the panel's top corner — faded
until you press it — stands the chooser permanently in a gutter at the side of the screen,
where it lists every pane whether or not you asked for one, marks the pane you are in, and goes
to a pane when you click its row. It covers nothing: the workspace gives up the gutter's width
rather than passing underneath it, so the tiles are laid out in what is left. `prefix s` still
walks the tiles exactly as before — the walk reports into the panel that is already there,
borrowing its title and its highlight for as long as it lasts, and Enter or Esc ends the walk
and leaves the panel standing.

The chooser hangs at the **start of the reading direction** — the left in a left-to-right
language, the right in a right-to-left one — pinned or not, so pinning moves it outward rather
than across the screen. *Settings → Workspace → Gutter side* overrides that for the gutter:
*Automatic* follows the language, *Left* and *Right* say so outright.

**How a transfer inside a container happens depends on the deploy backend.** On the
host backends (Docker, containerd, bare) every path in a running container is read and
written directly. On **kubernetes** there are two routes and the explorer picks between
them:

- Paths inside the volumes the workload declares go through its **caretaker sidecar**,
  which is given those volumes and nothing else. This is the preferred route: it needs
  nothing from the application image, it reports real errnos, and a copy between two
  paths of one volume never leaves the pod.
- Every other path — anything in the container **image** — goes through **tar run inside
  the container**, the same mechanism `kubectl cp` uses. It is the only way to reach a
  mount namespace the caretaker does not share.

The consequence worth knowing: the tar route needs a `tar` in the application image. A
distroless image has none, so image-layer paths there can be browsed (listing is an
exec, which always works) but not transferred, and the refusal says so. Volume-backed
paths keep working on such an image, because that route never touches it. A pod whose
caretaker has not connected yet gives a similar refusal for a different reason; the
message distinguishes them, and that one clears on its own once the pod is up.

**Copy and Move do not use the gutter.** `prefix s` is the chooser's own question and belongs in
its standing home, but *Copy … to another pane* and *Move … to another pane* are questions with
an answer due, so they open a card of their own on the **other side** of the screen and leave the
gutter alone. The card is a plain list of destinations: no mini map, no mark saying where the
focus is, and no activity badges — a transfer picks by name, by kind and by which rows are greyed
out, and it deliberately leaves the focus where it is. A pane that is *working* or *needs you*
still says so on its tab, where that is what a tab is for; in a list of places to send files it
would read as a warning about sending there. Unpinned, those questions look as they always did.

The pin appears only where a gutter is affordable: a window at least 960px wide, driven by a
mouse rather than a finger. On a phone or a tablet the chooser floats however the setting is
left, so a preference synced from a desk does not cost a small screen a column.

To move the view yourself, use a trackpad or the scrollbars — or, **on a touch screen, drag
with two fingers**. One finger belongs to whatever is under it: a listing scrolls, a terminal
scrolls, the tab bar slides sideways, a divider resizes. Two fingers are the gesture nothing
else wants, so they pan the workspace from anywhere, including from the middle of a terminal.
Pinching still zooms, when *Settings → Workspace → Pane zoom* is on: fingers that keep their
distance pan, fingers that spread zoom.

Two commands resize a pane against its neighbours, on tmux's own binds — `prefix Alt+←/→`
for **narrower** and **wider**, `prefix Alt+↑/↓` for **shorter** and **taller**. With a
mouse, **Alt-drag a divider**: a plain drag trades space between the two panes either side
of it, and an Alt-drag resizes one and lets the workspace take up the difference.

When a layout has grown lopsided — three splits deep on one side and nothing on the other —
**Even horizontal** (`prefix H`) puts every pane in a single row of equal width, keeping every
pane, every tab and every running shell exactly where it is and only changing the arrangement
around them. If an even share of the screen would be narrower than the 40rem floor, the row is
built at the floor and the workspace gets wider instead, so "even" never means "each of eight
panes gets an eighth of the screen".

The workspace's **own right and bottom borders are dividers too**. Every other divider sits
between two panes and trades between them; these have nothing on their far side, so dragging
one resizes the workspace itself — and the panes that grow are the ones touching that border,
exactly as when a split extends it. A pane two columns in does not move. They sit on the
workspace's edge rather than the screen's, so once you have dragged one out past the viewport
you will need to scroll back to it to drag it in again.

**Settings → Workspace → New pane sizing** switches this off. *Divide the pane* is the
classic tmux behaviour — the tiling always fits the screen, so each new pane halves the one
you were on however small that makes it, and nothing ever scrolls. *Extend the workspace*
is the default.

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
   [Compose extensions](/guides/compose-support#interactive-shell-candidates));
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
operation as the [`project_apply` MCP tool](#mcp-endpoint-for-agent-clients).)

**See also:** [`cornus web`](/cli/web), [`cornus compose`](/cli/compose),
[Networking and conduits](/guides/networking), [Observability](/guides/observability),
[connection configuration reference](/reference/connection-config)
