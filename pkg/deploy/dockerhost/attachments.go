package dockerhost

import (
	"context"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

var _ deploy.AttachingBackend = (*Backend)(nil)

// ApplyWithAttachments implements deploy.AttachingBackend: it converges to spec
// with every client-side attachment realized by ONE companion caretaker per
// replica, sharing that replica's network namespace.
//
// This is the single entry point for the whole attachment surface —
// ApplyWithMounts and ApplyWithEgress are thin delegations to it, mirroring how
// the kubernetes backend folds its roles into one pod caretaker. Collecting the
// plan before anything is created is what makes it possible: Docker cannot add
// mounts to an already-created container, so every bind the app container needs
// must be known before the per-replica create loop, and the old
// "Apply, then start a second companion beside it" shape could never merge.
//
// Two things follow that were not previously possible on this backend:
//
//   - mounts and client-side egress in the SAME deploy. The server routes any
//     deploy declaring credentials or egress through an AttachingBackend and only
//     falls back to EgressBackend for an egress-ONLY deploy, so combining them
//     used to be rejected outright even though each worked alone.
//   - one caretaker process per replica instead of one per role, which also makes
//     the transparent-egress redirect incapable of capturing a sibling
//     companion's server dial: Mark is process-wide, so the mark that exempts the
//     egress proxy's own upstream dials exempts the 9P relay and port-forward
//     dials with it.
//
// Client-sourced credentials mostly do not arrive here any more. A co-located
// server resolves env deliveries at deploy time, materializes file deliveries as
// ordinary read-only binds, and serves endpoint deliveries on a listener it binds
// inside the workload's own network namespace — so what reaches this function is
// only what the server could NOT realize, and it is refused with the reason.
func (b *Backend) ApplyWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount, creds []deploy.AttachCredential, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	appSpec, err := deploy.RealizeCredentials(spec, creds, "dockerhost",
		"the server realizes env, file and endpoint deliveries itself when it is co-located with the daemon; a delivery arriving here could not be realized that way — remote mode, a tcp:// or ssh:// endpoint whose paths and pids are on another machine, or a ROOTLESS daemon that cannot read a credential directory this server owns — and there is no caretaker on this backend to serve it instead")
	if err != nil {
		return api.DeployStatus{}, b.errf("%w", err)
	}

	var (
		appBinds [][]mountSpec
		plans    []companionPlan
	)
	if len(mounts) > 0 {
		appSpec, appBinds, plans, err = b.planMounts(ctx, appSpec, mounts)
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
		replicas := deploy.Replicas(appSpec)
		if plans == nil {
			plans = make([]companionPlan, replicas)
		}
		for i := range plans {
			plans[i].egress = ep.egress
			plans[i].mark = ep.mark
			plans[i].capAdd = ep.capAdd
			if plans[i].agentImage == "" {
				plans[i].agentImage = ep.agentImage
			}
		}
	}
	if plans == nil {
		return b.apply(ctx, appSpec, nil, nil)
	}
	return b.apply(ctx, appSpec,
		func(replica int) []mountSpec {
			if replica < len(appBinds) {
				return appBinds[replica]
			}
			return nil
		},
		func(replica int) companionPlan { return plans[replica] },
	)
}
