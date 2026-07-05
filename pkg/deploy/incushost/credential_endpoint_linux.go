//go:build linux

package incushost

import (
	"context"
	"fmt"
	"os"

	"cornus/pkg/deploy"
)

// The server refuses endpoint deliveries outright if this assertion ever stops
// holding, silently, so it is worth failing the build instead.
var (
	_ deploy.CredentialEndpointBinder = (*Backend)(nil)
	_ deploy.CredentialBinder         = (*Backend)(nil)
)

// BindsCredentialEndpoints implements deploy.CredentialEndpointBinder.
//
// This is the one caretaker role incus can host without a caretaker, and the
// reason is worth stating because the companion doc in companion_linux.go says
// the opposite about its neighbours. Mounts and egress are not wired here
// because an incus companion is a SIBLING INSTANCE and Incus exposes no way for
// one instance to join another's network namespace — so the companion cannot
// propagate a mount into the app, and cannot capture its traffic.
//
// Neither limit applies to the SERVER. It does not need to join the namespace
// from inside Incus; it enters it from the host through the instance's init
// process, exactly as the dockerhost backend does. The sibling-instance
// constraint is a fact about companions, not about the namespace.
//
// False in remote mode, where the instance's pid is a pid on the machine running
// incusd and means nothing in this process's /proc.
func (b *Backend) BindsCredentialEndpoints(ctx context.Context) bool { return !b.remote }

// InstanceNetns implements deploy.CredentialEndpointBinder.
//
// Incus reports the instance's init pid in its state, so the handle is
// /proc/<pid>/ns/net — the same shape dockerhost uses, and it inherits the same
// two hazards. The pid comes from the DAEMON, so it is meaningless in a
// containerized cornus that does not share incusd's pid namespace; and a pid
// freed by an instance that stopped can be reused by an unrelated process. Both
// present as a valid-looking path naming the wrong namespace, which would bind a
// live credential endpoint somewhere it was never meant to be, so the namespace
// is checked to differ from this server's own before it is returned.
func (b *Backend) InstanceNetns(ctx context.Context, name string, replica int) (string, error) {
	id := instanceName(name, replica)
	st, err := b.conn.InstanceState(id)
	if err != nil {
		return "", fmt.Errorf("incus: instance state for %s: %w", id, err)
	}
	if st == nil {
		return "", fmt.Errorf("incus: instance %s not found", id)
	}
	if st.Pid <= 0 {
		// Ordinary between create and start, and the reason the caller retries.
		return "", fmt.Errorf("incus: instance %s is not running yet", id)
	}
	nsPath := fmt.Sprintf("/proc/%d/ns/net", st.Pid)
	same, err := sameNetnsAsSelf(nsPath)
	if err != nil {
		return "", fmt.Errorf("incus: instance %s network namespace at %s is unreadable: %w "+
			"(either this server lacks the privilege to read a root-owned instance's namespace, or it is "+
			"containerized and does not share incusd's pid namespace)", id, nsPath, err)
	}
	if same {
		return "", fmt.Errorf("incus: instance %s resolves to THIS server's own network namespace via %s; "+
			"refusing to bind a credential endpoint there, since it would be reachable by the whole host", id, nsPath)
	}
	return nsPath, nil
}

// sameNetnsAsSelf reports whether nsPath names the same network namespace as
// this process's own, by the identity of the inode behind each procfs link.
func sameNetnsAsSelf(nsPath string) (bool, error) {
	target, err := os.Stat(nsPath)
	if err != nil {
		return false, err
	}
	self, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		return false, err
	}
	return os.SameFile(target, self), nil
}

// BindsCredentialDir implements deploy.CredentialBinder, and answers FALSE — on
// an ordering problem, not on the id mapping that used to be the reason.
//
// The mapping itself is solved: IDMap reads volatile.idmap.current and the
// server owns the file as the ids the workload runs with (measured: chowning a
// host directory into the instance's range makes it readable and writable
// inside, with no raw.idmap and no isolation cost).
//
// What blocks it is WHEN that map exists. incus records it on the INSTANCE, and
// only once the instance has been created — but a credential file has to be
// written before then, because it reaches the workload as a disk device in the
// create request. Measured: the daemon exposes no id-map base of its own
// (neither GET /1.0 nor the default profile carries one), so there is nothing to
// ask before the instance exists.
//
// The remedy is a different shape rather than a different lookup: incus can
// attach a disk device to a STOPPED instance, so create -> read the map -> write
// the files -> attach -> start would close it. That is a restructure of this
// backend's Apply, not a capability flag, so it is left for its own change.
//
// env and endpoint deliveries are unaffected and work here.
func (b *Backend) BindsCredentialDir(ctx context.Context) bool { return false }
