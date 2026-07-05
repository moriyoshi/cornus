//go:build linux

package builder

import (
	"context"
	"testing"

	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
)

// captureExecutor is a fake executor.Executor that records the mounts it was
// asked to run, standing in for the real runc executor.
type captureExecutor struct {
	gotMounts []executor.Mount
}

func (c *captureExecutor) Run(_ context.Context, _ string, _ executor.Mount, mounts []executor.Mount, _ executor.ProcessInfo, started chan<- struct{}) (resourcestypes.Recorder, error) {
	c.gotMounts = mounts
	if started != nil {
		close(started)
	}
	return nil, nil
}

func (c *captureExecutor) Exec(context.Context, string, executor.ProcessInfo) error { return nil }

// TestMountHookSubstitutesBindSource proves the executor seam: a wrapper can
// swap a RUN mount's source for a host-backed bind before the container starts,
// without forking BuildKit. Verifies (1) the wrapper satisfies the interface,
// (2) the marked mount is substituted while others pass through, and (3) the
// substituted source yields a containerd bind mount to our directory.
func TestMountHookSubstitutesBindSource(t *testing.T) {
	inner := &captureExecutor{}
	const target = "/cornus-spike"

	var _ executor.Executor = &mountHookExecutor{} // interface satisfaction

	hook := &mountHookExecutor{
		inner: inner,
		rewrite: func(m executor.Mount) (executor.Mount, bool) {
			if m.Dest != target {
				return m, false
			}
			m.Src = hostBindMountable{dir: "/host/data", readonly: true}
			return m, true
		},
	}

	in := []executor.Mount{
		{Dest: "/other", Src: nil},
		{Dest: target, Src: nil, Readonly: true},
	}
	if _, err := hook.Run(context.Background(), "id", executor.Mount{}, in, executor.ProcessInfo{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(inner.gotMounts) != 2 {
		t.Fatalf("inner saw %d mounts, want 2", len(inner.gotMounts))
	}
	if inner.gotMounts[0].Src != nil {
		t.Errorf("unmarked mount was rewritten: %+v", inner.gotMounts[0])
	}
	sub := inner.gotMounts[1].Src
	if sub == nil {
		t.Fatal("marked mount was not substituted")
	}

	// The substituted source resolves to a containerd bind mount to our dir.
	ref, err := sub.Mount(context.Background(), true)
	if err != nil {
		t.Fatalf("substituted Mount: %v", err)
	}
	ms, release, err := ref.Mount()
	if err != nil {
		t.Fatalf("ref.Mount: %v", err)
	}
	defer release()
	if len(ms) != 1 || ms[0].Type != "bind" || ms[0].Source != "/host/data" {
		t.Fatalf("unexpected mounts: %+v", ms)
	}
	var ro bool
	for _, o := range ms[0].Options {
		if o == "ro" {
			ro = true
		}
	}
	if !ro {
		t.Errorf("expected read-only bind, options = %v", ms[0].Options)
	}
}
