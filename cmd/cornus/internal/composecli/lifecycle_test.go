package composecli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/devcontainer"
)

// fakeConn is a net.Conn whose reads drain a fixed payload then EOF; writes are
// discarded. Enough to drive runExec's io.Copy.
type fakeConn struct{ io.Reader }

func (f *fakeConn) Write(p []byte) (int, error)        { return len(p), nil }
func (f *fakeConn) Close() error                       { return nil }
func (f *fakeConn) LocalAddr() net.Addr                { return nil }
func (f *fakeConn) RemoteAddr() net.Addr               { return nil }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// fakeRunner records exec calls and returns per-command exit codes.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []api.ExecConfig
	inspect map[string]int
	id      int
	exitFor func(cmd []string) int
}

func newFakeRunner() *fakeRunner { return &fakeRunner{inspect: map[string]int{}} }

func (f *fakeRunner) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cfg)
	f.id++
	id := fmt.Sprintf("e%d", f.id)
	code := 0
	if f.exitFor != nil {
		code = f.exitFor(cfg.Cmd)
	}
	f.inspect[id] = code
	return id, nil
}

func (f *fakeRunner) ExecStart(ctx context.Context, id string, cfg api.ExecStartConfig) (net.Conn, error) {
	return &fakeConn{Reader: strings.NewReader("out:" + id + "\n")}, nil
}

func (f *fakeRunner) ExecInspect(ctx context.Context, id string) (api.ExecState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return api.ExecState{ExitCode: f.inspect[id]}, nil
}

func shell(s string) *devcontainer.LifecycleCommand {
	return &devcontainer.LifecycleCommand{Commands: [][]string{{"/bin/sh", "-c", s}}}
}

func TestRunContainerHooksOrderAndConfig(t *testing.T) {
	f := newFakeRunner()
	h := &devcontainer.Hooks{
		User:       "vscode",
		WorkDir:    "/work",
		OnCreate:   shell("a"),
		PostCreate: shell("b"),
		PostStart:  shell("c"),
	}
	var out bytes.Buffer
	if err := runContainerHooks(context.Background(), f, "proj-devcontainer", h, &out); err != nil {
		t.Fatalf("runContainerHooks: %v", err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("expected 3 execs, got %d", len(f.calls))
	}
	wantOrder := []string{"a", "b", "c"}
	for i, c := range f.calls {
		if got := c.Cmd[len(c.Cmd)-1]; got != wantOrder[i] {
			t.Errorf("call %d ran %q, want %q (order broken)", i, got, wantOrder[i])
		}
		if c.User != "vscode" || c.WorkingDir != "/work" {
			t.Errorf("call %d user/workdir = %q/%q", i, c.User, c.WorkingDir)
		}
		if !c.AttachStdout || !c.AttachStderr {
			t.Errorf("call %d should attach stdout+stderr", i)
		}
	}
	if !strings.Contains(out.String(), "out:e1") {
		t.Errorf("output not streamed: %q", out.String())
	}
}

func TestRunContainerHooksAbortsOnNonZeroExit(t *testing.T) {
	f := newFakeRunner()
	f.exitFor = func(cmd []string) int {
		if cmd[len(cmd)-1] == "fail" {
			return 7
		}
		return 0
	}
	h := &devcontainer.Hooks{
		OnCreate:   shell("fail"),
		PostCreate: shell("never"),
	}
	err := runContainerHooks(context.Background(), f, "res", h, io.Discard)
	if err == nil {
		t.Fatal("expected an error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "code 7") {
		t.Errorf("error should carry the exit code: %v", err)
	}
	// The failing phase must stop the sequence before postCreate runs.
	if len(f.calls) != 1 {
		t.Errorf("expected 1 exec before abort, got %d", len(f.calls))
	}
}

func TestRunContainerHooksParallelObjectForm(t *testing.T) {
	f := newFakeRunner()
	h := &devcontainer.Hooks{
		PostCreate: &devcontainer.LifecycleCommand{Commands: [][]string{
			{"echo", "x"}, {"echo", "y"},
		}},
	}
	if err := runContainerHooks(context.Background(), f, "res", h, io.Discard); err != nil {
		t.Fatalf("runContainerHooks: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 execs, got %d", len(f.calls))
	}
}

// TestRunStartHooksOnlyPerStart checks that runStartHooks (used by
// start/restart) runs ONLY postStart -> postAttach, skipping the once-per-create
// hooks (onCreate/updateContent/postCreate).
func TestRunStartHooksOnlyPerStart(t *testing.T) {
	f := newFakeRunner()
	h := &devcontainer.Hooks{
		User:          "vscode",
		WorkDir:       "/work",
		OnCreate:      shell("create"),
		UpdateContent: shell("update"),
		PostCreate:    shell("postcreate"),
		PostStart:     shell("start"),
		PostAttach:    shell("attach"),
	}
	var out bytes.Buffer
	if err := runStartHooks(context.Background(), f, "proj-devcontainer", h, &out); err != nil {
		t.Fatalf("runStartHooks: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 execs (postStart, postAttach), got %d", len(f.calls))
	}
	wantOrder := []string{"start", "attach"}
	for i, c := range f.calls {
		if got := c.Cmd[len(c.Cmd)-1]; got != wantOrder[i] {
			t.Errorf("call %d ran %q, want %q", i, got, wantOrder[i])
		}
		if c.User != "vscode" || c.WorkingDir != "/work" {
			t.Errorf("call %d user/workdir = %q/%q", i, c.User, c.WorkingDir)
		}
	}
}

// TestRunStartHooksNilAndEmpty checks that runStartHooks is a no-op when there
// are no hooks (plain Compose) or no per-start hooks declared.
func TestRunStartHooksNilAndEmpty(t *testing.T) {
	f := newFakeRunner()
	if err := runStartHooks(context.Background(), f, "res", nil, io.Discard); err != nil {
		t.Fatalf("nil hooks: %v", err)
	}
	// Only create-time hooks: runStartHooks must run nothing.
	h := &devcontainer.Hooks{OnCreate: shell("create"), PostCreate: shell("pc")}
	if err := runStartHooks(context.Background(), f, "res", h, io.Discard); err != nil {
		t.Fatalf("create-only hooks: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no execs, got %d", len(f.calls))
	}
}

// TestWaitRunningThenHooksGatesOnRunning is the regression guard for the kube
// devcontainer race: a mounted deploy-attach hold signals ready as soon as the
// Deployment object exists (before any pod is scheduled), so the container
// lifecycle exec must not fire until the workload reports running — otherwise the
// server-side exec resolves the pod before the scheduler creates it and fails with
// "no pods for deployment ...".
func TestWaitRunningThenHooksGatesOnRunning(t *testing.T) {
	poll := &scriptedPoller{steps: []pollStep{
		{st: status()}, // objects created, no pod yet
		{st: status(inst("dc-0", "running", true))}, // pod scheduled and running
	}}
	f := newFakeRunner()
	f.exitFor = func(cmd []string) int {
		// ExecCreate must only fire after the poller has observed the running
		// state (the initial empty read plus the running read = at least two
		// Status calls). A lower count means the hook ran before the pod existed.
		if n := poll.count(); n < 2 {
			t.Errorf("hook exec fired after only %d Status polls; must wait for running", n)
		}
		return 0
	}
	h := &devcontainer.Hooks{PostCreate: shell("marker")}
	var out bytes.Buffer
	d := testDriver(&out)
	err := waitRunningThenHooks(context.Background(), poll, f, "dc", "dc-devcontainer",
		"dc-devcontainer", h, d, d.Progress(), nil, &out, time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("waitRunningThenHooks: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected exactly one hook exec, got %d", len(f.calls))
	}
}

// TestWaitRunningThenHooksNoHooks confirms a plain mounted Compose service (no
// devcontainer hooks) neither polls status nor execs — the gate is a no-op.
func TestWaitRunningThenHooksNoHooks(t *testing.T) {
	poll := &scriptedPoller{steps: []pollStep{{st: status()}}}
	f := newFakeRunner()
	var out bytes.Buffer
	d := testDriver(&out)
	if err := waitRunningThenHooks(context.Background(), poll, f, "web", "web", "web", nil,
		d, d.Progress(), nil, &out, time.Millisecond, time.Second); err != nil {
		t.Fatalf("waitRunningThenHooks nil hooks: %v", err)
	}
	if poll.count() != 0 {
		t.Errorf("nil hooks must not poll status, polled %d times", poll.count())
	}
	if len(f.calls) != 0 {
		t.Errorf("nil hooks must not exec, ran %d", len(f.calls))
	}
}

// The real client must satisfy the execRunner seam the lifecycle depends on.
var _ execRunner = (*client.Client)(nil)
