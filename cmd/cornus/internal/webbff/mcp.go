package webbff

// The /.cornus/mcp surface: an MCP (Model Context Protocol) server whose tools are
// thin adapters over the same operation core (core.go) the /.cornus/web/* HTTP
// handlers use, so the web UI and MCP clients can never drift. The Streamable HTTP
// transport is mounted on the same mux as the web routes and inherits guardHost
// (webbff.go); the stdio transport (MCPRun) reuses the identical server for
// launch-a-command MCP clients (Zed context servers, Claude Desktop).
//
// Streaming stays web-only: interactive exec/terminals and live logs/stats
// WebSockets do not fit MCP's request/response model. MCP gets a bounded
// logs_tail and a one-shot exec_run instead.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cornus/pkg/api"
)

// MCPServer builds the MCP server exposing the BFF's request/response operations
// as tools. The same *mcp.Server backs both the HTTP (MCPHandler) and stdio
// (MCPRun) transports.
func (s *Server) MCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "cornus",
		Title:   "cornus",
		Version: s.cfg.Version,
	}, nil)
	s.registerMCPTools(srv)
	return srv
}

// MCPHandler builds the Streamable HTTP handler for the MCP server, mountable on
// the BFF mux. DNS-rebinding protection is delegated to guardHost (the BFF's
// canonical, allow-list Host guard that wraps the whole mux), so the SDK's own
// localhost-only variant — which would reject the legitimate published-conduit
// Host — is disabled here.
func (s *Server) MCPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.MCPServer() },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
}

// MCPRun serves the MCP server over stdio until ctx is cancelled or the transport
// closes. It is the entry point for the `cornus mcp` subcommand. A peer that
// closes stdin (the normal way an MCP client shuts a stdio server down) surfaces
// as an EOF/connection-closed error from the SDK; that is a clean exit, not a
// failure, so it is swallowed here.
func (s *Server) MCPRun(ctx context.Context) error {
	err := s.MCPServer().Run(ctx, &mcp.StdioTransport{})
	if err == nil {
		return nil
	}
	// A peer closing stdin surfaces from the SDK as its internal jsonrpc2
	// "server is closing" error (cause io.EOF), which is not an importable
	// sentinel and does not unwrap to io.EOF — so match it by message alongside
	// the exported sentinels. All of these mean a clean shutdown.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, mcp.ErrConnectionClosed) || errors.Is(err, context.Canceled) ||
		strings.Contains(err.Error(), "server is closing") {
		return nil
	}
	return err
}

// jsonResult renders v as pretty-printed JSON text content. Tools return their
// results this way (with an `any` output type, so the SDK infers no output schema)
// to stay robust against JSON-schema inference over the deep api.* result types.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
}

// ---- tool input schemas ----

type mcpNoInput struct{}

type mcpName struct {
	Name string `json:"name" jsonschema:"the workload (deployment resource) name, e.g. myproj-web"`
}

type mcpAction struct {
	Name   string `json:"name" jsonschema:"the workload (deployment resource) name"`
	Action string `json:"action" jsonschema:"one of start, stop, or restart"`
}

type mcpTunnelStart struct {
	Name         string `json:"name" jsonschema:"the workload (deployment resource) name"`
	Port         int    `json:"port" jsonschema:"the container port to expose"`
	Proto        string `json:"proto,omitempty" jsonschema:"http (default) or tcp"`
	AuthToken    string `json:"authToken,omitempty" jsonschema:"tunnel provider auth token, if required"`
	ForwardAgent bool   `json:"forwardAgent,omitempty"`
}

type mcpProject struct {
	Project string `json:"project" jsonschema:"the compose project name"`
}

type mcpPath struct {
	Path string `json:"path" jsonschema:"absolute path of an editable file (from files_list)"`
}

type mcpFileWrite struct {
	Path    string `json:"path" jsonschema:"absolute path of an editable file (from files_list)"`
	Content string `json:"content" jsonschema:"the full new file contents"`
}

type mcpLogsTail struct {
	Name string `json:"name" jsonschema:"the workload (deployment resource) name"`
	Tail int    `json:"tail,omitempty" jsonschema:"number of trailing log lines to return (default 200)"`
}

type mcpExecRun struct {
	Name string   `json:"name" jsonschema:"the workload (deployment resource) name"`
	Cmd  []string `json:"cmd" jsonschema:"the command and arguments to run, e.g. [\"ls\", \"-la\"]"`
}

// registerMCPTools wires every tool as a thin adapter over a core method.
func (s *Server) registerMCPTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "workloads_list",
		Description: "List all workloads: the loaded compose project's services (in dependency order, including not-yet-created ones) joined with every deployment the server reports.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
		out, err := s.Workloads(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"workloads": out})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "workload_get",
		Description: "Get one workload's spec, status, and tunnel state by name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpName) (*mcp.CallToolResult, any, error) {
		detail, err := s.WorkloadDetail(ctx, in.Name)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(detail)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "workload_action",
		Description: "Start, stop, or restart a workload.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAction) (*mcp.CallToolResult, any, error) {
		if err := s.WorkloadAction(ctx, in.Name, in.Action); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"result": "ok"})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "workload_delete",
		Description: "Delete a workload (deployment) by name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpName) (*mcp.CallToolResult, any, error) {
		if err := s.WorkloadDelete(ctx, in.Name); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"result": "ok"})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "volume_delete",
		Description: "Delete a named volume.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpName) (*mcp.CallToolResult, any, error) {
		if err := s.VolumeDelete(ctx, in.Name); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"result": "ok"})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tunnel_start",
		Description: "Open a hosted tunnel exposing a workload port to the public internet.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTunnelStart) (*mcp.CallToolResult, any, error) {
		st, err := s.TunnelStart(ctx, in.Name, api.TunnelRequest{
			Port: in.Port, Proto: in.Proto, AuthToken: in.AuthToken, ForwardAgent: in.ForwardAgent,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(st)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tunnel_stop",
		Description: "Tear down a workload's tunnel.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpName) (*mcp.CallToolResult, any, error) {
		if err := s.TunnelStop(ctx, in.Name); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"result": "ok"})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tunnels_list",
		Description: "List every workload tunnel, plus the client agent's live local port-forwards and conduit banners.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
		resp, err := s.Tunnels(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(resp)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "projects_list",
		Description: "List the loaded compose project and any project the client agent has live sessions for.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
		return jsonResult(map[string]any{"projects": s.Projects(ctx)})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "project_graph",
		Description: "Get the service dependency graph (nodes and depends_on edges) of the loaded compose project.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpProject) (*mcp.CallToolResult, any, error) {
		g, err := s.Graph(ctx, in.Project)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(g)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "project_apply",
		Description: "Re-deploy the loaded compose project (equivalent to `cornus compose up -d`). Returns the captured apply output.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpProject) (*mcp.CallToolResult, any, error) {
		var out capBuffer
		out.cap = maxToolCapture
		res := map[string]string{}
		if err := s.Apply(ctx, in.Project, &out); err != nil {
			// A 404 (unknown project) is a real tool error; a failed re-exec is
			// reported alongside the captured output so the model can see it.
			if _, is404 := err.(*statusError); is404 {
				return nil, nil, err
			}
			res["error"] = err.Error()
		}
		res["output"] = out.String()
		return jsonResult(res)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "mounts_list",
		Description: "List every mount (bind and volume) of the loaded project's services, with a derived live/running/inactive status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
		return jsonResult(map[string]any{"mounts": s.Mounts(ctx)})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "files_list",
		Description: "List the editable files (compose file(s), env file(s), client config). Only these exact paths may be read or written.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
		return jsonResult(map[string]any{"files": s.Files()})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "file_read",
		Description: "Read an editable file's contents. The path must be one returned by files_list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpPath) (*mcp.CallToolResult, any, error) {
		data, err := s.FileRead(in.Path)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"path": in.Path, "content": string(data)})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "file_write",
		Description: "Overwrite an editable file's contents. The path must be one returned by files_list; any other path is rejected by the allow-list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpFileWrite) (*mcp.CallToolResult, any, error) {
		if err := s.FileWrite(in.Path, []byte(in.Content)); err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"result": "ok"})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "logs_tail",
		Description: "Return the last N lines of a workload's logs (non-streaming; the live log stream stays web-only). Output is bounded.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpLogsTail) (*mcp.CallToolResult, any, error) {
		logs, err := s.LogsTail(ctx, in.Name, in.Tail)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]string{"logs": logs})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "exec_run",
		Description: "Run a single command inside a workload and return its captured stdout, stderr, and exit code (one-shot, non-interactive; the interactive terminal stays web-only). Output is bounded.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpExecRun) (*mcp.CallToolResult, any, error) {
		res, err := s.ExecRun(ctx, in.Name, in.Cmd)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(res)
	})
}
