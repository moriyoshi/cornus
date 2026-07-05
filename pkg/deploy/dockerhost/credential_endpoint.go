package dockerhost

import (
	"context"
	"fmt"
	"os"

	"cornus/pkg/deploy"
)

// The server refuses endpoint deliveries outright if this assertion ever stops
// holding, silently, so it is worth failing the build instead.
var _ deploy.CredentialEndpointBinder = (*Backend)(nil)

// BindsCredentialEndpoints implements deploy.CredentialEndpointBinder.
//
// False in remote mode, and false whenever the endpoint may name a daemon on
// another machine. A remote daemon's container pids are pids on ANOTHER machine,
// and /proc/<pid> here would name either nothing or — far worse — an unrelated
// LOCAL process that happens to hold that number, whose namespace is a perfectly
// valid namespace belonging to something else entirely.
//
// nonLocal is checked separately from Remote because they are different
// questions and only one of them is a mode the operator chose. Remote() reports
// CORNUS_DOCKER_REMOTE / CORNUS_PODMAN_REMOTE; nonLocal reports what the SOCKET
// says — DOCKER_HOST=tcp://... or CORNUS_PODMAN_SOCKET=ssh://... reach another
// machine without anyone setting a mode, and would otherwise pass a !Remote()
// test while nothing they name exists here.
func (b *Backend) BindsCredentialEndpoints(ctx context.Context) bool {
	return !b.remote && !b.api.nonLocal()
}

// InstanceNetns implements deploy.CredentialEndpointBinder.
//
// Unlike containerd and bare, this backend does not own the namespace: Docker
// creates it when the container STARTS, and the only handle on it is the init
// process, via /proc/<pid>/ns/net. Two consequences follow, and both are checked
// rather than assumed.
//
// First, the pid comes from the daemon, so it is a pid in the DAEMON's pid
// namespace. A cornus server running in a container with its own pid namespace
// resolves that number against a different process table, where it names some
// unrelated process or nothing at all.
//
// Second, even sharing a pid namespace, a container that exited between the
// inspect and the open leaves its pid free to be reused.
//
// Both failures have the same shape — a valid-looking path naming the wrong
// namespace — and the same consequence, which is a live credential endpoint
// bound somewhere it was never meant to be. So the namespace is verified to be
// something other than this server's own before it is returned. That does not
// prove it is the RIGHT container's namespace, but it removes the outcome that
// matters: publishing the credential into the server's own namespace, and from
// there to the host.
func (b *Backend) InstanceNetns(ctx context.Context, name string, replica int) (string, error) {
	id, err := b.instanceID(ctx, name, replica)
	if err != nil {
		return "", err
	}
	st, err := b.api.containerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("dockerhost: inspect %s: %w", id, err)
	}
	if !st.Running || st.Pid == 0 {
		// Not an error worth giving up on: this is the ordinary state between a
		// create and a start, and the whole reason the caller retries.
		return "", fmt.Errorf("dockerhost: instance %s is not running yet", id)
	}
	nsPath := fmt.Sprintf("/proc/%d/ns/net", st.Pid)
	same, err := sameNetnsAsSelf(nsPath)
	if err != nil {
		return "", fmt.Errorf("dockerhost: instance %s network namespace at %s is unreadable: %w "+
			"(either this server lacks the privilege to read a root-owned container's namespace, or it is "+
			"containerized and does not share the daemon's pid namespace)", id, nsPath, err)
	}
	if same {
		return "", fmt.Errorf("dockerhost: instance %s resolves to THIS server's own network namespace via %s; "+
			"refusing to bind a credential endpoint there, since it would be reachable by the whole host "+
			"(the container is host-networked, or this server cannot resolve the daemon's pids)", id, nsPath)
	}
	return nsPath, nil
}

// sameNetnsAsSelf reports whether nsPath names the same network namespace as
// this process's own. A network namespace is identified by the inode behind
// /proc/<pid>/ns/net, so two paths naming one namespace stat identically — which
// is how this can be answered without entering either of them.
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
