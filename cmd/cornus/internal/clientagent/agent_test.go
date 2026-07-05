package clientagent

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/clientconduit"
	"cornus/pkg/conduithost"
	"cornus/pkg/supervisor"
)

// newTestAgent wires an agent without Serve (no socket / signals), so dispatch
// methods can be driven directly.
func newTestAgent(t *testing.T, resolve ResolveFunc) *Agent {
	t.Helper()
	a := New(resolve)
	// Never the real rendezvous: without this the agent's tests write into the
	// user's runtime directory and contend for real addresses.
	a.conduitRegistry = conduithost.NewRegistry(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.ctx, a.stop = ctx, cancel
	a.sup = supervisor.New(ctx, func(string, ...any) {})
	return a
}

// fakeResolve returns a Conn with a bogus endpoint (never dialed by ForwardOnly
// services) and records the specs it saw.
func fakeResolve(seen *[]ConnSpec) ResolveFunc {
	return func(s ConnSpec) (*clientconn.Conn, error) {
		if seen != nil {
			*seen = append(*seen, s)
		}
		return &clientconn.Conn{Endpoint: "http://fake:5000", Cleanup: func() {}}, nil
	}
}

// conduitCfg builds a port-forward conduit configuration distinguished only by its
// ingress settings, so two configurations key two different conduits without
// binding a SOCKS5 proxy. It exercises exactly the settings the old first-writer
// behavior kept stale (ingress suffix / CA / controller live in the conduit
// identity).
func conduitCfg(suffix string) WireConduitCfg {
	return ToWireConduit(clientconduit.Config{
		Mode: clientconduit.ModePortForward,
		Ingress: &clientconduit.IngressConfig{
			Mode:         clientconduit.IngressEmulate,
			SuffixDomain: suffix,
			CAFile:       "/ca/" + suffix + ".pem",
		},
	})
}

func fwdOnlyReqWith(project, svc string, cfg WireConduitCfg) Request {
	req := fwdOnlyReq(project, svc)
	req.Conduit = cfg
	return req
}

func fwdOnlyReq(project, svc string) Request {
	return Request{
		Action:  "up",
		Project: project,
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Services: []Service{{
			Name:         svc,
			Spec:         api.DeploySpec{Name: project + "-" + svc, Ports: []api.PortMapping{{Host: 0, Container: 80}}},
			ForwardPorts: true,
			ForwardOnly:  true,
		}},
	}
}

func TestAgentUpDownReleasesConn(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	resp, _ := a.dispatch(fwdOnlyReq("proj", "web"))
	if !resp.OK {
		t.Fatalf("up = %+v", resp)
	}
	if got := resp.Running; len(got) != 1 || got[0] != "web" {
		t.Fatalf("running = %v, want [web]", got)
	}
	a.mu.Lock()
	nConns, nProjects := len(a.conns), len(a.projects)
	a.mu.Unlock()
	if nConns != 1 || nProjects != 1 {
		t.Fatalf("conns=%d projects=%d, want 1,1", nConns, nProjects)
	}

	// down empties the project -> project + conn released.
	resp, _ = a.dispatch(Request{Action: "down", Project: "proj"})
	if !resp.OK {
		t.Fatalf("down = %+v", resp)
	}
	a.mu.Lock()
	nConns, nProjects = len(a.conns), len(a.projects)
	a.mu.Unlock()
	if nConns != 0 || nProjects != 0 {
		t.Fatalf("after down conns=%d projects=%d, want 0,0", nConns, nProjects)
	}
}

func TestAgentTwoProjectsShareOneConn(t *testing.T) {
	var seen []ConnSpec
	a := newTestAgent(t, fakeResolve(&seen))

	if resp, _ := a.dispatch(fwdOnlyReq("p1", "a")); !resp.OK {
		t.Fatalf("up p1 = %+v", resp)
	}
	if resp, _ := a.dispatch(fwdOnlyReq("p2", "b")); !resp.OK {
		t.Fatalf("up p2 = %+v", resp)
	}
	a.mu.Lock()
	nConns := len(a.conns)
	var refs int
	for _, cs := range a.conns {
		refs = cs.refs
	}
	a.mu.Unlock()
	if nConns != 1 {
		t.Fatalf("two projects on one server = %d connStates, want 1", nConns)
	}
	if refs != 2 {
		t.Fatalf("connState refs = %d, want 2", refs)
	}
	// The resolver is consulted once per shared connState.
	if len(seen) != 1 {
		t.Fatalf("resolver called %d times, want 1 (shared)", len(seen))
	}
}

// TestIdleCheckHonorsInflightAndWork locks the fix for the cold-start race: the
// agent must not idle-exit while a request is in flight (conn resolved but the
// counted child not yet added) or while it holds any project/docker frontend.
func TestIdleCheckHonorsInflightAndWork(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	var stopped atomic.Bool
	a.stop = func() { stopped.Store(true) }

	// A request in flight => not idle even with no registered work.
	a.mu.Lock()
	a.inflight = 1
	a.mu.Unlock()
	a.idleCheck()
	if stopped.Load() {
		t.Fatal("idleCheck stopped the agent with a request in flight")
	}

	// A live project => not idle.
	a.mu.Lock()
	a.inflight = 0
	a.mu.Unlock()
	a.dispatch(fwdOnlyReq("proj", "web"))
	stopped.Store(false)
	a.idleCheck()
	if stopped.Load() {
		t.Fatal("idleCheck stopped the agent while holding a project")
	}

	// Fully idle => exit.
	a.dispatch(Request{Action: "down", Project: "proj"})
	a.idleCheck()
	if !stopped.Load() {
		t.Fatal("idleCheck did not stop a fully idle agent")
	}
}

// TestDownKeepsConnWhileUpInFlight locks the fix for the concurrent up+down
// race: while an `up` handler is in flight (entry.active > 0), a `down` that
// empties the project must NOT release the shared conn/conduit out from under the
// StartService calls still using them.
func TestDownKeepsConnWhileUpInFlight(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	if resp, _ := a.dispatch(fwdOnlyReq("proj", "web")); !resp.OK {
		t.Fatalf("up = %+v", resp)
	}
	// Simulate an `up` handler mid-flight on this project (as doUp does between
	// ensureProject and releaseProjectUse).
	a.mu.Lock()
	entry := a.projects["proj"]
	entry.active++
	a.mu.Unlock()

	// A concurrent down empties the project, but must leave conn/conduit alive.
	if resp, _ := a.dispatch(Request{Action: "down", Project: "proj"}); !resp.OK {
		t.Fatalf("down = %+v", resp)
	}
	a.mu.Lock()
	nConns, nProjects := len(a.conns), len(a.projects)
	a.mu.Unlock()
	if nConns != 1 || nProjects != 1 {
		t.Fatalf("down released project mid-up: conns=%d projects=%d, want 1,1", nConns, nProjects)
	}

	// Once the in-flight up finishes, the now-empty project is collectible.
	a.mu.Lock()
	entry.active--
	a.mu.Unlock()
	a.removeProject("proj")
	a.mu.Lock()
	nConns, nProjects = len(a.conns), len(a.projects)
	a.mu.Unlock()
	if nConns != 0 || nProjects != 0 {
		t.Fatalf("after up settled: conns=%d projects=%d, want 0,0", nConns, nProjects)
	}
}

// TestConcurrentUpDownNoUseAfterFree hammers up+down on one project from many
// goroutines; under -race it catches any teardown of a conn/conduit that an
// overlapping up still references. The invariant asserted here is that the agent
// stays internally consistent (a project is never left holding a conn whose
// refcount underflowed) and that a final quiescent down fully releases.
func TestConcurrentUpDownNoUseAfterFree(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.dispatch(fwdOnlyReq("proj", "web")) }()
		go func() { defer wg.Done(); a.dispatch(Request{Action: "down", Project: "proj"}) }()
	}
	wg.Wait()

	// Quiesce: a final down (with no up in flight) must fully release everything.
	a.dispatch(Request{Action: "down", Project: "proj"})
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cs := range a.conns {
		if cs.refs < 0 {
			t.Fatalf("connState refcount underflowed: %d", cs.refs)
		}
	}
	if len(a.projects) != 0 {
		t.Fatalf("projects not released after quiescent down: %d", len(a.projects))
	}
	if len(a.conns) != 0 {
		t.Fatalf("conns not released after quiescent down: %d", len(a.conns))
	}
}

// TestHandleReapsSilentClient locks the fix for the leaked-goroutine defect: a
// client that connects to the control socket but never writes must be reaped by
// the read deadline instead of parking handle in Decode forever.
func TestHandleReapsSilentClient(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	// Shrink the request read timeout for the test; restore after.
	prev := requestReadTimeout
	requestReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { requestReadTimeout = prev })

	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = cli.Close() })

	done := make(chan struct{})
	go func() { a.handle(srv); close(done) }()

	// The client never writes. handle must return once the read deadline fires.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return for a silent client; the read deadline is missing")
	}
}

// forwardAddr extracts the bound local address from a "127.0.0.1:x -> :80" line.
func forwardAddr(t *testing.T, forwards map[string][]string, svc string) string {
	t.Helper()
	got := forwards[svc]
	if len(got) != 1 {
		t.Fatalf("forwards for %s = %v, want exactly one bound listener", svc, got)
	}
	return strings.Fields(got[0])[0]
}

// conduitRefs snapshots a project's connState, the conduit key it records, and the
// refcounts on both.
func conduitRefs(t *testing.T, a *Agent, project string) (conn *connState, key conduitKey, refs, nConduits, connRefs int) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.projects[project]
	if e == nil {
		t.Fatalf("project %q is not held by the agent", project)
	}
	if es := e.conn.conduit[e.egKey]; es != nil {
		refs = es.refs
	}
	return e.conn, e.egKey, refs, len(e.conn.conduit), e.conn.refs
}

// The core fix: a same-name `up` carrying changed conduit settings must reconcile
// the live project onto the new conduit instead of silently keeping the first
// writer's. An `up` with unchanged settings must change nothing.
func TestAgentUpReconcilesChangedConduitSettings(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	t.Cleanup(func() { a.dispatch(Request{Action: "down", Project: "proj"}) })

	resp, _ := a.dispatch(fwdOnlyReqWith("proj", "web", conduitCfg("a.test")))
	if !resp.OK {
		t.Fatalf("first up = %+v", resp)
	}
	firstAddr := forwardAddr(t, resp.Forwards, "web")
	a.mu.Lock()
	firstProject := a.projects["proj"].project
	a.mu.Unlock()

	// Unchanged settings: no rebind, no warning, and the very same bound listener.
	resp, _ = a.dispatch(fwdOnlyReqWith("proj", "web", conduitCfg("a.test")))
	if !resp.OK {
		t.Fatalf("re-up (unchanged) = %+v", resp)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("re-up with identical conduit settings warned: %v", resp.Warnings)
	}
	if resp.Statuses["web"] != StatusUpToDate {
		t.Fatalf("re-up (unchanged) status = %q, want %q", resp.Statuses["web"], StatusUpToDate)
	}
	if got := forwardAddr(t, resp.Forwards, "web"); got != firstAddr {
		t.Fatalf("re-up with identical conduit settings rebound the listener: %s -> %s", firstAddr, got)
	}

	// Changed ingress settings: reconciled in place onto the new conduit.
	resp, _ = a.dispatch(fwdOnlyReqWith("proj", "web", conduitCfg("b.test")))
	if !resp.OK {
		t.Fatalf("re-up (changed conduit) = %+v", resp)
	}
	if got := forwardAddr(t, resp.Forwards, "web"); got == firstAddr {
		t.Fatalf("changed conduit settings left the exposure on the old conduit (%s)", got)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "reconciled it onto the new conduit") {
		t.Fatalf("warnings = %v, want one line reporting the reconcile", resp.Warnings)
	}
	assertListenerGone(t, firstAddr) // the conduit we left is closed, not leaked

	// Reconciled in place, not torn down and rebuilt: the same live Project (and so
	// the same mount sessions and desired-state history) carries through.
	a.mu.Lock()
	sameProject := a.projects["proj"].project == firstProject
	a.mu.Unlock()
	if !sameProject {
		t.Fatal("the conduit reconcile replaced the live Project instead of rebinding it (mount sessions would have been rebuilt)")
	}

	_, key, refs, nConduits, connRefs := conduitRefs(t, a, "proj")
	if want := conduitKeyOf(conduitCfg("b.test").Runtime(), "proj"); key != want {
		t.Fatalf("project conduit key = %+v, want the new settings' key %+v", key, want)
	}
	if nConduits != 1 || refs != 1 {
		t.Fatalf("after the reconcile: %d conduits, new conduit refs=%d; want 1 and 1 (the old conduit must be released exactly once)", nConduits, refs)
	}
	if connRefs != 1 {
		t.Fatalf("connState refs = %d after the reconcile, want 1 (acquire/release must stay paired)", connRefs)
	}
}

// A conduit shared with another project must survive one project's reconcile: the
// refcount is dropped, not the conduit, and the other project keeps its live
// listeners.
func TestAgentSharedConduitSurvivesProjectReconcile(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	t.Cleanup(func() {
		a.dispatch(Request{Action: "down", Project: "p1"})
		a.dispatch(Request{Action: "down", Project: "p2"})
	})

	shared := conduitCfg("shared.test")
	if resp, _ := a.dispatch(fwdOnlyReqWith("p1", "a", shared)); !resp.OK {
		t.Fatalf("up p1 = %+v", resp)
	}
	resp, _ := a.dispatch(fwdOnlyReqWith("p2", "b", shared))
	if !resp.OK {
		t.Fatalf("up p2 = %+v", resp)
	}
	p2Addr := forwardAddr(t, resp.Forwards, "b")

	sharedKey := conduitKeyOf(shared.Runtime(), "p1")
	if _, key, refs, nConduits, _ := conduitRefs(t, a, "p1"); key != sharedKey || refs != 2 || nConduits != 1 {
		t.Fatalf("before the reconcile: key match=%v refs=%d conduits=%d; want the shared key, 2, 1", key == sharedKey, refs, nConduits)
	}

	// p1 alone moves to different conduit settings.
	if resp, _ := a.dispatch(fwdOnlyReqWith("p1", "a", conduitCfg("moved.test"))); !resp.OK {
		t.Fatalf("re-up p1 (changed conduit) = %+v", resp)
	}

	a.mu.Lock()
	cs := a.projects["p1"].conn
	p1Key, p2Key := a.projects["p1"].egKey, a.projects["p2"].egKey
	sharedState, p1State := cs.conduit[sharedKey], cs.conduit[p1Key]
	nConduits, connRefs := len(cs.conduit), cs.refs
	a.mu.Unlock()

	if sharedState == nil {
		t.Fatal("p1's reconcile closed the shared conduit that p2 still uses")
	}
	if sharedState.refs != 1 {
		t.Fatalf("shared conduit refs = %d after p1 moved off it, want 1 (p2's reference)", sharedState.refs)
	}
	if p1State == nil || p1State.refs != 1 {
		t.Fatalf("p1's new conduit = %+v, want one live conduit with refs 1", p1State)
	}
	if p1Key == sharedKey {
		t.Fatal("p1 was not moved off the shared conduit")
	}
	if p2Key != sharedKey {
		t.Fatal("p1's reconcile moved p2 off the shared conduit")
	}
	if nConduits != 2 || connRefs != 2 {
		t.Fatalf("conduits=%d connRefs=%d, want 2 and 2", nConduits, connRefs)
	}

	// p2 is untouched: same running service, same bound listener, still accepting.
	a.mu.Lock()
	p2 := a.projects["p2"].project
	a.mu.Unlock()
	if got := p2.Running(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("p2 running = %v after p1's reconcile, want [b]", got)
	}
	if got := forwardAddr(t, p2.Forwards(), "b"); got != p2Addr {
		t.Fatalf("p1's reconcile rebound p2's listener: %s -> %s", p2Addr, got)
	}
	c, err := net.Dial("tcp", p2Addr)
	if err != nil {
		t.Fatalf("p2's listener died with p1's reconcile: %v", err)
	}
	c.Close()
}

// The server connection is IDENTITY, not configuration: the agent can only attach
// to workloads, never migrate them, so it refuses a same-name `up` aimed at a
// different server with an error naming what differs — and leaves the held project
// exactly as it was.
func TestAgentUpRejectsChangedServerConnection(t *testing.T) {
	var seen []ConnSpec
	a := newTestAgent(t, fakeResolve(&seen))
	t.Cleanup(func() { a.dispatch(Request{Action: "down", Project: "proj"}) })

	if resp, _ := a.dispatch(fwdOnlyReq("proj", "web")); !resp.OK {
		t.Fatalf("first up = %+v", resp)
	}

	moved := fwdOnlyReq("proj", "web")
	moved.Conn.Server = "http://other:5000"
	resp, _ := a.dispatch(moved)
	if resp.OK {
		t.Fatal("an up against a different server was silently accepted")
	}
	for _, want := range []string{`"proj"`, "server", "http://fake:5000", "http://other:5000", "cornus compose down"} {
		if !strings.Contains(resp.Error, want) {
			t.Fatalf("error %q does not mention %q", resp.Error, want)
		}
	}

	// The refused up changes nothing: no second connection resolved, the project
	// still runs against the original one, and its use-count is not leaked.
	a.mu.Lock()
	nConns, nProjects := len(a.conns), len(a.projects)
	entry := a.projects["proj"]
	active := entry.active
	a.mu.Unlock()
	if nConns != 1 || nProjects != 1 {
		t.Fatalf("after the refused up: conns=%d projects=%d, want 1,1", nConns, nProjects)
	}
	if len(seen) != 1 {
		t.Fatalf("resolver called %d times, want 1 (the refused up must not resolve the new server)", len(seen))
	}
	if active != 0 {
		t.Fatalf("entry.active = %d after a refused up, want 0 (the use-count leaked)", active)
	}
	if got := entry.project.Running(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("running = %v after a refused up, want [web]", got)
	}
}

// conduitKeyOf must canonicalize the configuration, so the key a conduit is
// acquired under is the key it is later released under. An unset Mode means the
// port-forward default; keying the raw config leaked the conduit and its refcount.
func TestConduitKeyOfNormalizesDefaultMode(t *testing.T) {
	unset := conduitKeyOf(ConduitCfg{}, "proj")
	explicit := conduitKeyOf(ConduitCfg{Mode: clientconduit.ModePortForward}, "proj")
	if unset != explicit {
		t.Fatalf("conduitKeyOf({}) = %+v, want the port-forward key %+v", unset, explicit)
	}
}

// ...and the end-to-end consequence: a default-configured conduit shared by two
// projects must lose exactly one reference when one project goes down.
func TestDefaultConduitRefcountIsReleasedOnDown(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	if resp, _ := a.dispatch(fwdOnlyReq("p1", "a")); !resp.OK {
		t.Fatalf("up p1 = %+v", resp)
	}
	if resp, _ := a.dispatch(fwdOnlyReq("p2", "b")); !resp.OK {
		t.Fatalf("up p2 = %+v", resp)
	}
	if _, _, refs, nConduits, _ := conduitRefs(t, a, "p1"); refs != 2 || nConduits != 1 {
		t.Fatalf("two projects on the default conduit: refs=%d conduits=%d, want 2,1", refs, nConduits)
	}

	if resp, _ := a.dispatch(Request{Action: "down", Project: "p1"}); !resp.OK {
		t.Fatalf("down p1 = %+v", resp)
	}
	if _, _, refs, nConduits, _ := conduitRefs(t, a, "p2"); refs != 1 || nConduits != 1 {
		t.Fatalf("after down p1: refs=%d conduits=%d, want 1,1 (the release must find the conduit)", refs, nConduits)
	}

	if resp, _ := a.dispatch(Request{Action: "down", Project: "p2"}); !resp.OK {
		t.Fatalf("down p2 = %+v", resp)
	}
	a.mu.Lock()
	nConns, nProjects := len(a.conns), len(a.projects)
	a.mu.Unlock()
	if nConns != 0 || nProjects != 0 {
		t.Fatalf("after both downs: conns=%d projects=%d, want 0,0", nConns, nProjects)
	}
}

// Concurrent `up`s alternating between two conduit configurations (plus downs)
// must never underflow or leak a refcount: every acquire is paired with exactly
// one release, and the entry's recorded conduit key always names a live conduit.
// Run under -race, this is the refcounted-state stress for the rebind path.
func TestConcurrentConduitReconcileKeepsRefcounts(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	cfgs := []WireConduitCfg{conduitCfg("x.test"), conduitCfg("y.test")}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		cfg := cfgs[i%2]
		wg.Add(2)
		go func() { defer wg.Done(); a.dispatch(fwdOnlyReqWith("proj", "web", cfg)) }()
		go func() { defer wg.Done(); a.dispatch(Request{Action: "down", Project: "proj"}) }()
	}
	wg.Wait()

	// Quiesce: one settled up, then a full down must release everything.
	if resp, _ := a.dispatch(fwdOnlyReqWith("proj", "web", cfgs[0])); !resp.OK {
		t.Fatalf("settling up = %+v", resp)
	}
	cs, key, refs, nConduits, connRefs := conduitRefs(t, a, "proj")
	if refs != 1 || nConduits != 1 || connRefs != 1 {
		t.Fatalf("after the storm: conduit refs=%d conduits=%d connRefs=%d, want 1,1,1", refs, nConduits, connRefs)
	}
	if cs.conduit[key] == nil {
		t.Fatal("the entry's conduit key names no live conduit")
	}
	if resp, _ := a.dispatch(Request{Action: "down", Project: "proj"}); !resp.OK {
		t.Fatalf("final down = %+v", resp)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.conns) != 0 || len(a.projects) != 0 {
		t.Fatalf("after the final down: conns=%d projects=%d, want 0,0", len(a.conns), len(a.projects))
	}
}

func TestAgentStatusAndControl(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	a.dispatch(fwdOnlyReq("proj", "web"))

	resp, _ := a.dispatch(Request{Action: "status"})
	if !resp.OK || resp.Inventory == nil {
		t.Fatalf("status = %+v", resp)
	}
	if got := resp.Inventory.Projects["proj"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("inventory projects = %v", resp.Inventory.Projects)
	}

	if resp, _ := a.dispatch(Request{Action: "ping"}); !resp.OK {
		t.Fatalf("ping = %+v", resp)
	}
	if resp, _ := a.dispatch(Request{Action: "bogus"}); resp.OK {
		t.Fatalf("bogus should not be OK: %+v", resp)
	}
	// stop signals exit.
	if resp, exit := a.dispatch(Request{Action: "stop"}); !resp.OK || !exit {
		t.Fatalf("stop = %+v exit=%v; want ok + exit", resp, exit)
	}
}
