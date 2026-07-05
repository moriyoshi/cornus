//go:build linux

package incushost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

var _ deploy.LateIDCredentialBinder = (*Backend)(nil)

// Credential FILE delivery on incus.
//
// The other host backends resolve the workload's id map from something that
// outlives any one workload, so the server owns the files correctly and Apply is
// an ordinary read-only bind. incus records the map on the INSTANCE, so there is
// nothing to ask until the instance exists — which is why file delivery was
// refused here at all (deploy.LateIDCredentialBinder says the rest).
//
// The window this uses was measured against a live daemon rather than read from
// documentation (2026-08-08, see JOURNAL):
//
//   - a created-but-never-started instance carries volatile.idmap.next, and it is
//     exactly the map volatile.idmap.current becomes at first start;
//   - a disk device can be attached to a stopped instance;
//   - a privileged instance carries "[]" in both keys, i.e. the identity.
//
// So: create stopped -> read the map -> chown -> start. The chown must precede the
// start, not merely the first read: these directories are bind-mounted, so fixing
// them afterwards would race a workload that may already have failed to read its
// credential.

// ownCredentialDirs corrects the ownership of the server-written credential
// directories to the ids this deployment's workload will actually run with.
//
// A no-op when there are no directories, which is every deploy that carries no
// file deliveries — including every Apply that did not come through
// ApplyOwningCredentialDirs.
func (b *Backend) ownCredentialDirs(ctx context.Context, spec api.DeploySpec, dirs []string, replicas int) error {
	if len(dirs) == 0 {
		return nil
	}
	uid, gid, ok := deploy.CredentialFileOwner(spec.User)
	if !ok {
		// A NAME lives in the image's /etc/passwd, which this backend never reads.
		// Guessing root is the silently-unreadable case the whole facility exists
		// to prevent, so refuse with the reason.
		return fmt.Errorf("incus: cannot deliver credential files to deployment %q: its user %q is a "+
			"name, and the numeric id it resolves to lives in the image's /etc/passwd, which cornus "+
			"does not read; use a numeric user (e.g. `user: 1000`)", spec.Name, spec.User)
	}
	hostUID, hostGID, err := b.credentialHostIDs(ctx, spec.Name, uid, gid, replicas)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := chownTree(dir, hostUID, hostGID, b.chown); err != nil {
			return fmt.Errorf("incus: owning credential directory %s for deployment %q: %w", dir, spec.Name, err)
		}
	}
	return nil
}

// lchown is the real ownership change. It is a variable on the backend so tests
// can observe WHICH ids land on WHICH paths without needing root.
//
// That is not a convenience: the first version of these tests called the syscall
// and skipped on an unprivileged host, so the two that cover the feature itself
// never ran and the package still reported ok. The syscall is os.Lchown and is
// not what can be wrong here; the ids and the paths are.
func lchown(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }

// credentialHostIDs resolves the host ids the credential files must carry, and
// refuses the case where the replicas do not agree.
//
// One host directory is bind-mounted into every replica, so it can carry exactly
// one ownership. That is fine on a default daemon, which hands every instance the
// same range — but `security.idmap.isolated=true` allocates a DISTINCT range per
// instance, and then no single ownership can satisfy them all. Delivering
// replica 0's ownership anyway would leave replicas 1..n-1 with a credential file
// they cannot read: the deploy succeeds, one replica works, the others fail on
// their own permission error much later. Refusing names the cause at deploy time.
func (b *Backend) credentialHostIDs(ctx context.Context, app string, uid, gid, replicas int) (int, int, error) {
	hostUID, hostGID, err := deploy.HostIDsFor(ctx, b, app, uid, gid)
	if err != nil {
		return 0, 0, err
	}
	for i := 1; i < replicas; i++ {
		in, err := b.conn.Instance(instanceName(app, i))
		if err != nil {
			return 0, 0, fmt.Errorf("incus: reading replica %d's id map: %w", i, err)
		}
		if in == nil {
			continue
		}
		m, err := parseIncusIDMap(instanceIDMapRaw(in.Config))
		if err != nil {
			return 0, 0, err
		}
		ru, uok := m.HostUID(uid)
		rg, gok := m.HostGID(gid)
		if !uok || !gok || ru != hostUID || rg != hostGID {
			return 0, 0, fmt.Errorf("incus: cannot deliver credential files to deployment %q: its "+
				"replicas were given DIFFERENT id ranges (replica 0 maps %d:%d to %d:%d, replica %d to "+
				"%d:%d), and one host directory is bind-mounted into all of them, so no single "+
				"ownership is readable by every replica — this is what security.idmap.isolated=true "+
				"does; deploy with a single replica or without isolated id maps",
				app, uid, gid, hostUID, hostGID, i, ru, rg)
		}
	}
	return hostUID, hostGID, nil
}

// chownTree owns dir and everything under it.
//
// The DIRECTORY matters as much as the files: a remapping runtime reaches this
// tree as an ordinary user, and the measured failure on rootless podman was
// `statfs` on the directory, before any file was opened.
func chownTree(dir string, uid, gid int, chown func(string, int, int) error) error {
	if chown == nil {
		chown = lchown
	}
	return filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return chown(path, uid, gid)
	})
}
