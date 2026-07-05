//go:build linux

package barehost

import (
	"context"
	"fmt"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// The server refuses endpoint deliveries outright if this assertion ever stops
// holding, silently, so it is worth failing the build instead.
var _ deploy.CredentialEndpointBinder = (*Backend)(nil)

// BindsCredentialEndpoints implements deploy.CredentialEndpointBinder. This
// backend pins each instance's network namespace itself before creating the
// container, and runs runc as this server's own child, so the pin is a path in
// this very process's mount namespace.
//
// False in remote mode, where the workload is not this process's child and the
// pin names nothing locally.
func (b *Backend) BindsCredentialEndpoints(ctx context.Context) bool { return !b.remote }

// InstanceNetns implements deploy.CredentialEndpointBinder.
//
// It requires the instance to be RUNNING, via runningInstanceAt. That is not
// incidental strictness: a record can outlive its namespace, and a stopped
// instance's pin is exactly the leftover-file case below. Requiring running
// state means the answer is either a namespace that currently exists or an
// error, never a path that used to be one.
func (b *Backend) InstanceNetns(ctx context.Context, name string, replica int) (string, error) {
	rec, err := b.runningInstanceAt(ctx, name, replica)
	if err != nil {
		return "", err
	}
	if rec.NetNS == "" {
		// An instance on the host network carries no pin. Nothing to enter, and
		// binding in the server's own namespace would publish the credential to
		// the whole host — so this is a refusal, not a fallback.
		return "", fmt.Errorf("bare: instance %s has no network namespace of its own (host networking)", rec.ID)
	}
	if !hostrun.NetnsAlive(rec.NetNS) {
		return "", fmt.Errorf("bare: instance %s network namespace pin %s is not live", rec.ID, rec.NetNS)
	}
	return rec.NetNS, nil
}
