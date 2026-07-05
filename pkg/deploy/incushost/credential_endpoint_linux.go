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

// BindsCredentialDir implements deploy.CredentialBinder, and now answers TRUE.
//
// It answered false for an ORDERING reason rather than a mapping one: incus
// records the id map on the INSTANCE, so there was nothing to ask at the moment
// the server had to write the files. That is still true — what changed is that
// the backend no longer needs the answer then. Apply creates instances stopped,
// takes ownership of the server's directories once their map exists, and starts
// them afterwards (credential_file_linux.go, deploy.LateIDCredentialBinder).
//
// The window was measured against a live daemon rather than reasoned about
// (2026-08-08): a created-but-never-started instance carries
// volatile.idmap.next, and it is exactly the map volatile.idmap.current becomes
// at first start. The earlier note here said the daemon exposes no id-map base
// beforehand; that was right about GET /1.0 and the default profile, and wrong
// about the instance.
//
// One case is refused rather than delivered wrong: replicas given DIFFERENT id
// ranges (security.idmap.isolated=true) cannot share one host directory, since it
// carries a single ownership. See credentialHostIDs.
func (b *Backend) BindsCredentialDir(ctx context.Context) bool { return true }
