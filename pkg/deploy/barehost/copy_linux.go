//go:build linux

package barehost

import (
	"context"
	"fmt"
	"io"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/containerdhost/tarcopy"
)

// runningInstance resolves a deployment's first instance and asserts it is
// running, returning its record and runtime state. Copy needs a RUNNING
// instance: a stopped instance's recorded pid may be dead or recycled by the
// kernel for an unrelated host process whose /proc/<pid>/root could be the host
// root, so the runtime state must report the instance running (mirrors
// containerdhost's requirement).
func (b *Backend) runningInstance(ctx context.Context, name string) (*instanceRecord, runtimeState, error) {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return nil, runtimeState{}, err
	}
	if len(recs) == 0 {
		return nil, runtimeState{}, fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	st, err := b.rt.State(ctx, recs[0].ID)
	if err != nil || st.Status != runcStateRunning || st.Pid == 0 {
		return nil, runtimeState{}, fmt.Errorf("bare: copy needs a running instance (%s)", recs[0].ID)
	}
	return recs[0], st, nil
}

// procRoot resolves the deployment's first instance to its rootfs as seen
// through the init's /proc/<pid>/root. Path resolution against it is confined by
// tarcopy (continuity fs.RootPath), so container symlinks cannot escape to the
// host. This is the cgroupfs-runtime path; sandboxed runtimes (gVisor) whose
// guest fs is not visible via /proc/<pid>/root use the exec-tar path in
// copy_exec_linux.go instead.
func (b *Backend) procRoot(ctx context.Context, name string) (string, error) {
	_, st, err := b.runningInstance(ctx, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/proc/%d/root", st.Pid), nil
}

// StatPath returns metadata for path inside the deployment's first instance
// (docker cp / archive HEAD).
func (b *Backend) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	if b.sandboxed {
		return b.statPathViaExec(ctx, name, path)
	}
	root, err := b.procRoot(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	return tarcopy.Stat(root, path)
}

// CopyFrom writes a tar of path from the deployment's first instance to w and
// returns the path's stat (docker cp from container / archive GET).
func (b *Backend) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	if b.sandboxed {
		return b.copyFromViaExec(ctx, name, path, w)
	}
	root, err := b.procRoot(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	return tarcopy.Pack(root, path, w)
}

// CopyTo extracts the tar read from r into path inside the deployment's first
// instance (docker cp into container / archive PUT).
func (b *Backend) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	if b.sandboxed {
		return b.copyToViaExec(ctx, name, path, r, opts)
	}
	root, err := b.procRoot(ctx, name)
	if err != nil {
		return err
	}
	return tarcopy.Unpack(root, path, r, tarcopy.UnpackOptions{
		NoOverwriteDirNonDir: opts.NoOverwriteDirNonDir,
		CopyUIDGID:           opts.CopyUIDGID,
	})
}
