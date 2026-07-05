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

## Test Coverage

Unit tests cover the shared core and MCP adapters. `e2e/scenarios/web.star`
covers Streamable HTTP, while the MCP stdio scenario launches a real child via
`mcp.CommandTransport`.

## Pitfalls

- Never write diagnostics to stdout in stdio mode.
- Update HTTP, MCP, and browser adapters together when the shared core changes.
- An in-memory MCP test does not prove process lifetime or stdio purity.
