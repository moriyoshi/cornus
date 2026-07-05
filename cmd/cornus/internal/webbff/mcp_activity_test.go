package webbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"cornus/pkg/activity"
)

// fakeActivityServer serves /.cornus/v1/activity as the real one does: NDJSON,
// one record per line, with the serving instance in a header. It records the
// query it was asked so a test can assert the filters actually travelled.
func fakeActivityServer(t *testing.T, events []activity.Event, live string, seen *url.Values) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.cornus/v1/activity", func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.Query()
		}
		out := events
		if r.URL.Query().Get("unfinished") != "" {
			out = activity.UnfinishedFrom(events)
		}
		if k := r.URL.Query().Get("kind"); k != "" {
			var kept []activity.Event
			for _, e := range out {
				if string(e.Kind) == k {
					kept = append(kept, e)
				}
			}
			out = kept
		}
		if live != "" {
			w.Header().Set("Cornus-Activity-Live-Instance", live)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, e := range out {
			_ = enc.Encode(e)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A crashed run and a healthy one: instance "dead" began a lifetime and a mount
// and never ended either; instance "live" is the process serving the request.
func flightFixture() []activity.Event {
	return []activity.Event{
		{TS: "2026-07-26T10:00:00Z", Proc: "server", Instance: "dead", Kind: activity.KindServer, Phase: activity.PhaseBegin, ID: "d1"},
		{TS: "2026-07-26T10:00:01Z", Proc: "server", Instance: "dead", Kind: activity.KindMount9P, Phase: activity.PhaseBegin, ID: "d2",
			Target: "/var/lib/cornus/mounts/sess-1/m0", Attrs: map[string]string{"deployment": "web"}},
		{TS: "2026-07-26T10:00:02Z", Proc: "server", Instance: "dead", Kind: activity.KindBuild, Phase: activity.PhaseBegin, ID: "d3"},
		{TS: "2026-07-26T10:00:03Z", Proc: "server", Instance: "dead", Kind: activity.KindBuild, Phase: activity.PhaseEnd, ID: "d3", Status: activity.StatusOK},
		{TS: "2026-07-26T10:05:00Z", Proc: "server", Instance: "live", Kind: activity.KindServer, Phase: activity.PhaseBegin, ID: "l1"},
	}
}

func TestMCPActivityRead(t *testing.T) {
	var seen url.Values
	upstream := fakeActivityServer(t, flightFixture(), "live", &seen)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range lt.Tools {
		if tool.Name == "activity_read" {
			found = true
			// The description has to say what makes these records different from
			// workloads_list, or a model will never reach for them after a failure.
			if !strings.Contains(tool.Description, "did not finish") {
				t.Errorf("activity_read description does not explain the begin/end model: %q", tool.Description)
			}
		}
	}
	if !found {
		t.Fatal("tools/list is missing activity_read")
	}

	var out webActivity
	callTool(t, cs, "activity_read", nil, &out)
	if len(out.Events) != 5 {
		t.Fatalf("activity_read returned %d records, want the whole flight", len(out.Events))
	}
	// Without this a reader cannot tell the serving process's own open lifetime
	// from a crashed one, and would report a healthy server as having died.
	if out.LiveInstance != "live" {
		t.Errorf("liveInstance = %q, want it carried through from the server", out.LiveInstance)
	}
}

// The filters must reach the server rather than being accepted and ignored — an
// agent that asked for unfinished records and silently got everything would draw
// exactly the wrong conclusion.
func TestMCPActivityReadPassesFiltersThrough(t *testing.T) {
	var seen url.Values
	upstream := fakeActivityServer(t, flightFixture(), "live", &seen)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	var out webActivity
	callTool(t, cs, "activity_read", map[string]any{"kind": "9p-mount", "unfinished": true}, &out)
	if seen.Get("kind") != "9p-mount" || seen.Get("unfinished") == "" {
		t.Fatalf("the server was asked %v, want kind and unfinished forwarded", seen)
	}
	if len(out.Events) != 1 || out.Events[0].Target != "/var/lib/cornus/mounts/sess-1/m0" {
		t.Fatalf("activity_read = %+v, want just the stranded mount", out.Events)
	}

	// "2h" is what an investigator types; it has to become a concrete instant on
	// the wire, not be passed through as an uninterpreted string.
	callTool(t, cs, "activity_read", map[string]any{"since": "2h"}, &out)
	if since := seen.Get("since"); since == "2h" || since == "" {
		t.Errorf("since on the wire = %q, want a resolved RFC3339 instant", since)
	}
}

// A malformed since is a tool error, not an empty result: silently returning
// nothing reads as "nothing happened", which is the worst possible answer from a
// flight recorder.
func TestMCPActivityReadRejectsABadSince(t *testing.T) {
	upstream := fakeActivityServer(t, flightFixture(), "live", nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "activity_read", Arguments: map[string]any{"since": "yesterday"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("an unparseable since must be a tool error, not an empty flight")
	}
}

// The unfinished set is offered as a RESOURCE as well as a tool because it is
// context, not an action: a client can attach it the way it attaches a file, so
// an agent asked about a misbehaving deploy already knows the last server died
// mid-flight instead of having to suspect it first.
func TestMCPActivityUnfinishedResource(t *testing.T) {
	upstream := fakeActivityServer(t, flightFixture(), "live", nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	cs := connectMCP(t, s)

	lr, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var listed bool
	for _, r := range lr.Resources {
		if r.URI == activityUnfinishedURI {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("resources/list is missing %s", activityUnfinishedURI)
	}

	rr, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: activityUnfinishedURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Contents) != 1 {
		t.Fatalf("got %d contents, want one", len(rr.Contents))
	}
	if rr.Contents[0].URI != activityUnfinishedURI {
		t.Errorf("content URI = %q, want the requested resource", rr.Contents[0].URI)
	}
	var out webActivity
	if err := json.Unmarshal([]byte(rr.Contents[0].Text), &out); err != nil {
		t.Fatalf("resource body is not JSON: %v", err)
	}
	// The crashed run's lifetime and its stranded mount, and the live server's own
	// open lifetime — which liveInstance is what marks as not-a-crash.
	ids := map[string]bool{}
	for _, e := range out.Events {
		ids[e.ID] = true
	}
	for _, want := range []string{"d1", "d2", "l1"} {
		if !ids[want] {
			t.Errorf("unfinished resource is missing %s: %+v", want, out.Events)
		}
	}
	if ids["d3"] {
		t.Error("a completed build was reported as unfinished")
	}
	if out.LiveInstance != "live" {
		t.Error("without liveInstance the serving process's own open lifetime reads as a crash")
	}
}
