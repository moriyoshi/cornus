//go:build linux

package containerdhost

import (
	"context"
	"io"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/fsop"
)

// Serving structured filesystem operations without a caretaker.
//
// The kubernetes backend implements this through a caretaker sidecar, because a
// pod is the only place its bytes are reachable. A co-located host backend has a
// shorter route: the container's whole rootfs is visible through its init
// process at /proc/<pid>/root, which is how CopyTo/CopyFrom already work here.
//
// What that buys is not speed on a stat. It is that a rename or a copy WITHIN a
// workload becomes one in-place operation instead of a full byte relay out to
// the caller and back — see cmd/cornus/internal/webbff/fsplan.go, whose execServer
// plan exists for exactly this and had no backend to use it on outside kubernetes.
var _ deploy.FSOperator = (*Backend)(nil)

// FSOp implements deploy.FSOperator.
//
// The single root is Target "/" over the container's rootfs, so a request path is
// used as-is — unlike the caretaker, which maps each mounted volume from the path
// the app names to the path it can see. Confinement is tarcopy's (continuity
// fs.RootPath), the same as every other read of this rootfs, so a symlink inside
// the container cannot walk out to the host.
//
// procRoot requires a RUNNING instance and says so. That is not incidental
// strictness: a stopped task's recorded pid can be recycled by the kernel, and
// /proc/<pid>/root for an unrelated host process is the HOST root — which would
// hand this operator the machine instead of the container.
func (b *Backend) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	root, err := b.procRoot(ctx, name)
	if err != nil {
		// Not an error to the caller: an unsupported answer is the one it can act
		// on, by relaying the operation itself instead of failing the user's copy.
		return api.FSOpUnsupported("containerd: " + err.Error()), nil
	}
	return fsop.Serve(req, []fsop.Root{{Target: "/", Path: root}}, body, out)
}
