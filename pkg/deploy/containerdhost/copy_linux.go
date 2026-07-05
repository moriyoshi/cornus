//go:build linux

package containerdhost

import (
	"context"
	"fmt"
	"io"

	ctd "github.com/containerd/containerd"

	"cornus/pkg/api"
	"cornus/pkg/deploy/containerdhost/tarcopy"
)

// procRoot resolves the deployment's first instance to its rootfs as seen
// through the task's /proc/<pid>/root. Path resolution against it is confined
// by tarcopy (continuity fs.RootPath), so container symlinks cannot escape to
// the host. Copy operations therefore need a running task; stopped instances
// are not supported (matching the kubernetes backend's running-pod
// requirement).
func (b *Backend) procRoot(ctx context.Context, name string) (string, error) {
	c, err := b.firstInstance(ctx, name)
	if err != nil {
		return "", err
	}
	nctx := b.ns(ctx)
	task, err := runningTask(nctx, c)
	if err != nil {
		return "", fmt.Errorf("containerd: copy needs a running instance: %w", err)
	}
	// runningTask only proves a task record exists, not that its init is live.
	// A stopped-but-undeleted task's recorded Pid may have been recycled by the
	// kernel for an unrelated host process, whose /proc/<pid>/root could be the
	// host root ('/'), letting tarcopy escape the container. Require Running.
	status, err := task.Status(nctx)
	if err != nil {
		return "", fmt.Errorf("containerd: copy needs a running instance: %w", err)
	}
	if status.Status != ctd.Running {
		return "", fmt.Errorf("containerd: instance %s is not running", c.ID())
	}
	return fmt.Sprintf("/proc/%d/root", task.Pid()), nil
}

// StatPath returns metadata for path inside the deployment's first instance
// (docker cp / archive HEAD).
func (b *Backend) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	root, err := b.procRoot(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	return tarcopy.Stat(root, path)
}

// CopyFrom writes a tar of path from the deployment's first instance to w and
// returns the path's stat (docker cp from container / archive GET).
func (b *Backend) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	root, err := b.procRoot(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	return tarcopy.Pack(root, path, w)
}

// CopyTo extracts the tar read from r into path inside the deployment's first
// instance (docker cp into container / archive PUT).
func (b *Backend) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	root, err := b.procRoot(ctx, name)
	if err != nil {
		return err
	}
	return tarcopy.Unpack(root, path, r, tarcopy.UnpackOptions{
		NoOverwriteDirNonDir: opts.NoOverwriteDirNonDir,
		CopyUIDGID:           opts.CopyUIDGID,
	})
}
