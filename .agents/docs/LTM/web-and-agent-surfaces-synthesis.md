# Web and Agent Surfaces Synthesis

## Summary

`cornus web` is a client-side control surface composed of an embedded SolidJS
SPA and a loopback backend-for-frontend. Browser HTTP, Streamable HTTP MCP, and
stdio MCP adapt one value-returning operation core. The BFF combines remote
server status with local project definitions, so structured workload origin can
support attribution without letting names or backend metadata fabricate a
Compose project model.

## Included Documents

| Document | Focus |
|----------|-------|
| [web-ui.md](./web-ui.md) | Embedded SPA, BFF protocols, workspaces, design system, development, and shipping |
| [web-bff-and-mcp.md](./web-bff-and-mcp.md) | Shared operation core and HTTP/stdio MCP adapters |
| [workload-lineage.md](./workload-lineage.md) | Structured origin metadata and trust-bounded project attribution |

## Stable Knowledge

- The BFF runs client-side because it needs both a Cornus server connection and
  access to local project files. Loopback serving is protected by `guardHost`;
  it is not a remotely exposed replacement control plane.
- `cmd/cornus/internal/webbff/core.go` is the behavior seam. Browser handlers and
  MCP tools translate transport input into core calls and translate returned
  values into bounded responses.
- Streamable HTTP MCP at `/.cornus/mcp` is enabled by default unless `--no-mcp`
  is set. `cornus web --mcp-stdio` binds no listener, writes only JSON-RPC to
  stdout, and sends diagnostics to stderr.
- The SPA and BFF use explicit JSON and binary WebSocket protocols for workloads,
  graphs, files, terminals, logs, and observability. Development mock responses
  must preserve the same shape as the real BFF.
- `api.Origin` is structured metadata persisted through each backend's native
  label, annotation, or record mechanism. Project identity must never be
  inferred from deployment naming conventions.
- A project section can be built only from a Compose project loaded by the local
  BFF. A workload attributed to an unloaded project retains its origin name but
  remains in the Overview's `Other` group.
- Origin is not a stored deployment specification. Uniform mount reporting
  would require a `DeployStatus` extension and backend-specific observation;
  realized paths may already differ from the caller's original paths.
- Release images and downloadable binaries must embed the production SPA.
  Node-optional Go development may use the placeholder, but release and
  integrated E2E builds may not.

## Operational Guidance

- Add an operation to the shared core first, then expose it through the browser
  and MCP adapters that need it. Keep tool errors structured and bounded.
- Treat local project definitions as the authority for graphs, service
  relationships, and caller-facing mounts. Treat backend status as the authority
  for live workload state and structured origin.
- Preserve stdout purity and process lifetime in stdio mode. In-memory MCP tests
  cannot prove either property.
- Read `.agents/docs/DESIGN_SYSTEM.md` before changing SPA styling or adding a
  screen. Keep real and mock BFF protocols synchronized.

## Files

- `cmd/cornus/web.go` - web command, loopback server, and stdio mode.
- `cmd/cornus/internal/webbff/` - operation core, HTTP adapters, MCP adapters,
  project join, filesystem, terminal, and observability operations.
- `web/src/`, `web/public/`, and `web/mock/` - SPA, assets, and development
  fixtures.
- `pkg/api/` and `pkg/deploy/*` - origin model and backend persistence.
- `pkg/webui/` - embedded production assets and placeholder boundary.

## Tests

- BFF unit tests use fake server and agent seams for project, workload, file,
  terminal, observability, and lineage behavior.
- MCP unit tests cover the shared server through SDK transport.
- `web.star` exercises the embedded SPA, real BFF, Streamable HTTP MCP, project
  views, and protocol paths.
- The stdio E2E launches a real child through `mcp.CommandTransport`, sends
  multiple requests, verifies stderr separation, and closes it through stdin.

## Pitfalls

- Never write logs or banners to stdout in `--mcp-stdio` mode.
- Do not infer that an origin-attributed project is locally loaded.
- Backend mount inspection cannot reliably reconstruct caller paths.
- A mock-only or in-memory test does not prove shipped SPA embedding, WebSocket
  framing, or child-process behavior.
