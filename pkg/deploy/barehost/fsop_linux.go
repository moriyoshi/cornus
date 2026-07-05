//go:build linux

package barehost

import (
	"context"
	"io"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/fsop"
)

var _ deploy.FSOperator = (*Backend)(nil)

// FSOp implements deploy.FSOperator over the container's rootfs, reached through
// the init process at /proc/<pid>/root — the same route CopyTo/CopyFrom take on
// this backend.
//
// SANDBOXED RUNTIMES ARE REFUSED, and that refusal is the whole reason this is
// not three lines. Under gVisor the guest filesystem is served by the
// sentry/gofer and is NOT visible at /proc/<pid>/root (copy_exec_linux.go says
// so, and copy falls back to running tar inside the container for exactly this).
// Reading that path anyway would not fail — it would silently read the SANDBOX
// PROCESS's own root and answer confidently about the wrong filesystem.
//
// The exec fallback copy uses is deliberately not extended here. It serves two
// operations by streaming a tar; this interface has eight, including rename and
// copy whose entire value is being in-place. Relaying is the honest answer, and
// FSErrUnsupported is how the caller is told to do it — the Files screen then
// works exactly as it does today, just without the fast path.
func (b *Backend) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	if b.sandboxed {
		return api.FSOpUnsupported(
			"bare: this runtime sandboxes the container filesystem, so it is not visible at " +
				"/proc/<pid>/root and a structured operation would answer about the wrong tree"), nil
	}
	root, err := b.procRoot(ctx, name)
	if err != nil {
		// Unsupported rather than an error: the caller relays instead of failing.
		return api.FSOpUnsupported("bare: " + err.Error()), nil
	}
	return fsop.Serve(req, []fsop.Root{{Target: "/", Path: root}}, body, out)
}
