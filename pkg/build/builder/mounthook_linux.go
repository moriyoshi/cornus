//go:build linux

package builder

import (
	"context"

	"github.com/containerd/containerd/mount"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
)

// mountHookExecutor wraps an executor.Executor and rewrites a RUN step's mounts
// just before the container starts, then delegates. It is the seam that would
// let a RUN bind mount be backed by something other than a BuildKit snapshot
// (e.g. a lazy FUSE/9p mountpoint) without forking BuildKit: cornus installs
// it by overwriting wopt.Executor (a public WorkerOpt field) after
// runc.NewWorkerOpt and before base.NewWorker.
//
// SPIKE: this only proves the substitution mechanism. It runs at the executor,
// which is downstream of the exec op resolving its inputs — so by the time Run
// is called the original bind source is already a materialized snapshot. Making
// the transfer lazy additionally requires the bind input not to be a local
// source in the first place (see JOURNAL); that is a separate, earlier seam.
type mountHookExecutor struct {
	inner executor.Executor
	// rewrite returns a replacement mount and true to substitute it; false to
	// leave the mount untouched. nil means pass everything through unchanged.
	rewrite func(executor.Mount) (executor.Mount, bool)
}

func (e *mountHookExecutor) Run(ctx context.Context, id string, rootfs executor.Mount, mounts []executor.Mount, process executor.ProcessInfo, started chan<- struct{}) (resourcestypes.Recorder, error) {
	if e.rewrite != nil {
		out := make([]executor.Mount, len(mounts))
		for i, m := range mounts {
			if rep, ok := e.rewrite(m); ok {
				out[i] = rep
			} else {
				out[i] = m
			}
		}
		mounts = out
	}
	return e.inner.Run(ctx, id, rootfs, mounts, process, started)
}

func (e *mountHookExecutor) Exec(ctx context.Context, id string, process executor.ProcessInfo) error {
	return e.inner.Exec(ctx, id, process)
}

// hostBindMountable is an executor.Mountable that bind-mounts a fixed host
// directory into the container. It is the smallest possible custom mount source:
// a later lazy implementation would swap dir for a FUSE/9p mountpoint that the
// MountableRef ensures is mounted (and reference-counts) before returning.
type hostBindMountable struct {
	dir      string
	readonly bool
}

func (m hostBindMountable) Mount(_ context.Context, readonly bool) (executor.MountableRef, error) {
	return hostBindRef{dir: m.dir, readonly: readonly || m.readonly}, nil
}

type hostBindRef struct {
	dir      string
	readonly bool
}

func (r hostBindRef) Mount() ([]mount.Mount, func() error, error) {
	opts := []string{"rbind"}
	if r.readonly {
		opts = append(opts, "ro")
	}
	return []mount.Mount{{
		Type:    "bind",
		Source:  r.dir,
		Options: opts,
	}}, func() error { return nil }, nil
}

func (r hostBindRef) IdentityMapping() *idtools.IdentityMapping { return nil }
