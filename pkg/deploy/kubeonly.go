package deploy

import (
	"context"
	"log/slog"

	"cornus/pkg/api"
)

// WarnKubernetesOnlyFields warns, one line per field, for the DeploySpec fields
// that only the kubernetes backend can realize. Every host backend must call it
// from its apply prelude.
//
// It exists because these four fields were being accepted and dropped in TOTAL
// SILENCE by dockerhost (the DEFAULT backend), containerdhost, and barehost. Only
// incushost warned. A user who writes an `x-cornus-hub` block, DNS records, a
// Docker endpoint, or a proxy block and deploys to the default backend got a
// successful deploy and no feature — the failure mode the "warn per-field, never a
// silent drop" rule in .agents/docs/LTM/deploy-backend-contract.md exists to
// prevent, and the same one that let barehost drop spec.Ingress unnoticed.
//
// Shared rather than copied into four preludes for the reason those four diverged
// in the first place: the fields are kubernetes-only for the SAME reason on every
// host backend, so the explanation should exist once, and a field added to this
// list should start warning everywhere at once. Where a backend has something
// specific to say — incus mapping the narrowest subset, dockerhost serving ingress
// itself — that stays in its own prelude. The wording here is incushost's, which
// had it right and alone.
//
// agentForward is the one with a nuance worth keeping: on a host backend
// ssh-agent forwarding IS available, but it is driven by the server running in
// remote mode, never by the per-deployment spec field, so the message names the
// env var that actually turns it on rather than saying "unsupported".
func WarnKubernetesOnlyFields(ctx context.Context, log *slog.Logger, spec api.DeploySpec, remoteModeEnv string) {
	if spec.Proxy != nil {
		log.WarnContext(ctx, "backend ignores proxy (a kubernetes-only enforcing egress sidecar); the workload's outbound traffic is unrestricted")
	}
	if spec.DNS != nil {
		log.WarnContext(ctx, "backend ignores dns records (a kubernetes-only caretaker resolver); the workload resolves every name through the host's own resolver")
	}
	if spec.Hub != nil {
		log.WarnContext(ctx, "backend ignores hub (a kubernetes-only workload-to-workload overlay); the workload's imported service names do not resolve and its exports are not registered")
	}
	if spec.Docker != nil {
		log.WarnContext(ctx, "backend ignores docker (a kubernetes-only in-pod Docker API endpoint); DOCKER_HOST is not injected, so the workload has no docker daemon to talk to")
	}
	if spec.UpdateConfig != nil {
		// Deliberately says nothing about WHEN this backend recreates: dockerhost
		// now recreates only what changed (see its reuse.go), while containerd,
		// bare and incus still recreate on every Apply, and this one message is
		// shared by all four. What every one of them has in common — and all the
		// operator needs here — is that a recreate, whenever it happens, is
		// wholesale rather than rolling.
		log.WarnContext(ctx, "backend ignores updateConfig: this backend has no rolling-update concept, so when it does replace a deployment's instances it replaces them wholesale, with a gap where the old instances used to be")
	}
	if spec.AgentForward {
		// Both arms warn. An empty remoteModeEnv means this backend has no
		// agent-forward story at all (barehost keeps its companions
		// single-purpose — see its AgentForwardEnabled), and that is exactly the
		// case where silence would be worst.
		if remoteModeEnv != "" {
			log.WarnContext(ctx, "backend ignores agentForward (a kubernetes-only per-deployment opt-in); on this backend ssh-agent forwarding is available exactly when the server runs in remote mode",
				"remoteMode", remoteModeEnv)
		} else {
			log.WarnContext(ctx, "backend ignores agentForward (a kubernetes-only per-deployment opt-in); this backend offers no ssh-agent forwarding, so `cornus exec --forward-agent` is refused rather than silently unwired")
		}
	}
}
