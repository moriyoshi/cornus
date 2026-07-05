//go:build linux

package barehost

import (
	"context"
	"fmt"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/egresspolicy"
)

const (
	// defaultEgressPort is the loopback port the companion proxy listens on when
	// the spec names none.
	defaultEgressPort = 15002
	// egressMark is the SO_MARK the transparent companion stamps on its own sockets
	// so the nftables redirect exempts its relay/direct dials (it runs as root and
	// so cannot be exempted by uid). Mirrors containerdhost.
	egressMark = 15002
)

var _ deploy.EgressBackend = (*Backend)(nil)

// ApplyWithEgress implements deploy.EgressBackend: it deploys the workload and,
// beside each replica, a companion caretaker JOINING that instance's pinned netns
// that runs the client-side-egress forward proxy. The app reaches the proxy on
// loopback (proxy mode injects proxy env; transparent mode captures all TCP via
// the companion's nftables redirect), and its outbound connections are relayed
// through the cornus server back to the client — the same model as the kubernetes
// pod sidecar, realized here as a second runc container in the same netns.
//
// Unlike the sidecar mount path, egress is NOT gated on remote mode: a co-located
// bare backend cannot itself relay the app's egress to the client, so the
// companion is always required for client-side egress.
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
		return spec, companionPlan{}, fmt.Errorf("bare: %w", err)
	}
	if e.Mode != "proxy" && e.Mode != "transparent" {
		return spec, companionPlan{}, fmt.Errorf("bare: client-side egress mode %q is not supported (want %q or %q)", e.Mode, "proxy", "transparent")
	}
	if egress.AgentImage == "" {
		return spec, companionPlan{}, fmt.Errorf("bare: client-side egress needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	port := e.ListenPort
	if port == 0 {
		port = defaultEgressPort
	}
	// Proxy mode points the app at the loopback proxy via env; transparent captures
	// all app TCP through the companion's redirect (no env).
	app := spec
	if e.Mode == "proxy" {
		app.Env = mergeEgressProxyEnv(spec.Env, *e, port)
	}
	role := &caretakerEgressRole{
		Server:     egress.RelayURL,
		Session:    egress.Session,
		Mode:       e.Mode,
		ListenPort: port,
		Rules:      e.Rules,
		Script:     e.Script,
		Default:    e.Default,
	}
	plan := companionPlan{egress: role}
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
