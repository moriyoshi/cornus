package webbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cornus/pkg/obsstore"
)

// fakeObsUpstream serves the store's read endpoints as the real server does, and
// records the queries it was asked so a test can prove the filters travelled.
func fakeObsUpstream(t *testing.T, entries []obsstore.LogEntry, traces []obsstore.TraceSummary, spans []obsstore.Span, seen *url.Values) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		if seen != nil {
			*seen = r.URL.Query()
		}
	}
	mux.HandleFunc("GET /.cornus/v1/obs/logs", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(entries)
	})
	mux.HandleFunc("GET /.cornus/v1/obs/traces", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(traces)
	})
	mux.HandleFunc("GET /.cornus/v1/obs/trace/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(spans)
	})
	mux.HandleFunc("GET /.cornus/v1/obs/metrics", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode([]obsstore.Series{{
			Labels: map[string]string{"service": "web"},
			Points: []obsstore.Point{{T: time.Unix(1, 0).UTC(), V: 1.5}},
		}})
	})
	mux.HandleFunc("GET /.cornus/v1/obs/status", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(obsstore.Status{
			Dir:     "/data/observability",
			Dropped: 7,
			Tables:  []obsstore.TableStatus{{Name: "logs", Rows: 42}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// noStoreUpstream is a server without the observability store: the routes are
// simply absent, which is what a build lacking the imbh tag looks like.
func noStoreUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv
}

func obsFixtureEntries() []obsstore.LogEntry {
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	return []obsstore.LogEntry{
		{Time: base, Service: "web", Body: "listening on :8080", SeverityText: "INFO"},
		{Time: base.Add(time.Minute), Service: "web", Body: "payment gateway timeout", SeverityText: "ERROR", TraceID: "abc123"},
	}
}

// TestMCPObserveToolsAreListed checks the tools exist AND that their descriptions
// explain what they answer.
//
// The description assertions are not pedantry. A model picks a tool by matching
// the user's situation against its description, so a tool whose text does not
// mention that these logs OUTLIVE the container is a tool it will never reach for
// when a container has just died — which is the only moment it matters.
func TestMCPObserveToolsAreListed(t *testing.T) {
	s := testServer(t, fakeObsUpstream(t, nil, nil, nil, nil), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}

	for _, want := range []string{"observe_logs", "observe_traces", "observe_trace", "observe_metrics", "observe_status"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("tool %q is not listed", want)
		}
	}

	// The property that makes observe_logs distinct from logs_tail.
	if d := byName["observe_logs"].Description; !strings.Contains(d, "survive") {
		t.Errorf("observe_logs description does not say the records outlive the container: %q", d)
	}
	// The property that makes observe_status worth calling at all.
	if d := byName["observe_status"].Description; !strings.Contains(d, "DROPPED") {
		t.Errorf("observe_status description does not warn about dropped records: %q", d)
	}
	// The tool chain has to be discoverable, or a model finds a slow trace and
	// then has no idea what to do with the id.
	if d := byName["observe_traces"].Description; !strings.Contains(d, "observe_trace") {
		t.Errorf("observe_traces description does not point at the follow-up tool: %q", d)
	}
}

// TestMCPObserveLogsPassesFiltersThrough proves the filters reach the upstream
// server rather than being accepted and quietly ignored.
func TestMCPObserveLogsPassesFiltersThrough(t *testing.T) {
	var seen url.Values
	s := testServer(t, fakeObsUpstream(t, obsFixtureEntries(), nil, nil, &seen), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	var out struct {
		Entries []obsstore.LogEntry `json:"entries"`
	}
	callTool(t, cs, "observe_logs", map[string]any{
		"service":  "web",
		"match":    "timeout",
		"severity": "error",
		"since":    "2h",
	}, &out)

	if len(out.Entries) != 2 {
		t.Fatalf("got %d entries, want the fixture's 2", len(out.Entries))
	}
	for k, want := range map[string]string{
		"service":  "web",
		"match":    "timeout",
		"severity": "error",
		"since":    "2h",
	} {
		if got := seen.Get(k); got != want {
			t.Errorf("upstream query %s = %q, want %q", k, got, want)
		}
	}
	// Newest-first is not optional: with a limit, oldest-first would answer a
	// different question and look almost right.
	if seen.Get("newest") == "" {
		t.Error("observe_logs did not ask for newest-first")
	}
}

// TestMCPObserveLogsBoundsTheResult keeps a chatty workload from flooding the
// context window of the very model that asked.
func TestMCPObserveLogsBoundsTheResult(t *testing.T) {
	var seen url.Values
	s := testServer(t, fakeObsUpstream(t, nil, nil, nil, &seen), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	for _, limit := range []any{nil, 0, 100000} {
		args := map[string]any{}
		if limit != nil {
			args["limit"] = limit
		}
		callTool(t, cs, "observe_logs", args, nil)
		got := seen.Get("limit")
		if limit == 100000 || limit == nil || limit == 0 {
			if got != "200" {
				t.Errorf("limit %v produced upstream limit %q, want the %d cap", limit, got, observeToolLimit)
			}
		}
	}
}

// TestMCPObserveTraceAssemblesTheTree is the one place the MCP surface
// deliberately differs from the raw HTTP API: a model reading a flat span list
// has to reconstruct causality from parentSpanId, which it does unreliably.
func TestMCPObserveTraceAssemblesTheTree(t *testing.T) {
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	spans := []obsstore.Span{
		{TraceID: "abc", SpanID: "child", ParentSpanID: "root", Name: "db.query", Start: base.Add(time.Second), Duration: time.Second},
		{TraceID: "abc", SpanID: "root", Name: "GET /checkout", Start: base, Duration: 3 * time.Second},
	}
	s := testServer(t, fakeObsUpstream(t, nil, nil, spans, nil), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	var out struct {
		Roots []*obsstore.Node `json:"roots"`
	}
	callTool(t, cs, "observe_trace", map[string]any{"traceId": "abc"}, &out)

	if len(out.Roots) != 1 {
		t.Fatalf("got %d roots, want 1: %+v", len(out.Roots), out.Roots)
	}
	if out.Roots[0].Name != "GET /checkout" {
		t.Errorf("root = %q", out.Roots[0].Name)
	}
	if len(out.Roots[0].Children) != 1 || out.Roots[0].Children[0].Name != "db.query" {
		t.Errorf("child span was not nested under its parent: %+v", out.Roots[0].Children)
	}
}

// TestMCPObserveTracesRejectsABadDuration: malformed input must be a tool error,
// not a silently dropped filter that returns everything.
func TestMCPObserveTracesRejectsABadDuration(t *testing.T) {
	s := testServer(t, fakeObsUpstream(t, nil, nil, nil, nil), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "observe_traces",
		Arguments: map[string]any{"minDuration": "soon"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a malformed minDuration was accepted; it must be a tool error")
	}
	if !strings.Contains(toolText(res), "minDuration") {
		t.Errorf("error does not name the offending field: %s", toolText(res))
	}
}

// TestMCPObserveStatusSurfacesDropped: the dropped counter is the reason this
// tool exists, so it has to survive the round trip.
func TestMCPObserveStatusSurfacesDropped(t *testing.T) {
	s := testServer(t, fakeObsUpstream(t, nil, nil, nil, nil), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	var out obsstore.Status
	callTool(t, cs, "observe_status", map[string]any{}, &out)
	if out.Dropped != 7 {
		t.Errorf("Dropped = %d, want 7", out.Dropped)
	}
	if len(out.Tables) != 1 || out.Tables[0].Rows != 42 {
		t.Errorf("table stats lost in transit: %+v", out.Tables)
	}
}

// TestMCPObserveWithoutAStoreSaysSo is the honesty requirement. An agent that
// gets an empty result concludes nothing happened and stops looking; an agent
// told the capability is absent knows the data never existed.
func TestMCPObserveWithoutAStoreSaysSo(t *testing.T) {
	s := testServer(t, noStoreUpstream(t), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "observe_logs",
		Arguments: map[string]any{"service": "web"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("querying an absent store succeeded, returning %s", toolText(res))
	}
	msg := toolText(res)
	for _, want := range []string{"observability store", "--obs"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// TestMCPObserveErrorsResource covers the context resource: recent workload
// errors a client can attach to a conversation, so an agent starts out already
// knowing what the user's code reported.
func TestMCPObserveErrorsResource(t *testing.T) {
	var seen url.Values
	s := testServer(t, fakeObsUpstream(t, obsFixtureEntries(), nil, nil, &seen), fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	list, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	var found *mcp.Resource
	for _, r := range list.Resources {
		if r.URI == obsErrorsURI {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("resource %s is not listed", obsErrorsURI)
	}
	// It must distinguish itself from the neighbouring activity resource, or a
	// client cannot tell which one answers "what is wrong".
	if !strings.Contains(found.Description, "user's own code") {
		t.Errorf("resource description does not distinguish it from cornus's own records: %q", found.Description)
	}

	read, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: obsErrorsURI})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(read.Contents))
	}
	var payload struct {
		Since   string              `json:"since"`
		Entries []obsstore.LogEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &payload); err != nil {
		t.Fatalf("decoding resource: %v", err)
	}
	if payload.Since != observeErrorsSince {
		t.Errorf("since = %q, want %q", payload.Since, observeErrorsSince)
	}
	if len(payload.Entries) != 2 {
		t.Errorf("got %d entries, want the fixture's 2", len(payload.Entries))
	}
	// The resource is specifically about errors; without the severity filter it
	// would be an unbounded log dump attached to every conversation.
	if seen.Get("severity") != "error" {
		t.Errorf("resource queried severity %q, want error", seen.Get("severity"))
	}
}
