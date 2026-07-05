package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/compose"
)

// testDriver renders a plain-mode driver whose stderr (where progress events and
// warnings land) is captured in buf, so the reconcile reporters can be asserted
// on without a terminal.
func testDriver(buf *bytes.Buffer) *cliout.Driver {
	return cliout.New(cliout.Options{Stdout: io.Discard, Stderr: buf, Output: "plain"})
}

// scriptedPoller returns the next scripted status on each Status call, repeating
// the last entry once the script is exhausted. An entry with err set is returned
// as a transient error (nil status).
type scriptedPoller struct {
	mu     sync.Mutex
	steps  []pollStep
	called int
}

type pollStep struct {
	st  api.DeployStatus
	err error
}

func (p *scriptedPoller) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.called
	if i >= len(p.steps) {
		i = len(p.steps) - 1
	}
	p.called++
	s := p.steps[i]
	return s.st, s.err
}

// count reports how many Status calls have been made so far.
func (p *scriptedPoller) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.called
}

func inst(id, state string, running bool) api.InstanceStatus {
	return api.InstanceStatus{ID: id, State: state, Running: running}
}

func status(instances ...api.InstanceStatus) api.DeployStatus {
	return api.DeployStatus{Name: "web", Instances: instances}
}

func healthInst(id, state string, running bool, health string) api.InstanceStatus {
	return api.InstanceStatus{ID: id, State: state, Running: running, Health: health}
}

func exitedInst(id string, code int) api.InstanceStatus {
	return api.InstanceStatus{ID: id, State: "exited", Running: false, ExitCode: &code}
}

func TestDependencySatisfied(t *testing.T) {
	code1 := 1
	cases := []struct {
		name      string
		st        api.DeployStatus
		condition string
		want      bool
	}{
		{"started ok", status(inst("web-0", "running", true)), compose.DependsOnStarted, true},
		{"started not running", status(inst("web-0", "pending", false)), compose.DependsOnStarted, false},
		{"started no instances", status(), compose.DependsOnStarted, false},
		{"started partial", status(inst("web-0", "running", true), inst("web-1", "pending", false)), compose.DependsOnStarted, false},
		{"healthy ok", status(healthInst("web-0", "running", true, "healthy")), compose.DependsOnHealthy, true},
		{"healthy starting", status(healthInst("web-0", "running", true, "starting")), compose.DependsOnHealthy, false},
		{"healthy empty health (containerd)", status(inst("web-0", "running", true)), compose.DependsOnHealthy, false},
		{"healthy no instances", status(), compose.DependsOnHealthy, false},
		{"completed ok", status(exitedInst("web-0", 0)), compose.DependsOnCompleted, true},
		{"completed nonzero", status(api.InstanceStatus{ID: "web-0", State: "exited", Running: false, ExitCode: &code1}), compose.DependsOnCompleted, false},
		{"completed still running", status(inst("web-0", "running", true)), compose.DependsOnCompleted, false},
		{"completed no exit code", status(inst("web-0", "exited", false)), compose.DependsOnCompleted, false},
		{"completed no instances", status(), compose.DependsOnCompleted, false},
		{"unknown treated as started", status(inst("web-0", "running", true)), "bogus_condition", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dependencySatisfied(c.st, c.condition); got != c.want {
				t.Errorf("dependencySatisfied(%s) = %v, want %v", c.condition, got, c.want)
			}
		})
	}
}

// depRuntime builds a minimal runtime whose service `dependent` depends_on the
// given dependencies, each mapped to a "<dep>-res" deployment resource.
func depRuntime(dependent string, deps compose.DependsOn) *runtime {
	svcs := map[string]compose.ServiceDocument{dependent: {DependsOn: deps}}
	plans := map[string]compose.ServicePlan{}
	for _, d := range deps {
		svcs[d.Service] = compose.ServiceDocument{}
		plans[d.Service] = compose.ServicePlan{Service: d.Service, Resource: d.Service + "-res"}
	}
	return &runtime{project: compose.NewProject(&compose.ProjectDocument{Services: svcs}).View(nil), plans: plans}
}

func TestWaitForDependenciesBlocksThenProceeds(t *testing.T) {
	// db starts unhealthy (starting), then becomes healthy: the wait must block
	// then proceed with no error.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(healthInst("db-0", "running", true, "starting"))},
		{st: status(healthInst("db-0", "running", true, "healthy"))},
	}}
	rt := depRuntime("web", compose.DependsOn{{Service: "db", Condition: compose.DependsOnHealthy, Required: true}})
	selected := map[string]bool{"web": true, "db": true}
	var out bytes.Buffer
	err := waitForDependencies(context.Background(), rt, p, "web", selected, testDriver(&out), nil, time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("waitForDependencies = %v, want nil", err)
	}
	if p.count() < 2 {
		t.Errorf("expected the wait to block for at least 2 polls, got %d", p.count())
	}
	if !strings.Contains(out.String(), "waiting") {
		t.Errorf("expected a waiting event; got:\n%s", out.String())
	}
}

func TestWaitForDependenciesRequiredTimeout(t *testing.T) {
	// db never becomes healthy: a required dependency's timeout must error.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(healthInst("db-0", "running", true, "starting"))},
	}}
	rt := depRuntime("web", compose.DependsOn{{Service: "db", Condition: compose.DependsOnHealthy, Required: true}})
	selected := map[string]bool{"web": true, "db": true}
	var out bytes.Buffer
	err := waitForDependencies(context.Background(), rt, p, "web", selected, testDriver(&out), nil, time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("waitForDependencies = nil, want a required-dependency timeout error")
	}
}

func TestWaitForDependenciesOptionalTimeout(t *testing.T) {
	// db never becomes healthy but is NOT required: warn and proceed (no error).
	p := &scriptedPoller{steps: []pollStep{
		{st: status(healthInst("db-0", "running", true, "starting"))},
	}}
	rt := depRuntime("web", compose.DependsOn{{Service: "db", Condition: compose.DependsOnHealthy, Required: false}})
	selected := map[string]bool{"web": true, "db": true}
	var out bytes.Buffer
	err := waitForDependencies(context.Background(), rt, p, "web", selected, testDriver(&out), nil, time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDependencies = %v, want nil (optional dependency)", err)
	}
	if !strings.Contains(out.String(), "continuing") {
		t.Errorf("expected an optional-dependency warning; got:\n%s", out.String())
	}
}

func TestWaitForDependenciesSkipsUnselected(t *testing.T) {
	// db is not in the selection: the wait must skip it and never poll.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(healthInst("db-0", "running", true, "starting"))},
	}}
	rt := depRuntime("web", compose.DependsOn{{Service: "db", Condition: compose.DependsOnHealthy, Required: true}})
	selected := map[string]bool{"web": true} // db not selected
	var out bytes.Buffer
	err := waitForDependencies(context.Background(), rt, p, "web", selected, testDriver(&out), nil, time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDependencies = %v, want nil", err)
	}
	if p.count() != 0 {
		t.Errorf("unselected dependency was polled %d times, want 0", p.count())
	}
}

func TestReportReconcileTransitions(t *testing.T) {
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "pending", false))},
		{err: errors.New("transient")}, // must not abort the wait
		{st: status(inst("web-0", "running", true))},
	}}
	var out bytes.Buffer
	d := testDriver(&out)
	st := reportReconcile(context.Background(), p, "web", "web", d, d.Progress(), nil, time.Millisecond, 5*time.Second)

	got := out.String()
	for _, want := range []string{"web  web-0: pending\n", "web  web-0: running\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
	// A state is printed once per change, not on every poll.
	if n := strings.Count(got, "web-0: pending"); n != 1 {
		t.Errorf("pending printed %d times, want 1; got:\n%s", n, got)
	}
	if len(st.Instances) != 1 || !st.Instances[0].Running {
		t.Errorf("final status not running: %+v", st)
	}
}

func TestReportReconcileTimeout(t *testing.T) {
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "pending", false))},
	}}
	var out bytes.Buffer
	d := testDriver(&out)
	st := reportReconcile(context.Background(), p, "web", "web", d, d.Progress(), nil, time.Millisecond, 20*time.Millisecond)

	got := out.String()
	if !strings.Contains(got, "gave up waiting") {
		t.Errorf("timeout path did not report giving up; got:\n%s", got)
	}
	if len(st.Instances) != 1 || st.Instances[0].Running {
		t.Errorf("expected last-seen non-running status, got %+v", st)
	}
}

func TestReportReconcileContextCancel(t *testing.T) {
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "pending", false))},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: must return promptly without waiting the timeout
	var out bytes.Buffer
	d := testDriver(&out)
	done := make(chan struct{})
	go func() {
		reportReconcile(ctx, p, "web", "web", d, d.Progress(), nil, time.Millisecond, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reportReconcile did not return on cancelled context")
	}
}

// TestReportReconcileViaClient drives the reporter through a real *client.Client
// against an httptest server, exercising the GET /.cornus/v1/deploy/{name} path and JSON
// decode end-to-end (not just the narrow fake), and confirming *client.Client
// satisfies statusPoller.
func TestReportReconcileViaClient(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.cornus/v1/deploy/web" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		st := status(inst("web-0", "pending", false))
		if n >= 3 { // become ready after a couple of polls
			st = status(inst("web-0", "running", true))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	}))
	defer srv.Close()

	var out bytes.Buffer
	d := testDriver(&out)
	st := reportReconcile(context.Background(), client.New(srv.URL), "web", "web", d, d.Progress(), nil, time.Millisecond, 5*time.Second)
	if len(st.Instances) != 1 || !st.Instances[0].Running {
		t.Fatalf("did not converge to running: %+v", st)
	}
	got := out.String()
	for _, want := range []string{"web  web-0: pending\n", "web  web-0: running\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

func TestReportTeardownTransitions(t *testing.T) {
	// running -> terminating -> gone (empty instance set = fully removed).
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "running", true))},
		{st: status(inst("web-0", "pending", false))},
		{st: status()}, // gone
	}}
	var out bytes.Buffer
	st := reportTeardown(context.Background(), p, "web", "web", testDriver(&out), time.Millisecond, 5*time.Second)

	got := out.String()
	for _, want := range []string{"web  web-0: running\n", "web  web-0: pending\n", "web  removed\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
	if len(st.Instances) != 0 {
		t.Errorf("final status not gone: %+v", st)
	}
}

func TestReportTeardownTimeout(t *testing.T) {
	// Never becomes empty: the wait must give up and report it.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "pending", false))},
	}}
	var out bytes.Buffer
	st := reportTeardown(context.Background(), p, "web", "web", testDriver(&out), time.Millisecond, 20*time.Millisecond)

	got := out.String()
	if !strings.Contains(got, "gave up waiting for teardown") {
		t.Errorf("timeout path did not report giving up; got:\n%s", got)
	}
	if strings.Contains(got, "web  removed") {
		t.Errorf("must not report removed on timeout; got:\n%s", got)
	}
	if len(st.Instances) != 1 {
		t.Errorf("expected last-seen status with the instance, got %+v", st)
	}
}

func TestReportTeardownContextCancel(t *testing.T) {
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "running", true))},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: must return promptly without waiting the timeout
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		reportTeardown(ctx, p, "web", "web", testDriver(&out), time.Millisecond, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reportTeardown did not return on cancelled context")
	}
	if strings.Contains(out.String(), "web  removed") {
		t.Errorf("must not report removed on cancel; got:\n%s", out.String())
	}
}

func TestReportCompletionExitsPromptly(t *testing.T) {
	// A one-shot completion service: running, then exited(0). reportCompletion must
	// return as soon as it terminates, WITHOUT the "gave up waiting for running
	// state" warning that the shared Running gate would emit for an exiting service.
	// The timeout is an hour: the test proves the return is driven by the exit, not
	// the clock.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("job-0", "running", true))},
		{st: status(exitedInst("job-0", 0))},
	}}
	var out bytes.Buffer
	d := testDriver(&out)
	done := make(chan struct{})
	var st api.DeployStatus
	go func() {
		st = reportCompletion(context.Background(), p, "job", "job", d, d.Progress(), nil, time.Millisecond, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reportCompletion did not return after the one-shot exited")
	}
	if len(st.Instances) != 1 || st.Instances[0].Running {
		t.Errorf("expected a terminated instance, got %+v", st)
	}
	if strings.Contains(out.String(), "gave up waiting") {
		t.Errorf("a normal exit must not report a running-state timeout; got:\n%s", out.String())
	}
	// The same completed status then satisfies the dependent's gate.
	if !dependencySatisfied(st, compose.DependsOnCompleted) {
		t.Errorf("completed status should satisfy the dependent's gate: %+v", st)
	}
}

func TestReportCompletionTimeout(t *testing.T) {
	// A service that never terminates hits the timeout, which is reported as a
	// completion (not running-state) give-up.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("job-0", "running", true))},
	}}
	var out bytes.Buffer
	d := testDriver(&out)
	reportCompletion(context.Background(), p, "job", "job", d, d.Progress(), nil, time.Millisecond, 20*time.Millisecond)
	if !strings.Contains(out.String(), "gave up waiting for completion") {
		t.Errorf("timeout path should report giving up on completion; got:\n%s", out.String())
	}
}

func TestCompletionServices(t *testing.T) {
	// web depends_on job (completed) and db (started): only job is a completion
	// service, and only while it is part of the selection.
	rt := &runtime{project: compose.NewProject(&compose.ProjectDocument{Services: map[string]compose.ServiceDocument{
		"web": {DependsOn: compose.DependsOn{
			{Service: "job", Condition: compose.DependsOnCompleted, Required: true},
			{Service: "db", Condition: compose.DependsOnStarted, Required: true},
		}},
		"job": {},
		"db":  {},
	}}).View(nil)}
	got := completionServices(rt, map[string]bool{"web": true, "job": true, "db": true})
	if !got["job"] || got["db"] || got["web"] {
		t.Errorf("completionServices = %v, want only {job}", got)
	}
	// An unselected completion dependency is excluded.
	if got2 := completionServices(rt, map[string]bool{"web": true, "db": true}); got2["job"] {
		t.Errorf("unselected completion dependency should be excluded: %v", got2)
	}
}

func TestWatchGoneClosesWhenAllGone(t *testing.T) {
	// Non-empty first, then empty: the channel must close once every named
	// deployment reports zero instances.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "running", true))},
		{st: status()}, // gone from here on
	}}
	ch := watchGone(context.Background(), p, []string{"web"}, time.Millisecond)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("watchGone did not close the channel after the deployment went away")
	}
}

func TestWatchGoneDoesNotCloseOnCancel(t *testing.T) {
	// Stays non-empty; cancelling ctx must stop the watcher WITHOUT closing the
	// channel (the reader relies on that to tell Ctrl-C from external removal).
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "running", true))},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	ch := watchGone(ctx, p, []string{"web"}, time.Millisecond)
	cancel()
	select {
	case <-ch:
		t.Fatal("watchGone closed the channel on ctx cancel; must only close when gone")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestReportReconcileEmptyThenRunning(t *testing.T) {
	// Backends report an empty instance set before any container exists; the
	// reporter must keep polling rather than treat empty as "all running".
	p := &scriptedPoller{steps: []pollStep{
		{st: status()},
		{st: status(inst("web-0", "running", true))},
	}}
	var out bytes.Buffer
	d := testDriver(&out)
	st := reportReconcile(context.Background(), p, "web", "web", d, d.Progress(), nil, time.Millisecond, 5*time.Second)
	if len(st.Instances) != 1 || !st.Instances[0].Running {
		t.Errorf("did not converge to running: %+v", st)
	}
	if !strings.Contains(out.String(), "web  web-0: running\n") {
		t.Errorf("missing running transition; got:\n%s", out.String())
	}
}

func TestReportReconcileRemovedWhileWaiting(t *testing.T) {
	// Seen-then-gone: an instance existed (pending), then the deployment was
	// removed (zero instances) by an external `down`. reportReconcile must stop
	// waiting AT ONCE — not block the full timeout — so a foreground `up` reaches
	// its watchGone self-exit deterministically. The timeout here is an hour: the
	// test proves the return is driven by the removal, not the clock.
	p := &scriptedPoller{steps: []pollStep{
		{st: status(inst("web-0", "pending", false))},
		{st: status()}, // removed out from under us: zero instances after being seen
	}}
	var out bytes.Buffer
	d := testDriver(&out)
	done := make(chan struct{})
	var st api.DeployStatus
	go func() {
		st = reportReconcile(context.Background(), p, "web", "web", d, d.Progress(), nil, time.Millisecond, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reportReconcile kept waiting after the deployment was removed")
	}
	if len(st.Instances) != 0 {
		t.Errorf("expected zero instances after removal, got %+v", st)
	}
	// Removal is a clean stop, not the timeout-failure path.
	if strings.Contains(out.String(), "gave up waiting") {
		t.Errorf("removal must not be reported as a timeout; got:\n%s", out.String())
	}
}
