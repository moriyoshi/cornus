# Web BFF and MCP Surface

## Summary

`cornus web` runs a client-side backend-for-frontend (BFF) and exposes the same
operations to browsers and agents. Streamable HTTP at `/.cornus/mcp` and
`cornus web --mcp-stdio` share one MCP server and one value-returning core rather
than duplicating workload, project, file, log, exec, and observability logic.

## Key Facts

- `cmd/cornus/internal/webbff/core.go` is the operation seam; HTTP handlers and
  MCP tools are adapters.
- HTTP MCP is enabled by default and protected by the BFF's `guardHost`;
  `--no-mcp` disables it.
- `--mcp-stdio` binds no HTTP listener and keeps stdout pure JSON-RPC.
- MCP errors are bounded and structured; tool results do not expose Go values or
  raw handler state.
- The process-level E2E uses the official SDK `mcp.CommandTransport`, proving
  initialization, multiple requests in one child, stderr separation, and clean
  exit when stdin closes.
- The file explorer (`/.cornus/web/fs*`) browses a VIRTUAL namespace unifying
  confined local roots and every workload's container filesystem. A path there
  does not say what backs it, so `fsplan.go` resolves a `site` (client / server /
  container) and `planTransfer` — a PURE function, hence table-testable with no
  daemon — picks `execHere`, `execServer`, or `execRelay`.
- Cornus bind mounts are CLIENT-LOCAL, so a container path under one is served
  from the developer's own disk. Redirecting to `execHere` is the largest win in
  the explorer and needs no daemon at all; it is gated on `api.Origin` matching
  this host and directory, since `DeployStatus` carries no mount table.
- There is deliberately NO in-container exec route for FS semantics. `execServer`
  is `deploy.FSOperator` (`api.FSOpRequest`), served on kubernetes by the pod's
  caretaker over wire tag `S`. Refusals carry machine-readable codes so the caller
  can distinguish "tell the user" from "relay this instead".
- The BFF LEARNS whether a backend has an archive at all, from a bodyless
  `StatPath`, and routes later transfers accordingly. Deciding after the fact does
  not work: a PUT streams its tar from a pipe, so the transport has already
  consumed bytes by the time a 501 arrives and the stream cannot be replayed.
- The BFF has NO authentication (`guardHost` is a DNS-rebinding defence). A
  bind-mount source is therefore refused as a browsable root when it is a
  pseudo-filesystem — judged by `unix.Statfs` magic, never a path denylist — or a
  filesystem root, and `:ro` is honoured.

## Details

The initial implementation extracted the behavior of the existing
`/.cornus/web/*` handlers into context-taking methods. The final CLI shape folded
the interim `cornus mcp` command into `cornus web --mcp-stdio`; references to the
interim command are historical only.

The stdio harness exposes opaque session handles through Starlark builtins rather
than recreating JSON-RPC framing. This makes the test sensitive to the real
launch-a-command contract, which in-memory SDK tests and Streamable HTTP tests
cannot cover.

## Files

- `cmd/cornus/internal/webbff/core.go` - shared operations.
- `cmd/cornus/internal/webbff/mcp.go` - MCP tools and resources.
- `cmd/cornus/web.go` - HTTP and stdio mode selection.
- `pkg/e2e/mcp.go` - launched-client E2E support.
- `cmd/cornus/internal/webbff/fsplan.go` - sites, routes, and the fsop probe.
- `cmd/cornus/internal/webbff/fs.go` - the Fs* operations and the archive/fsop seam.
- `pkg/deploy/deploy.go` - the `FSOperator` capability contract.
- `pkg/caretaker/fsop.go`, `pkg/wire/fsop.go` - the operator and its framing.
- `e2e/scenarios/web-fs.star` - both arms (relay and kube redirect + operator).

## Test Coverage

Unit tests cover the shared core and MCP adapters. `e2e/scenarios/web.star`
covers Streamable HTTP, while the MCP stdio scenario launches a real child via
`mcp.CommandTransport`.

## Pitfalls

- Never write diagnostics to stdout in stdio mode.
- Update HTTP, MCP, and browser adapters together when the shared core changes.
- An in-memory MCP test does not prove process lifetime or stdio purity.

## Filesystem Operations

Copy and move are planned server-side before mutation. The planner normalizes paths, rejects self or descendant moves and collisions, and provides preflight for single and batch routes. `FsMove` replaces browser-side copy then delete, and transfer routes stream instead of imposing the former 10 MB whole-file buffer.

The caretaker filesystem operator carries operations to remote workloads. BFF core, HTTP routes, SPA, and caretaker protocol must evolve together; planner unit tests alone do not prove routing or remote execution. Docker and Kubernetes E2E arms exercise distinct realization paths.

Shell discovery inspects the workload rather than assuming `/bin/sh`. Kubernetes image-layer stat, get, and put use tar streams over `pods/exec`, retaining server-side archive and path confinement.

## Non-Loopback Web Binding

`cornus web --addr` may bind off-host. Host-header validation derives permitted authority from the configured listener rather than assuming loopback, without weakening origin or filesystem confinement.
