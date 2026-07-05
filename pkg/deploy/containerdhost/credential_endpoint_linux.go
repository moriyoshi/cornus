//go:build linux

package containerdhost

import (
	"context"
	"fmt"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// The server refuses these deliveries outright if either assertion ever stops
// holding, silently, so it is worth failing the build instead.
var (
	_ deploy.CredentialEndpointBinder = (*Backend)(nil)
	_ deploy.CredentialBinder         = (*Backend)(nil)
)

// BindsCredentialDir implements deploy.CredentialBinder: this backend resolves
// paths cornus wrote, translating them for containerd's mount namespace in
// hostMounts, so a rendered credential file is realized as an ordinary
// read-only bind like any other.
//
// It was excluded from this until 2026-08-07, on the reasoning that it is absent
// from hostcheck.UsesHostMountFastPath. That predicate is about CLIENT-LOCAL 9P
// MOUNTS — whether the server kernel-9P-mounts and the runtime binds the
// mountpoint — and a credential directory involves no 9P at all. Excluding this
// backend from one capability because of a fact about a different one was a
// category error, not a limitation of containerd.
//
// False in remote mode, where the path names nothing on the machine running
// containerd.
func (b *Backend) BindsCredentialDir(ctx context.Context) bool { return !b.remote }

// BindsCredentialEndpoints implements deploy.CredentialEndpointBinder. This
// backend pins each instance's network namespace itself (hostrun.CNIManager
// creates it before the container is created), so the path names a namespace
// this server can open directly.
//
// False in remote mode: the pin lives on the machine running containerd, and
// this server's mount namespace does not contain it. Binding would either fail
// or — worse — resolve some unrelated local path.
func (b *Backend) BindsCredentialEndpoints(ctx context.Context) bool { return !b.remote }

// InstanceNetns implements deploy.CredentialEndpointBinder.
//
// The LOCAL spelling of the pin is deliberate. hostNetnsPath translates the same
// value for containerd's mount namespace, and that translation is right for the
// OCI spec and wrong here: this server opens the path with its own eyes, which
// is the case hostpaths_linux.go singles out as keeping the local spelling.
func (b *Backend) InstanceNetns(ctx context.Context, name string, replica int) (string, error) {
	nctx := b.ns(ctx)
	id := instanceName(name, replica)
	c, err := b.client.LoadContainer(nctx, id)
	if err != nil {
		return "", fmt.Errorf("containerd: load %s: %w", id, err)
	}
	labels, err := c.Labels(nctx)
	if err != nil {
		return "", fmt.Errorf("containerd: labels for %s: %w", id, err)
	}
	nsPath := labels[labelNetNS]
	if nsPath == "" {
		return "", fmt.Errorf("containerd: instance %s has no pinned network namespace", id)
	}
	// A pin whose nsfs mount is gone (a host reboot clears /run) is a leftover
	// regular file. ns.GetNS would reject that on its own — it checks the FS
	// magic — so this is not the thing standing between us and a bad bind. It is
	// here for the diagnostic: the library's "unknown FS magic on ..." describes
	// the inode, while the actual situation is that reboot recovery has not
	// rebuilt this namespace yet, which is precisely the case where retrying is
	// the right response rather than giving up.
	if !hostrun.NetnsAlive(nsPath) {
		return "", fmt.Errorf("containerd: instance %s network namespace pin %s is not live", id, nsPath)
	}
	return nsPath, nil
}
