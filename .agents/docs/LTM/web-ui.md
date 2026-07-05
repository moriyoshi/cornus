# Embedded Web UI

## Summary

`cornus web` hosts an embedded SolidJS single-page application and a client-side
backend-for-frontend (BFF) under `/.cornus/web/*`. The command combines server workload state,
Compose project structure, local configuration, and background-agent inventory that cannot be
reconstructed inside `cornus serve`.

## Key Facts

- The BFF runs in the client command, not the server. It joins `pkg/client`, `pkg/compose`, and
  `clientagent` status data.
- The same BFF also co-hosts an MCP (Model Context Protocol) server at `/.cornus/mcp` (on by default;
  `cornus web --no-mcp` opts out) and over stdio via `cornus web --mcp-stdio`. MCP tools are thin
  adapters over the same value-returning operation core (`core.go`) the `/.cornus/web/*` handlers use,
  so the two surfaces cannot drift.
- The unauthenticated UI is loopback-only; `requireLoopback` enforces the boundary.
- Views cover overview, workload actions/details, Compose dependency graphs, mounts, tunnels and
  forwards, file editing/apply, streaming logs, stats plumbing, and interactive exec.
- Static assets are built from `web/` into `pkg/webui/dist` and embedded with `go:embed`.
- A Go-only build embeds `.gitkeep` and serves a 503 build hint; release artifacts and E2E images
  build the real SPA before compiling Go.
- CodeMirror 6 was chosen for mobile-friendly editing and smaller worker-free integration. Dagre
  lays out SVG dependency graphs; xterm.js renders exec sessions.

## Details

### BFF and protocols

`cmd/cornus/web.go` exposes config, projects, graph, workloads, mounts, tunnels, forwards, and an
exact-path allow-listed file API. Workload rows join live server state to Compose services in
dependency order, including spec-only services that have not been created. Mount status is derived:
`live` means the agent owns a session, `running` means backend-realized on a running workload, and
`inactive` means down or absent; volumes never report `live`.

Apply re-executes the current binary as `cornus compose ... up -d`. This reuses the canonical
reconcile, mount, conduit, and agent handoff behavior instead of duplicating unexported Compose CLI
logic.

Logs, stats, and exec use `github.com/coder/websocket`. Exec binary frames contain raw bytes in both
directions; text frames carry JSON resize controls such as `{"resize":{"h":24,"w":80}}`.
Initial dimensions are query parameters. Logs and exec must use binary frames because arbitrary
container output is not guaranteed to be valid UTF-8 at WebSocket frame boundaries. The default
same-origin check is retained.

### MCP surface

(2026-07-19) The BFF operation logic lives in value-returning, context-taking methods on
`webbff.Server` in `core.go` (`Workloads`, `WorkloadDetail`, `Graph`, `Apply`, `FileRead`,
`FileWrite`, `LogsTail`, `ExecRun`, ...). The `/.cornus/web/*` HTTP handlers (`handlers.go`) and the
MCP tools (`mcp.go`) are both thin adapters over these, so no operation logic is duplicated. Core
methods signal HTTP-shaped failures with a `statusError{code, err}`; the HTTP adapters map it with
`writeErr` (defaulting to 502), the MCP adapters surface the message as a tool error.

MCP uses the official `github.com/modelcontextprotocol/go-sdk/mcp` SDK. `Server.MCPServer()` registers
17 tools (`workloads_list`, `workload_get`, `workload_action`, `workload_delete`, `volume_delete`,
`tunnel_start`, `tunnel_stop`, `tunnels_list`, `projects_list`, `project_graph`, `project_apply`,
`mounts_list`, `files_list`, `file_read`, `file_write`, `logs_tail`, `exec_run`). Streaming stays
web-only: interactive exec/terminals and live logs/stats WebSockets do not fit MCP's
request/response model, so MCP gets a bounded `logs_tail` (non-streaming) and a one-shot `exec_run`
(no TTY, output demuxed with `stdcopy` and bounded to `maxToolCapture`). `file_write` goes through the
same `resolveEditable` allow-list as the HTTP editor.

Transports: `Server.MCPHandler()` returns a Streamable-HTTP `http.Handler` mounted at `/.cornus/mcp`
on the same mux, inside the same `guardHost` wrap (DNS-rebinding allow-list) as the web routes — so
the SDK's own localhost-only rebinding guard is disabled (`DisableLocalhostProtection`) to avoid it
rejecting the legitimate published-conduit Host. `Server.MCPRun(ctx)` serves the identical
`mcp.Server` over stdio for `cornus web --mcp-stdio` (`WebCmd.runStdio`, which binds no HTTP listener); a
peer closing stdin surfaces as the SDK's
internal "server is closing" error and is treated as a clean exit. With `--publish-in-conduit` the MCP
endpoint is published in the same SOCKS5 conduit as the UI (`clientagent.WebSpec.MCP` carries the
flag), exposing `file_write`/`exec_run` to conduit users exactly as the existing web surface already is.

### Development and mock modes

Vite development normally proxies `/.cornus` to a running `cornus web`. Alternatively,
`cornus web --frontend <url>` reverse-proxies non-BFF requests to a detached frontend server while
more-specific BFF routes remain local; WebSocket upgrade forwarding preserves HMR. A third loop,
`npm run dev:mock`, uses the standalone mock BFF without Go.

The mock implements exec and log WebSockets without an added npm dependency. `web/mock/ws.mjs`
handles RFC 6455 handshake, masking, split frames, extended lengths, fragmentation, ping/pong, and
close. `faketerm.mjs` loops a scripted shell demo until a keystroke transfers control to an
interactive fake. Pending sleeps must be resolved during takeover/close or cleared timers retain a
suspended async loop.

### Design system and brand

`web/src/styles.css` is layered from tokens through base typography, controls, components, and
responsive rules. Purple accent tokens derive from `assets/cornus-logo.svg` (`#4b1dc7` in light
mode and `#a78bfa` in dark mode); form controls share
height, spacing, border, focus-ring, and theme behavior. The SPA uses the canonical logo, a Cornus
wordmark, favicon, and theme color. `web/public/cornus-logo.svg` is copied into the Vite output.

### Shipping and embedding

`make web` runs the frontend build when npm is available; `make build-fast` intentionally skips it.
The root and E2E Dockerfiles have a Node 22 `webui` stage and copy its output into
`pkg/webui/dist` before Go compilation. Multi-arch images build architecture-independent frontend
assets once on `$BUILDPLATFORM`.

The release workflow likewise builds `webui-dist` once, uploads it, and downloads it into each
static-binary matrix job before cross-compilation. Both published images and downloadable binaries
therefore contain the real UI. Node modules are excluded from Docker build contexts.

## Files

- `cmd/cornus/web.go` - the `cornus web` command shell: loopback-guarded local serve, and (2026-07-18)
  the `--publish-in-conduit` path that hosts the BFF inside the background agent instead.
- `cmd/cornus/internal/webbff/` - (2026-07-18) the BFF and terminal manager, lifted out of `package main`
  so both the CLI and the agent can host it. `webbff.New(cfg, client, endpoint, resolver, AgentView) ->
  Server`; `Handler()` builds the SPA + `/.cornus/web/*` mux (and, when `Config.MCP` is set, `/.cornus/mcp`)
  behind a Host allow-list (DNS-rebinding guard); `Close()` reaps the persistent terminals (a real leak
  inside the long-lived agent). The `AgentView` interface breaks the import cycle: the CLI wires a
  socket-backed view, the agent its own live state. See [[client-daemon-and-conduit]] for how the agent
  publishes it in the shared conduit.
- `cmd/cornus/internal/webbff/core.go`, `mcp.go` - (2026-07-19) the shared operation core and the MCP
  tool adapters over it; `cornus web --mcp-stdio` (`WebCmd.runStdio` in `cmd/cornus/web.go`) serves the
  same MCP server over stdin/stdout.
- `pkg/webui/` - embedded SPA handler and `dist` boundary.
- `web/src/`, `web/public/`, `web/mock/` - Solid views, design system, assets, fixtures, and mock BFF.
- `pkg/e2e/frontend_stub.go`, `pkg/e2e/harness.go`, `e2e/scenarios/web.star` - harness coverage.
- `Dockerfile`, `e2e/container/Dockerfile`, `.github/workflows/release.yml`, `Makefile` - build and
  release embedding.

## Test Coverage

- `cmd/cornus/internal/webbff/webbff_test.go` uses a fake server API and a fake `AgentView` to cover
  joins, graph edges, derived mount status, file allow-listing, tunnels/forwards, and the Host
  allow-list; `cmd/cornus/web_test.go` keeps loopback enforcement, and `web_publish_test.go` covers the
  `--publish-in-conduit` request building (socks5 forcing, name derivation, absolute paths, flag
  conflicts). The full proxy -> published-name -> BFF path is exercised in
  `cmd/cornus/internal/clientagent/web_test.go` (`TestAgentWebServeEndToEnd`).
- `cmd/cornus/internal/webbff/mcp_test.go` drives the MCP server over the SDK's in-memory transport:
  `tools/list` exposes the full surface, tools return the same joins as their sibling HTTP handlers,
  `file_write`/`file_read` honor the allow-list, `guardHost` guards `/.cornus/mcp`, and `--no-mcp`
  removes the route.
- Vitest, jsdom, and Solid Testing Library render real views from shared fixtures. `npm test` is the
  frontend unit gate; `npm run build` is the type/build gate.
- `web.star` exercises the real BFF, embedded SPA, graph/mount data, and detached frontend path, plus
  the co-hosted MCP endpoint (Streamable-HTTP `initialize`, and asserts `--no-mcp` serves nothing). The
  containerized docker and kube runners embed the SPA; node-less local binaries may return the
  documented 503.
- The mock WebSocket endpoints were validated over real loopback TCP with masked RFC 6455 frames,
  scripted/interactive exec, one-shot commands, and log backlog plus streaming.

## Pitfalls

- Never expose the no-auth UI on a non-loopback address.
- Keep raw logs and terminal data in binary WebSocket frames; text frames enforce UTF-8.
- Do not weaken coder/websocket's same-origin default for this UI.
- Vite `emptyOutDir` removes `pkg/webui/dist/.gitkeep`; restore it after building. Built assets are
  ignored and should not be committed.
- `make web` is intentionally node-optional for Go-only development, but release and integrated E2E
  builds must stage frontend assets before `go build`.
- Node/npm are mise-managed on this development host; add the mise shims to non-interactive PATH.
