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
// logs_tail and a one-shot exec_run instead. The flight record is the same story
// — activity_read is the one-shot form, and `cornus activity --follow` keeps the
// live one, which is no loss here: a recorder is read after the fact, and an
// agent asking "what went wrong" wants the history, not a subscription.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cornus/pkg/api"
	"cornus/pkg/client"
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
	s.registerMCPObserveTools(srv)
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

type mcpActivity struct {
	Since      string `json:"since,omitempty" jsonschema:"only records at or after this time: an RFC3339 instant, or a duration back from now like 2h"`
	Kind       string `json:"kind,omitempty" jsonschema:"only this kind: server, caretaker, service, 9p-mount, build, or deploy"`
	Unfinished bool   `json:"unfinished,omitempty" jsonschema:"only activities that began and never finished: a process that did not shut down cleanly, or an effect nobody owns"`
}

type mcpObserveLogs struct {
	Service  string `json:"service,omitempty" jsonschema:"only this workload (deployment name); omit to search every workload"`
	Match    string `json:"match,omitempty" jsonschema:"only records whose message contains this text (full-text search)"`
	Severity string `json:"severity,omitempty" jsonschema:"only records at or above this level: debug, info, warn, error, or fatal"`
	Trace    string `json:"trace,omitempty" jsonschema:"only records belonging to this trace id, i.e. the logs of one request"`
	Since    string `json:"since,omitempty" jsonschema:"only records at or after this time: an RFC3339 instant, or a duration back from now like 2h"`
	Until    string `json:"until,omitempty" jsonschema:"only records before this time"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum records to return (capped server-side)"`
}

type mcpObserveTraces struct {
	Service     string `json:"service,omitempty" jsonschema:"only traces with a span from this workload"`
	Name        string `json:"name,omitempty" jsonschema:"only traces with a span of this name, e.g. GET /checkout"`
	Status      string `json:"status,omitempty" jsonschema:"only traces with this span status, e.g. error"`
	MinDuration string `json:"minDuration,omitempty" jsonschema:"only traces at least this long, as a duration like 500ms or 2s: how you find the slow ones"`
	Since       string `json:"since,omitempty" jsonschema:"only traces starting at or after this time"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum traces to return (capped server-side)"`
}

type mcpObserveTrace struct {
	TraceID string `json:"traceId" jsonschema:"the trace id, as returned by observe_traces"`
}

type mcpObserveMetrics struct {
	Query string `json:"query" jsonschema:"a PromQL range query, e.g. rate(http_requests_total[5m]). OpenTelemetry dots map to Prometheus underscores"`
	Since string `json:"since,omitempty" jsonschema:"start of the range (default 1h back)"`
	Until string `json:"until,omitempty" jsonschema:"end of the range (default now)"`
	Step  string `json:"step,omitempty" jsonschema:"resolution as a duration like 1m"`
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

	mcp.AddTool(srv, &mcp.Tool{
		Name: "activity_read",
		Description: "Read the server's flight records: what the server and its caretakers were doing, and what they did not finish. " +
			"Unlike workloads_list, which reports what is true right now, these records are written to disk as work happens and survive the process, the container, and the incident — so this is what answers \"what went wrong\" after the fact. " +
			"Work is recorded as a begin/end pair, so an activity with a begin and no end did not finish: an open server or caretaker lifetime is a process that did not shut down cleanly (SIGKILL, OOM, a panic, a host reboot), and an open 9p-mount is a mountpoint that may still exist with nobody owning it. " +
			"Records carry the writing process's instance id; compare it with liveInstance in the result to tell a lifetime that is open because the process is still RUNNING from one open because it died.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpActivity) (*mcp.CallToolResult, any, error) {
		out, err := s.Activity(ctx, in.Since, in.Kind, in.Unfinished)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})

	s.registerMCPResources(srv)
}

// registerMCPObserveTools wires the observability store's read surface.
//
// Every description says what the tool answers rather than what it queries, and
// says it in the vocabulary of the problem ("what did it print before it died",
// "find the slow ones"). A model picks tools by matching the user's situation
// against these sentences; a tool described as "queries the logs table" is a tool
// it reaches for only if it already knew that was the answer.
//
// Streaming stays web-only per the rule at the top of this file: these are all
// bounded request/response reads.
func (s *Server) registerMCPObserveTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "observe_logs",
		Description: "Search a workload's RECORDED logs, which survive the container that produced them. " +
			"Use this to answer what a service printed before it crashed, restarted, or was deleted \u2014 questions the live log tail cannot answer at all. " +
			"Supports full-text search, a minimum severity, and correlation to one trace id. " +
			"Requires a cornus server with the observability store enabled; if it is not, the tool says so rather than returning an empty result.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpObserveLogs) (*mcp.CallToolResult, any, error) {
		out, err := s.ObserveLogs(ctx, client.ObsLogQuery{
			Service:  in.Service,
			Match:    in.Match,
			Severity: in.Severity,
			TraceID:  in.Trace,
			Since:    in.Since,
			Until:    in.Until,
			Limit:    in.Limit,
			// Newest-first so a limit keeps the most RECENT records; taking the
			// oldest N would answer a different question.
			Newest: true,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"entries": out})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "observe_traces",
		Description: "Search recorded distributed traces across workloads. " +
			"Use this to find WHICH requests were slow or failed before looking at why: filter by minimum duration to find the slow ones, or by status=error to find the broken ones. " +
			"Returns trace summaries; pass a returned traceId to observe_trace for the span breakdown, or to observe_logs to read that one request's log lines.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpObserveTraces) (*mcp.CallToolResult, any, error) {
		var minDur time.Duration
		if in.MinDuration != "" {
			d, err := time.ParseDuration(in.MinDuration)
			if err != nil {
				return nil, nil, fmt.Errorf("minDuration: %w", err)
			}
			minDur = d
		}
		out, err := s.ObserveTraces(ctx, client.ObsTraceQuery{
			Service:     in.Service,
			Name:        in.Name,
			Status:      in.Status,
			MinDuration: minDur,
			Since:       in.Since,
			Limit:       in.Limit,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"traces": out})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "observe_trace",
		Description: "Get one trace's spans, already assembled into a parent/child tree so the causality is explicit. " +
			"Use it after observe_traces to see where a slow request actually spent its time, and which service failed first. " +
			"A span whose parent was never recorded still appears, as a root: a partially collected trace is exactly when this is being read.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpObserveTrace) (*mcp.CallToolResult, any, error) {
		out, err := s.ObserveTrace(ctx, in.TraceID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"roots": out})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "observe_metrics",
		Description: "Evaluate a PromQL range query over the metrics workloads have exported. " +
			"Use it to see a rate, a utilization, or a latency quantile over time rather than at one instant. " +
			"Only metrics an instrumented workload actually exported are present; constructs outside the supported PromQL profile are rejected with a diagnostic instead of being approximated.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpObserveMetrics) (*mcp.CallToolResult, any, error) {
		var step time.Duration
		if in.Step != "" {
			d, err := time.ParseDuration(in.Step)
			if err != nil {
				return nil, nil, fmt.Errorf("step: %w", err)
			}
			step = d
		}
		out, err := s.ObserveMetrics(ctx, in.Query, in.Since, in.Until, step)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{"series": out})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "observe_status",
		Description: "Report what the observability store is holding: row counts per signal, how far back it reaches, and how many records it DROPPED under load. " +
			"Check this before concluding from an empty search that nothing happened \u2014 a non-zero dropped count means the evidence may have been shed rather than never existing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
		out, err := s.ObserveStatus(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(out)
	})
}

// activityUnfinishedURI is the resource form of "what is currently unresolved".
//
// It is a resource rather than only a tool because it is CONTEXT, not an action:
// a client can attach it to a conversation the way it attaches a file, so an
// agent asked why a deploy is behaving strangely starts out already knowing that
// the last server died mid-flight — instead of having to suspect that first and
// then go looking.
const activityUnfinishedURI = "cornus://activity/unfinished"

func (s *Server) registerMCPResources(srv *mcp.Server) {
	srv.AddResource(&mcp.Resource{
		URI:      activityUnfinishedURI,
		Name:     "unfinished-activities",
		Title:    "Unfinished activities (flight record)",
		MIMEType: "application/json",
		Description: "Everything the cornus server and its caretakers began and never finished — the shortest description of what is currently wrong. " +
			"An open server/caretaker lifetime means that process did not shut down cleanly; an open 9p-mount means a mountpoint may still exist with nobody owning it. " +
			"An empty events list means nothing is outstanding. Records whose instance equals liveInstance belong to the process serving this request, so their open lifetime means \"still running\", not \"died\".",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		out, err := s.Activity(ctx, "", "", true)
		if err != nil {
			return nil, err
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: req.Params.URI, MIMEType: "application/json", Text: string(data),
		}}}, nil
	})

	srv.AddResource(&mcp.Resource{
		URI:      obsErrorsURI,
		Name:     "workload-errors",
		Title:    "Recent workload errors (observability store)",
		MIMEType: "application/json",
		Description: "Error-level records the DEPLOYED WORKLOADS emitted in the last hour, newest first. " +
			"Complements the unfinished-activities resource: that one says what cornus itself failed to finish, this one says what the user's own code reported. " +
			"An empty entries list means no workload logged an error in that window. " +
			"Absent entirely when the server has no observability store.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		data, err := s.observeErrorsJSON(ctx)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: req.Params.URI, MIMEType: "application/json", Text: string(data),
		}}}, nil
	})
}
