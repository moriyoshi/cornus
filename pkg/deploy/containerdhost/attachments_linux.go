//go:build linux

package containerdhost

import (
	"context"
	"fmt"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

var _ deploy.AttachingBackend = (*Backend)(nil)

// ApplyWithAttachments implements deploy.AttachingBackend: it converges to spec
// with every client-side attachment realized by ONE companion caretaker task per
// replica, joining that replica's pinned network namespace.
//
// This is the single entry point for the whole attachment surface —
// ApplyWithMounts and ApplyWithEgress are thin delegations to it, mirroring
// dockerhost and the kubernetes backend's one-caretaker-per-pod rule. Collecting
// the plan before anything is created is what makes it possible: the OCI spec
// (mounts included) is baked at container-create, so every bind the app needs
// must be known before the per-replica create loop, and the old "Apply, then
// start a second companion beside it" shape could never merge.
//
// Two things follow that were not previously possible on this backend: mounts
// and client-side egress in the SAME deploy (the server routes anything
// declaring egress through an AttachingBackend and only falls back to
// EgressBackend for an egress-ONLY deploy, so the pairing used to be rejected
// outright), and one caretaker process per replica instead of one per role —
// which also makes the transparent-egress redirect incapable of capturing a
// sibling companion's server dial, since Mark is process-wide.
//
// Client-sourced credentials are NOT yet realized here — see the creds argument.
func (b *Backend) ApplyWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount, creds []deploy.AttachCredential, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	if len(creds) > 0 {
		// Delivering a credential needs more than a role on the companion: endpoint
		// deliveries bind a loopback port and inject resolved env into the APP
		// container, file deliveries need a scratch directory shared with it, and
		// deploy-time env deliveries have no Secret indirection to hide behind on a
		// host backend the way they do on kubernetes. That is a feature port, not a
		// merge, so it stays unimplemented rather than half-wired — the same answer
		// the server gave before this backend became an AttachingBackend.
		return api.DeployStatus{}, fmt.Errorf("containerd: client-sourced credentials are not yet supported by the containerd backend")
	}

	appSpec := spec
	var (
		appBinds [][]specs.Mount
		plans    []companionPlan
		err      error
	)
	if len(mounts) > 0 {
		appSpec, appBinds, plans, err = b.planMounts(appSpec, mounts)
		if err != nil {
			return api.DeployStatus{}, err
		}
	}
	if egress != nil && egress.Spec != nil {
		var ep companionPlan
		appSpec, ep, err = b.planEgress(appSpec, egress)
		if err != nil {
			return api.DeployStatus{}, err
		}
		// Every replica's companion carries the same egress role: each has its own
		// netns, so each needs its own proxy listening on that netns' loopback.
		if plans == nil {
			plans = make([]companionPlan, deploy.Replicas(appSpec))
		}
		for i := range plans {
			plans[i].egress = ep.egress
			plans[i].mark = ep.mark
			plans[i].caps = ep.caps
			if plans[i].agentImage == "" {
				plans[i].agentImage = ep.agentImage
			}
		}
	}
	if plans == nil {
		return b.apply(ctx, appSpec, nil, nil)
	}
	return b.apply(ctx, appSpec,
		func(replica int) []specs.Mount {
			if replica < len(appBinds) {
				return appBinds[replica]
			}
			return nil
		},
		func(replica int) companionPlan { return plans[replica] },
	)
}
