package webbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cornus/pkg/api"
)

// connectMCP wires an in-memory MCP client to the BFF's MCP server, returning a
// live client session. It mirrors the fake-server harness used by the HTTP tests.
func connectMCP(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.MCPServer().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes a tool and decodes its text-content JSON payload into out. It
// fails the test if the tool reports an error.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s reported error: %s", name, toolText(res))
	}
	if out != nil {
		if err := json.Unmarshal([]byte(toolText(res)), out); err != nil {
			t.Fatalf("CallTool %s: decoding %q: %v", name, toolText(res), err)
		}
	}
	return res
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestMCPToolsMatchHTTP asserts the MCP tools return the same joins as their
// sibling HTTP handlers over shared fixtures.
func TestMCPToolsMatchHTTP(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-db", Image: "example/db:1@sha256:abc", Backend: "dockerhost",
			Instances: []api.InstanceStatus{{ID: "c1", State: "running", Running: true}}},
		{Name: "other", Image: "example/other:1",
			Instances: []api.InstanceStatus{{ID: "c2", State: "exited", Running: false}}},
	}, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	// tools/list exposes the whole surface.
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range lt.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"workloads_list", "workload_get", "workload_action", "workload_delete",
		"volume_delete", "tunnel_start", "tunnel_stop", "tunnels_list",
		"projects_list", "project_graph", "project_apply", "mounts_list",
		"files_list", "file_read", "file_write", "logs_tail", "exec_run",
	} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}

	// workloads_list matches the HTTP join exactly.
	var httpRows []webWorkload
	doJSON(t, s, "GET", "/.cornus/web/workloads", &httpRows)
	var mcpOut struct {
		Workloads []webWorkload `json:"workloads"`
	}
	callTool(t, cs, "workloads_list", nil, &mcpOut)
	if len(mcpOut.Workloads) != len(httpRows) {
		t.Fatalf("workloads_list: got %d rows, http had %d", len(mcpOut.Workloads), len(httpRows))
	}
	for i := range httpRows {
		if mcpOut.Workloads[i].Name != httpRows[i].Name || mcpOut.Workloads[i].Summary != httpRows[i].Summary {
			t.Errorf("row %d: mcp %+v != http %+v", i, mcpOut.Workloads[i], httpRows[i])
		}
	}

	// project_graph matches the HTTP graph.
	var g webGraph
	callTool(t, cs, "project_graph", map[string]any{"project": "proj"}, &g)
	if len(g.Nodes) != 2 || len(g.Edges) != 1 || g.Edges[0].From != "web" || g.Edges[0].To != "db" {
		t.Errorf("project_graph: %+v", g)
	}

	// An unknown project is a tool error, not a silent empty result.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "project_graph", Arguments: map[string]any{"project": "nope"},
	})
	if err != nil {
		t.Fatalf("CallTool project_graph nope: %v", err)
	}
	if !res.IsError {
		t.Errorf("project_graph for unknown project should be a tool error")
	}
}

// TestMCPFileWriteAllowList asserts file_write/file_read honor the same exact-path
// allow-list the HTTP editor endpoints do.
func TestMCPFileWriteAllowList(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	var files struct {
		Files []webFile `json:"files"`
	}
	callTool(t, cs, "files_list", nil, &files)
	var composeFile string
	for _, f := range files.Files {
		if f.Kind == "compose" {
			composeFile = f.Path
		}
	}
	if composeFile == "" {
		t.Fatalf("no compose file in editable set: %+v", files.Files)
	}

	// A write to an allow-listed path round-trips through a read.
	callTool(t, cs, "file_write", map[string]any{"path": composeFile, "content": "services: {}\n"}, nil)
	var content struct {
		Content string `json:"content"`
	}
	callTool(t, cs, "file_read", map[string]any{"path": composeFile}, &content)
	if content.Content != "services: {}\n" {
		t.Errorf("file_read after write: %q", content.Content)
	}

	// Anything outside the allow-list — including traversal — is a tool error.
	for _, bad := range []string{"/etc/passwd", composeFile + "/../../../etc/passwd"} {
		for _, tool := range []string{"file_read", "file_write"} {
			args := map[string]any{"path": bad}
			if tool == "file_write" {
				args["content"] = "x"
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil {
				t.Fatalf("%s %s: %v", tool, bad, err)
			}
			if !res.IsError {
				t.Errorf("%s %s: expected a tool error (outside allow-list)", tool, bad)
			}
		}
	}
}

// TestMCPGuardHost confirms the DNS-rebinding guard wraps /.cornus/mcp exactly as
// it wraps the web routes: loopback and the published name pass, a foreign Host is
// refused before reaching the MCP handler.
func TestMCPGuardHost(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	s.cfg.MCP = true
	s.cfg.PublishedName = "cornus.internal"
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for host, allowed := range map[string]bool{
		"127.0.0.1:41234":  true,
		"cornus.internal":  true,
		"evil.example.com": false,
	} {
		// A POST with no body still passes guardHost before the MCP handler rejects
		// it, so guardHost's verdict is visible as (not) 421 Misdirected Request.
		req := httptest.NewRequest("POST", "/.cornus/mcp", strings.NewReader("{}"))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if allowed && rec.Code == http.StatusMisdirectedRequest {
			t.Errorf("Host %q: guardHost rejected an allowed host (%d)", host, rec.Code)
		}
		if !allowed && rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: got %d, want 421 Misdirected Request", host, rec.Code)
		}
	}
}

// TestMCPDisabled asserts /.cornus/mcp is absent when MCP is off (default Config
// zero value), so opting out truly removes the surface.
func TestMCPDisabled(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	s.cfg.MCP = false
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/.cornus/mcp", strings.NewReader("{}"))
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// With no /.cornus/mcp route, the request falls through to the SPA handler at
	// "/", which never returns a JSON-RPC response — a 404/hint, not a 200 tool
	// reply. The key assertion: it is not served by the MCP handler.
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "jsonrpc") {
		t.Errorf("MCP disabled but /.cornus/mcp answered a JSON-RPC reply: %d %q", rec.Code, rec.Body.String())
	}
}
