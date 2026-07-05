//go:build linux

package barehost

import (
	"context"
	"fmt"
	"sync"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// fakeRuntime is an in-memory containerRuntime for tests: it records the verbs
// invoked and simulates a container's state machine without a real runtime
// binary, root, or a rootfs. It mirrors the seam containerdhost's
// fake_linux_test.go provides over its clientAPI.
type fakeRuntime struct {
	mu    sync.Mutex
	cs    map[string]*runtimeState
	calls []string
	// startErr / createErr / execErr, when set, make the corresponding verb fail
	// so tests can exercise error paths.
	createErr error
	startErr  error
	execErr   error
	// stats / statsErr back the runtime-native Stats source (sandboxed runtimes).
	stats    runtimeStats
	statsErr error
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{cs: map[string]*runtimeState{}} }

func (f *fakeRuntime) record(v string) { f.calls = append(f.calls, v) }

func (f *fakeRuntime) Create(ctx context.Context, id, bundle string, opts createOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("create:" + id)
	if f.createErr != nil {
		return f.createErr
	}
	if _, ok := f.cs[id]; ok {
		return fmt.Errorf("bare-fake: container %q already exists", id)
	}
	f.cs[id] = &runtimeState{ID: id, Status: runcStateCreated, Bundle: bundle}
	return nil
}

func (f *fakeRuntime) Start(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("start:" + id)
	if f.startErr != nil {
		return f.startErr
	}
	c, ok := f.cs[id]
	if !ok {
		return fmt.Errorf("bare-fake: container %q does not exist", id)
	}
	c.Status = runcStateRunning
	c.Pid = 1000 + len(f.calls)
	return nil
}

func (f *fakeRuntime) State(ctx context.Context, id string) (runtimeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cs[id]
	if !ok {
		return runtimeState{}, fmt.Errorf("bare-fake: container %q does not exist", id)
	}
	return *c, nil
}

func (f *fakeRuntime) Kill(ctx context.Context, id string, sig int, all bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("kill:%s:%d", id, sig))
	c, ok := f.cs[id]
	if !ok {
		return fmt.Errorf("bare-fake: container %q does not exist", id)
	}
	c.Status = runcStateStopped
	return nil
}

func (f *fakeRuntime) Delete(ctx context.Context, id string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("delete:" + id)
	delete(f.cs, id)
	return nil
}

func (f *fakeRuntime) List(ctx context.Context) ([]runtimeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]runtimeState, 0, len(f.cs))
	for _, c := range f.cs {
		out = append(out, *c)
	}
	return out, nil
}

func (f *fakeRuntime) Exec(ctx context.Context, id string, process specs.Process, opts runtimeExecOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("exec:" + id)
	return f.execErr
}

func (f *fakeRuntime) Stats(ctx context.Context, id string) (runtimeStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("stats:" + id)
	if f.statsErr != nil {
		return runtimeStats{}, f.statsErr
	}
	return f.stats, nil
}

var _ containerRuntime = (*fakeRuntime)(nil)
