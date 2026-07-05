package clientagent

import (
	"sort"
	"testing"

	"cornus/pkg/api"
)

func fwdSvc(project, svc string) Service {
	return Service{
		Name:         svc,
		Spec:         api.DeploySpec{Name: project + "-" + svc, Ports: []api.PortMapping{{Host: 0, Container: 80}}},
		ForwardPorts: true,
		ForwardOnly:  true,
	}
}

// watchReq builds a watched `up` (Watch=true). watchFiles is left nil so the
// agent's watch loop never actually fires a re-exec during the test — these
// tests exercise the arm/prune/pin/teardown state machine, not the re-exec.
func watchReq(project string, svcs []Service) Request {
	return Request{
		Action:   "up",
		Project:  project,
		Conn:     ConnSpec{Server: "http://fake:5000"},
		Services: svcs,
		Watch:    true,
		Reload:   &ReloadSpec{Argv: []string{"compose", "up", "-d", "--watch"}},
	}
}

func running(a *Agent, project string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.projects[project]
	if e == nil {
		return nil
	}
	out := append([]string(nil), e.project.Running()...)
	sort.Strings(out)
	return out
}

// TestAgentWatchArmsPinsAndFullDownReaps covers the watch lifecycle: a watched
// up marks the project watching, the pin survives the project going empty (a
// reload that removed every held service), removeProject refuses to reap it while
// watching, and a full down stops the watch and releases everything.
func TestAgentWatchArmsPinsAndFullDownReaps(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	if resp, _ := a.dispatch(watchReq("proj", []Service{fwdSvc("proj", "web")})); !resp.OK {
		t.Fatalf("watch up = %+v", resp)
	}
	a.mu.Lock()
	e := a.projects["proj"]
	watching := e != nil && e.watching && e.watchTok != nil
	a.mu.Unlock()
	if !watching {
		t.Fatal("project not armed for watching after a watch up")
	}

	// A reload that dropped the only service prunes it to empty (ApplyExact), but
	// the project must stay pinned by the watch.
	if resp, _ := a.dispatch(watchReq("proj", nil)); !resp.OK {
		t.Fatalf("watch reload up = %+v", resp)
	}
	if got := running(a, "proj"); len(got) != 0 {
		t.Fatalf("reload did not prune to empty; running=%v", got)
	}
	a.removeProject("proj") // must be a no-op: watching pins it
	a.mu.Lock()
	_, present := a.projects["proj"]
	a.mu.Unlock()
	if !present {
		t.Fatal("watch-only project was reaped despite being watched")
	}

	// A full down stops the watch and reaps the (now unpinned) project + conn.
	if resp, _ := a.dispatch(Request{Action: "down", Project: "proj"}); !resp.OK {
		t.Fatalf("down = %+v", resp)
	}
	a.mu.Lock()
	_, present = a.projects["proj"]
	nConns := len(a.conns)
	a.mu.Unlock()
	if present {
		t.Fatal("project not reaped after full down")
	}
	if nConns != 0 {
		t.Fatalf("conns not released after full down: %d", nConns)
	}
}

// TestAgentWatchPrunesRemovedService verifies a watched reload reconciles to
// EXACTLY the sent set: a service present before but absent from the reload is
// torn down.
func TestAgentWatchPrunesRemovedService(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	a.dispatch(watchReq("proj", []Service{fwdSvc("proj", "a"), fwdSvc("proj", "b")}))
	if got := running(a, "proj"); len(got) != 2 {
		t.Fatalf("want 2 running after initial watch up, got %v", got)
	}

	// Reload with only 'a' -> 'b' is pruned.
	a.dispatch(watchReq("proj", []Service{fwdSvc("proj", "a")}))
	if got := running(a, "proj"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("watched reload did not prune 'b'; running=%v", got)
	}
}

// TestAgentNonWatchUpMerges locks that pruning is confined to the watch path: a
// plain (non-watch) up still MERGES into the desired set, preserving partial
// `up SERVICE` semantics.
func TestAgentNonWatchUpMerges(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	a.dispatch(fwdOnlyReq("proj", "a"))
	a.dispatch(fwdOnlyReq("proj", "b"))
	if got := running(a, "proj"); len(got) != 2 {
		t.Fatalf("non-watch ups should merge to 2 services, got %v", got)
	}
}

// TestAgentWatchPartialDownKeepsWatch verifies a partial `down SERVICE` on a
// watched project leaves the watcher armed (only a full down stops it).
func TestAgentWatchPartialDownKeepsWatch(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	a.dispatch(watchReq("proj", []Service{fwdSvc("proj", "a"), fwdSvc("proj", "b")}))
	if resp, _ := a.dispatch(Request{Action: "down", Project: "proj", Names: []string{"a"}}); !resp.OK {
		t.Fatalf("partial down = %+v", resp)
	}
	a.mu.Lock()
	e := a.projects["proj"]
	stillWatching := e != nil && e.watching
	a.mu.Unlock()
	if !stillWatching {
		t.Fatal("partial down stopped the watch; it should only stop on a full down")
	}
	if got := running(a, "proj"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("partial down should leave 'b'; running=%v", got)
	}
}
