//go:build linux

package containerdhost

import (
	"context"
	"fmt"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/egresspolicy"
)

// Labels and defaults for cornus-managed companion containers/tasks — not app
// instances — that run alongside a deployment: the client-side-egress
// companion (this file) and the client-local-mount relay companion
// (mounts_linux.go).
const (
	// labelRole marks a cornus-managed container that is NOT an app instance
	// (the egress companion, the mount-relay companion) so Status/List/Delete
	// treat it specially.
	labelRole = "cornus.role"
	// roleEgressCaretaker marks the egress companion caretaker container/task.
	roleEgressCaretaker = "egress-caretaker"
	// defaultEgressPort is the loopback port the companion proxy listens on.
	defaultEgressPort = 15002
	// egressMark is the SO_MARK the transparent companion stamps on its own sockets;
	// the nftables redirect exempts it so the caretaker's relay/direct dials escape.
	egressMark = 15002
)

// isCompanion reports whether a container's labels mark it as a cornus-managed
// companion (the egress companion or the mount-relay companion, see
// mounts_linux.go) rather than an app instance.
func isCompanion(labels map[string]string) bool {
	return labels[labelRole] != ""
}

// ApplyWithEgress implements deploy.EgressBackend: it deploys the workload and,
// beside it, a companion caretaker task that JOINS the workload's pinned network
// namespace and runs the client-side-egress forward proxy. The app reaches the
// proxy on loopback via injected proxy env vars, and its outbound connections are
// relayed through the cornus server back to the client — like the kubernetes pod
// sidecar, realized here as a second task in the same netns.
//
// Both "proxy" and "transparent" modes are supported, and each replica gets its own
// companion task joining that instance's pinned netns.
func (b *Backend) ApplyWithEgress(ctx context.Context, spec api.DeploySpec, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	return b.ApplyWithAttachments(ctx, spec, nil, nil, egress)
}

// planEgress validates the egress attachment and returns the app spec (with the
// proxy env merged in proxy mode) plus the companion contribution. Split out of
// ApplyWithEgress so ApplyWithAttachments can fold the result into the same
// companion as the mount roles.
func (b *Backend) planEgress(spec api.DeploySpec, egress *deploy.AttachEgress) (api.DeploySpec, companionPlan, error) {
	e := egress.Spec
	if err := e.Validate(); err != nil {
		return spec, companionPlan{}, fmt.Errorf("containerd: %w", err)
	}
	if e.Mode != "proxy" && e.Mode != "transparent" {
		return spec, companionPlan{}, fmt.Errorf("containerd: client-side egress mode %q is not supported (want %q or %q)", e.Mode, "proxy", "transparent")
	}
	if egress.AgentImage == "" {
		return spec, companionPlan{}, fmt.Errorf("containerd: client-side egress needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	port := e.ListenPort
	if port == 0 {
		port = defaultEgressPort
	}
	// Proxy mode points the app at the loopback proxy via env; transparent
	// captures all app TCP through the companion's nftables redirect (no env).
	app := spec
	if e.Mode == "proxy" {
		app.Env = mergeEgressProxyEnv(spec.Env, *e, port)
	}
	role := &caretaker.EgressRole{
		Server:     egress.RelayURL,
		Session:    egress.Session,
		Mode:       e.Mode,
		ListenPort: port,
		Rules:      e.Rules,
		Script:     e.Script,
		Default:    e.Default,
	}
	plan := companionPlan{egress: role, agentImage: egress.AgentImage}
	if e.Mode == "transparent" {
		// The companion programs the nftables redirect in the shared netns and marks
		// its own sockets so its relay/direct dials escape it — both need NET_ADMIN.
		role.SetupRedirect = true
		plan.mark = egressMark
		plan.caps = []string{"CAP_NET_ADMIN"}
	}
	return app, plan, nil
}

// mergeEgressProxyEnv returns base with the caretaker proxy env vars merged in
// (they win — the caretaker proxy is authoritative in proxy mode).
func mergeEgressProxyEnv(base map[string]string, e api.EgressSpec, port int) map[string]string {
	env := make(map[string]string, len(base)+8)
	for k, v := range base {
		env[k] = v
	}
	for k, v := range egresspolicy.ProxyEnv(e, port) {
		env[k] = v
	}
	return env
}
