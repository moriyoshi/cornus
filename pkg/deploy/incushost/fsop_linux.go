//go:build linux

package incushost

import (
	"context"
	"io"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/fsop"
)

// Structured filesystem operations on incus, served over the daemon's own file
// channel.
//
// The other host backends reach a workload's bytes through a LOCAL path — the
// container's rootfs via its init process's /proc/<pid>/root — and hand it to
// fsop.LocalFS. This backend has no such path to offer, and the two obvious
// substitutes are both wrong:
//
//   - The instance's rootfs under /var/lib/incus exists only when cornus is
//     co-located on a `dir` storage pool, and reports ID-SHIFTED ownership (a
//     uid-1000 file appears as 1001000), which is the wrong answer to show a file
//     browser.
//   - The instance FILE API, which cp already uses, has no RENAME — close to the
//     reason FSOperator exists at all, since the archive trio can already stat and
//     copy but cannot move anything.
//
// The SFTP channel has every primitive, measured against a live daemon before
// this was written (2026-08-08; see JOURNAL). It needs nothing in the image: an
// OCI application container ships no sshd and a distroless one has no shell, and
// incusd serves the channel itself.
var _ deploy.FSOperator = (*Backend)(nil)

// FSOp implements deploy.FSOperator.
//
// One root, Target "/" over the instance's own filesystem, so a request path is
// used as-is — the same shape the co-located host backends use, and unlike the
// caretaker, which maps each mounted volume from the path the app names to the
// path it can see.
//
// Confinement is the CHANNEL: it addresses exactly one instance and the daemon
// holds that boundary, so a symlink inside the instance cannot reach the host the
// way one under /proc/<pid>/root could. What fsop.SFTPFS still enforces is that a
// request path cannot climb above the root by spelling.
func (b *Backend) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	// Replica 0, matching every other per-instance read on this backend (logs,
	// stats, exec default). A file browser addresses a deployment, not a replica.
	inst := instanceName(name, 0)
	client, err := b.conn.SFTP(inst)
	if err != nil {
		// NOT an error to the caller: unsupported is the answer it can act on, by
		// relaying the operation itself instead of failing the user's copy. A
		// stopped instance lands here, which is right — there is nothing to serve.
		return api.FSOpUnsupported("incus: no file channel to " + inst + ": " + err.Error()), nil
	}
	defer client.Close()
	return fsop.ServeFS(req, []fsop.Root{{Target: "/", Path: "/"}}, fsop.SFTPFS{Client: client}, body, out)
}
