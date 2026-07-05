package healthengine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/api"
)

// These pin the state machine against DOCKER's rules, not against merely
// reasonable ones: compose `depends_on: condition: service_healthy` compares
// against Docker's vocabulary, so a difference here decides whether real compose
// files converge.
//
// Durations are milliseconds so the suite stays fast; the rules under test are
// about ordering and counting, not about wall-clock.

// scriptedProbe returns results in order, repeating the last one forever, and
// counts calls.
type scriptedProbe struct {
	mu      sync.Mutex
	results []probeResult
	n       int
	calls   atomic.Int64
	argv    []string
}

type probeResult struct {
	code  int
	err   error
	block time.Duration // hold the probe this long (for the timeout rule)
}

func (s *scriptedProbe) fn(ctx context.Context, _ string, argv []string) (int, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.argv = argv
	r := s.results[min(s.n, len(s.results)-1)]
	s.n++
	s.mu.Unlock()
	if r.block > 0 {
		select {
		case <-ctx.Done():
			return 0, ctx.Err() // the timeout path: an unfinished probe is a failure
		case <-time.After(r.block):
		}
	}
	return r.code, r.err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func pass() probeResult { return probeResult{code: 0} }
func fail() probeResult { return probeResult{code: 1} }

// waitState polls until the engine reports want, or fails the test.
func waitState(t *testing.T, e *Engine, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got = e.State(id); got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("state = %q, want %q", got, want)
}

// mustNotReach fails if the engine ever reports bad within the window. Used for
// the rules that are about something NOT happening, where waiting for a positive
// state would pass for the wrong reason.
func mustNotReach(t *testing.T, e *Engine, id, bad string, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if e.State(id) == bad {
			t.Fatalf("state reached %q, which this rule says it must not", bad)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPassGoesHealthy(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "true"}, Interval: "5ms"})
	waitState(t, e, "i", StateHealthy)
}

// TestFailuresDuringStartPeriodDoNotCount is the rule most easily got wrong, and
// getting it wrong makes every slow-starting workload flap to unhealthy. With
// retries=1, a single counted failure would be enough — so if the start period is
// not honoured this reaches unhealthy almost immediately.
func TestFailuresDuringStartPeriodDoNotCount(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{fail()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{
		Test: []string{"CMD", "false"}, Interval: "5ms", Retries: 1, StartPeriod: "1s",
	})
	mustNotReach(t, e, "i", StateUnhealthy, 200*time.Millisecond)
	if got := e.State("i"); got != StateStarting {
		t.Fatalf("state = %q during the start period, want %q", got, StateStarting)
	}
	if p.calls.Load() < 2 {
		t.Fatalf("probe ran %d times; the test did not actually exercise repeated failures", p.calls.Load())
	}
}

// TestStartPeriodEndsAtFirstSuccess: Docker treats a success as "the container
// started", after which failures count normally EVEN IF the start period has not
// elapsed. Without this, a workload that passes once and then breaks stays
// "healthy" for the rest of a long start period.
func TestStartPeriodEndsAtFirstSuccess(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass(), fail()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{
		Test: []string{"CMD", "x"}, Interval: "5ms", Retries: 1, StartPeriod: "10s",
	})
	waitState(t, e, "i", StateHealthy)
	// Still deep inside the 10s start period, yet the failures must now count.
	waitState(t, e, "i", StateUnhealthy)
}

// TestRetriesMustBeConsecutive pins that a success RESETS the consecutive-failure
// count.
//
// The obvious version of this test — fail, fail, pass, then fail forever, assert
// it eventually goes unhealthy — passes whether or not the reset exists, because
// both paths reach unhealthy and it only asserts that they arrive. A
// neutralization sweep caught exactly that: removing `fails = 0` failed no test.
//
// So the script never gives 3 consecutive failures: two, a success, then two more.
// With the reset the count never reaches 3 and it stays healthy; WITHOUT it the
// four failures accumulate past the threshold and it flips. Asserting the flip
// never happens is deterministic, where counting probes to catch it would race
// with the poll interval.
func TestRetriesMustBeConsecutive(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{fail(), fail(), pass(), fail(), fail(), pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "x"}, Interval: "5ms", Retries: 3})
	waitState(t, e, "i", StateHealthy) // the first pass landed
	mustNotReach(t, e, "i", StateUnhealthy, 300*time.Millisecond)
	if got := e.State("i"); got != StateHealthy {
		t.Fatalf("state = %q after two non-consecutive failure runs, want %q", got, StateHealthy)
	}
	if p.calls.Load() < 6 {
		t.Fatalf("probe ran only %d times; the script never got past the reset", p.calls.Load())
	}
}

// TestBelowRetriesStaysStarting: retries=5 with an endless failure stream must not
// reach unhealthy before the fifth. Bounded by call count rather than by time, so
// it does not pass merely because the machine was slow.
func TestBelowRetriesStaysStarting(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{fail()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "x"}, Interval: "5ms", Retries: 5})
	for p.calls.Load() < 3 {
		if e.State("i") == StateUnhealthy {
			t.Fatalf("unhealthy after %d failures, want no earlier than 5", p.calls.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}
	waitState(t, e, "i", StateUnhealthy) // and it does get there eventually
}

// TestExecErrorIsAFailedProbe: exec against a container that is not up yet
// returns an error. Docker counts that as the probe failing; treating it as an
// engine fault would leave the state stuck.
func TestExecErrorIsAFailedProbe(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{{err: errors.New("container not running")}}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "x"}, Interval: "5ms", Retries: 2})
	waitState(t, e, "i", StateUnhealthy)
}

// TestProbeTimeoutIsAFailure: a probe that never returns must be abandoned at
// Timeout and counted, not allowed to wedge the loop.
func TestProbeTimeoutIsAFailure(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{{code: 0, block: time.Hour}}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{
		Test: []string{"CMD", "sleep"}, Interval: "5ms", Timeout: "20ms", Retries: 2,
	})
	waitState(t, e, "i", StateUnhealthy)
}

// TestUnwatchStopsProbing asserts on the CALL COUNTER after a quiet period rather
// than on a sleep-and-hope: a leaked goroutine keeps probing forever, and the only
// evidence is that calls keep arriving.
func TestUnwatchStopsProbing(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "x"}, Interval: "5ms"})
	waitState(t, e, "i", StateHealthy)

	e.Unwatch("i")
	if got := e.State("i"); got != "" {
		t.Fatalf("state after Unwatch = %q, want \"\" (no healthcheck)", got)
	}
	settled := p.calls.Load()
	time.Sleep(100 * time.Millisecond) // many intervals
	if after := p.calls.Load(); after != settled {
		t.Fatalf("probe ran %d more times after Unwatch; the loop leaked", after-settled)
	}
	if got := e.State("i"); got != "" {
		t.Fatalf("a late probe resurrected the state to %q after Unwatch", got)
	}
}

// TestNoHealthcheckHasNoState: "" is what api.InstanceStatus.Health means when
// absent, so a backend can assign State unconditionally.
func TestNoHealthcheckHasNoState(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	for _, hc := range []*api.Healthcheck{
		nil,
		{},
		{Test: []string{"NONE"}},
	} {
		e.Watch("i", hc)
		if got := e.State("i"); got != "" {
			t.Fatalf("healthcheck %+v produced state %q, want \"\"", hc, got)
		}
	}
	if p.calls.Load() != 0 {
		t.Fatalf("probed %d times for a workload with no healthcheck", p.calls.Load())
	}
}

// TestWatchIsVisibleImmediately: a caller polling Status between Watch and the
// first probe must not see "" and conclude there is no healthcheck — that is
// exactly the window compose polls in.
func TestWatchIsVisibleImmediately(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "x"}, Interval: "10s"})
	if got := e.State("i"); got != StateStarting {
		t.Fatalf("state right after Watch = %q, want %q", got, StateStarting)
	}
}

func TestCMDShellFormBecomesShellInvocation(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD-SHELL", "curl -f localhost || exit 1"}, Interval: "5ms"})
	waitState(t, e, "i", StateHealthy)
	p.mu.Lock()
	got := p.argv
	p.mu.Unlock()
	want := []string{"/bin/sh", "-c", "curl -f localhost || exit 1"}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestCMDFormDropsThePrefix(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "pg_isready", "-U", "postgres"}, Interval: "5ms"})
	waitState(t, e, "i", StateHealthy)
	p.mu.Lock()
	got := p.argv
	p.mu.Unlock()
	if len(got) != 3 || got[0] != "pg_isready" {
		t.Fatalf("argv = %v, want the CMD prefix dropped", got)
	}
}

// TestWatchReplacesTheExistingLoop: a redeploy re-Watches the same instance id,
// and two loops probing one instance would double the rate and race on state.
func TestWatchReplacesTheExistingLoop(t *testing.T) {
	p := &scriptedProbe{results: []probeResult{pass()}}
	e := New(p.fn)
	t.Cleanup(e.StopAll)
	hc := &api.Healthcheck{Test: []string{"CMD", "x"}, Interval: "5ms"}
	e.Watch("i", hc)
	waitState(t, e, "i", StateHealthy)
	e.Watch("i", hc)
	waitState(t, e, "i", StateHealthy)

	e.Unwatch("i")
	settled := p.calls.Load()
	time.Sleep(100 * time.Millisecond)
	if after := p.calls.Load(); after != settled {
		t.Fatalf("%d probes after Unwatch: the replaced loop was never cancelled", after-settled)
	}
}

func TestDefaultsMatchDocker(t *testing.T) {
	p, ok := resolve(&api.Healthcheck{Test: []string{"CMD", "x"}})
	if !ok {
		t.Fatal("a plain CMD healthcheck did not resolve")
	}
	if p.interval != DefaultInterval || p.timeout != DefaultTimeout || p.retries != DefaultRetries {
		t.Fatalf("defaults = interval %v timeout %v retries %d, want %v/%v/%d",
			p.interval, p.timeout, p.retries, DefaultInterval, DefaultTimeout, DefaultRetries)
	}
	if p.startPeriod != 0 {
		t.Errorf("default start period = %v, want 0", p.startPeriod)
	}
}

// TestNilEngineIsInert: a backend that has not wired an engine — or a
// partially-constructed one in a test — must not panic on the Status path. A nil
// Engine reports "", which is already what "no healthcheck" means.
func TestNilEngineIsInert(t *testing.T) {
	var e *Engine
	if got := e.State("i"); got != "" {
		t.Fatalf("nil engine State = %q, want \"\"", got)
	}
	e.Watch("i", &api.Healthcheck{Test: []string{"CMD", "x"}})
	e.Unwatch("i")
	e.StopAll()
	if got := e.State("i"); got != "" {
		t.Fatalf("nil engine State = %q after Watch, want \"\"", got)
	}
}
