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
// Client-sourced credentials mostly do not arrive here any more. A co-located
// server resolves env deliveries at deploy time, materializes file deliveries as
// ordinary read-only binds, and serves endpoint deliveries on a listener it binds
// inside the workload's own network namespace — so what reaches this function is
// only what the server could NOT realize, and it is refused with the reason.
//
// All three kinds are realized by the server as of 2026-08-07, file deliveries
// included: this backend translates a server-written directory into containerd's
// spelling in hostMounts, which is what BindsCredentialDir now reports.
func (b *Backend) ApplyWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount, creds []deploy.AttachCredential, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	appSpec, err := deploy.RealizeCredentials(spec, creds, "containerd",
		"the server realizes env, file and endpoint deliveries itself when it is co-located with containerd; a delivery arriving here could not be realized that way \u2014 in practice remote mode, where the paths and pids this server resolves are not the ones containerd sees \u2014 and this backend starts no caretaker to serve it instead")
	if err != nil {
		return api.DeployStatus{}, fmt.Errorf("containerd: %w", err)
	}

	var (
		appBinds [][]specs.Mount
		plans    []companionPlan
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
