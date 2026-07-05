package dockerhost

import (
	"context"
	"fmt"
	"io"
	"os"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/fsop"
)

var _ deploy.FSOperator = (*Backend)(nil)

// FSOp implements deploy.FSOperator over the container's rootfs at
// /proc/<pid>/root.
//
// This backend normally reaches container files through the daemon's archive
// API, which serves get and put and nothing else. The structured interface has
// eight operations, and the two worth having — rename and copy — are precisely
// the ones the archive API cannot express: through it, moving a file inside a
// container means pulling every byte out and pushing it back. Reading the rootfs
// directly is what makes them one in-place call.
//
// Three configurations are refused rather than attempted, all for the same
// reason in different clothes: this process cannot see what the daemon sees.
//
//   - a NON-LOCAL endpoint (DOCKER_HOST=tcp://, CORNUS_PODMAN_SOCKET=ssh://) or
//     remote mode — the pid belongs to another machine's process table;
//   - ROOTLESS podman — the container's init belongs to another user, and
//     /proc/<pid>/root is not readable;
//   - an unprivileged server against a root-owned container, which fails the
//     same way and is caught by the open below rather than predicted.
//
// Every refusal is FSErrUnsupported with a nil error, which is the contract: the
// caller relays the operation itself, so the Files screen keeps working and only
// loses the fast path.
func (b *Backend) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	if b.remote || b.api.nonLocal() {
		return api.FSOpUnsupported(b.tag() +
			": the daemon may be on another machine, so its container pids name nothing here"), nil
	}
	if b.rootless(ctx) {
		return api.FSOpUnsupported(b.tag() +
			": this daemon is rootless, so the container's filesystem belongs to another user and is not readable here"), nil
	}
	root, err := b.instanceProcRoot(ctx, name)
	if err != nil {
		return api.FSOpUnsupported(b.tag() + ": " + err.Error()), nil
	}
	return fsop.Serve(req, []fsop.Root{{Target: "/", Path: root}}, body, out)
}

// instanceProcRoot resolves the deployment's first instance to its rootfs as seen
// through the init process.
//
// Running and Pid come from ONE inspect, so the state cannot have been read from
// a different moment than the pid it belongs to. That still leaves the window
// every /proc-based container tool has, which is why the check below exists.
func (b *Backend) instanceProcRoot(ctx context.Context, name string) (string, error) {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return "", err
	}
	st, err := b.api.containerInspect(ctx, id)
	if err != nil {
		return "", err
	}
	if !st.Running || st.Pid == 0 {
		return "", fmt.Errorf("instance %s is not running", id)
	}
	root := fmt.Sprintf("/proc/%d/root", st.Pid)
	if err := refuseHostRoot(root); err != nil {
		return "", err
	}
	return root, nil
}

// refuseHostRoot rejects a rootfs path that is this machine's own root.
//
// A stopped container frees its pid, and the kernel recycles it. If an unrelated
// HOST process picks it up, /proc/<pid>/root is "/" — and every operation here
// would then be served against the whole machine while looking perfectly normal.
// A container's root is a different directory by construction, so comparing the
// two removes the outcome that matters. It does not prove the pid still belongs
// to the RIGHT container, which is the residual window /proc-based tooling has
// generally; it does mean the worst case is answering about another container
// rather than about the host.
func refuseHostRoot(root string) error {
	target, err := os.Stat(root)
	if err != nil {
		return err
	}
	self, err := os.Stat("/")
	if err != nil {
		return err
	}
	if os.SameFile(target, self) {
		return fmt.Errorf("%s is this host's own root, not a container filesystem "+
			"(the container is gone and its pid was recycled)", root)
	}
	return nil
}
