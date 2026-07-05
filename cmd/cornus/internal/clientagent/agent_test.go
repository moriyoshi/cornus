package clientagent

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/supervisor"
)

// newTestAgent wires an agent without Serve (no socket / signals), so dispatch
// methods can be driven directly.
func newTestAgent(t *testing.T, resolve ResolveFunc) *Agent {
	t.Helper()
	a := New(resolve)
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
