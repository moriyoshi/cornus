//go:build linux

package barehost

import (
	"context"
	"fmt"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

var _ deploy.AttachingBackend = (*Backend)(nil)

// companionPlan is one replica's contribution to its companion caretaker's
// Config, collected BEFORE anything is created so a single companion can carry
// every role that replica needs instead of one companion per role. Mirrors
// dockerhost/containerdhost's plan of the same name.
type companionPlan struct {
	roles  []caretakerMountRole
	egress *caretakerEgressRole
	binds  []specs.Mount
	// mark is the SO_MARK the companion stamps on its own sockets, and caps the
	// extra capabilities it needs — both driven by transparent egress.
	mark int
	caps []string
}

// hasRoles reports whether the plan contributes any role, i.e. whether a
// companion must be started for this replica at all.
func (p companionPlan) hasRoles() bool { return len(p.roles) > 0 || p.egress != nil }

// ApplyWithAttachments implements deploy.AttachingBackend: it converges to spec
// with every client-side attachment realized by ONE companion caretaker per
// replica, joining that replica's pinned network namespace.
//
// This is the single entry point for the whole attachment surface —
// ApplyWithMounts and ApplyWithEgress are thin delegations to it, mirroring the
// other host backends and the kubernetes one-caretaker-per-pod rule. It unlocks
// mounts and client-side egress in the SAME deploy: the server routes anything
// declaring egress through an AttachingBackend and only falls back to
// EgressBackend for an egress-ONLY deploy, so the pairing used to be rejected
// outright even though each feature worked alone.
//
// Unlike dockerhost/containerdhost there is no always-on companion here — this
// backend is daemonless and therefore always co-located with the server, so
// nothing needs a companion unless an attachment asks for one. A replica whose
// plan contributes no role gets no companion.
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
		return api.DeployStatus{}, fmt.Errorf("bare: client-sourced credentials are not yet supported by the bare backend")
	}

	appSpec := spec
	var (
		appBinds [][]specs.Mount
		plans    []companionPlan
		agentRef string
		err      error
	)
	if len(mounts) > 0 {
		agentRef = mounts[0].AgentImage
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
		if agentRef == "" {
			agentRef = egress.AgentImage
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
		}
	}
	if plans == nil {
		return b.applyInternal(ctx, appSpec, applyHooks{})
	}
	// Pull the agent image once per Apply, not per replica — and only once it is
	// certain a companion will run.
	agent, err := b.img.pull(ctx, agentRef)
	if err != nil {
		return api.DeployStatus{}, fmt.Errorf("bare: pull companion agent image: %w", err)
	}

	return b.applyInternal(ctx, appSpec, applyHooks{
		extraAppMounts: func(replica int) []specs.Mount {
			if replica < len(appBinds) {
				return appBinds[replica]
			}
			return nil
		},
		afterStart: func(ctx context.Context, replica int, netnsPath string) error {
			plan := plans[replica]
			if !plan.hasRoles() {
				return nil
			}
			return b.startPlanCompanion(ctx, spec.Name, replica, netnsPath, agent, agentRef, plan)
		},
	})
}

// startPlanCompanion starts one replica's companion caretaker in the app
// instance's pinned netns, carrying every role the plan contributed.
//
// Folding the roles into one companion is what the caretaker was always able to
// do (pkg/caretaker.Run composes an arbitrary role set under one supervisor
// tree); one companion per role was an artifact of each role arriving through
// its own entry point. Two companions in one netns also meant a transparent
// egress redirect captured the other's traffic, since the nftables chain is
// netns-scoped and exempts only the one process-wide Mark.
//
// The companion keeps the mount caretaker's identity whenever it carries mount
// roles and the egress caretaker's otherwise, so existing tooling and E2E
// assertions keep matching.
func (b *Backend) startPlanCompanion(ctx context.Context, name string, replica int, netnsPath string, agent pulledImage, agentRef string, plan companionPlan) error {
	compID, role := fmt.Sprintf("cornus-%s-egress-%d", name, replica), roleEgressCaretaker
	if len(plan.roles) > 0 {
		compID, role = fmt.Sprintf("cornus-%s-mount-%d", name, replica), roleMountCaretaker
	}
	cs := companionSpec{
		appName:   name,
		compID:    compID,
		replica:   replica,
		role:      role,
		netnsPath: netnsPath,
		agent:     agent,
		agentRef:  agentRef,
		cfg:       caretakerConfig{Mounts: plan.roles, Egress: plan.egress, Mark: plan.mark},
		binds:     plan.binds,
		caps:      plan.caps,
		// Only the caretaker's own kernel 9P mount syscall needs privilege; an
		// egress-only companion stays unprivileged exactly as before.
		privileged: len(plan.roles) > 0,
	}
	if err := b.startCompanion(ctx, cs); err != nil {
		return fmt.Errorf("bare: start companion caretaker: %w", err)
	}
	return nil
}
