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
	if egress == nil || egress.Spec == nil {
		return b.Apply(ctx, spec)
	}
	e := egress.Spec
	if err := e.Validate(); err != nil {
		return api.DeployStatus{}, fmt.Errorf("bare: %w", err)
	}
	if e.Mode != "proxy" && e.Mode != "transparent" {
		return api.DeployStatus{}, fmt.Errorf("bare: client-side egress mode %q is not supported (want %q or %q)", e.Mode, "proxy", "transparent")
	}
	if egress.AgentImage == "" {
		return api.DeployStatus{}, fmt.Errorf("bare: client-side egress needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	port := e.ListenPort
	if port == 0 {
		port = defaultEgressPort
	}
	agent, err := b.img.pull(ctx, egress.AgentImage)
	if err != nil {
		return api.DeployStatus{}, fmt.Errorf("bare: pull egress agent image: %w", err)
	}

	// Proxy mode points the app at the loopback proxy via env; transparent captures
	// all app TCP through the companion's redirect (no env). Apply's recreate reaps
	// any prior companion (it lists by app, which the companion record carries).
	app := spec
	if e.Mode == "proxy" {
		app.Env = mergeEgressProxyEnv(spec.Env, *e, port)
	}
	return b.applyInternal(ctx, app, applyHooks{
		afterStart: func(ctx context.Context, replica int, netnsPath string) error {
			return b.startEgressCompanion(ctx, spec.Name, replica, netnsPath, agent, egress.AgentImage, egress, port)
		},
	})
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

// startEgressCompanion starts one replica's egress companion caretaker in the
// app instance's pinned netns.
func (b *Backend) startEgressCompanion(ctx context.Context, name string, replica int, netnsPath string, agent pulledImage, agentRef string, egress *deploy.AttachEgress, port int) error {
	e := egress.Spec
	role := &caretakerEgressRole{
		Server:     egress.RelayURL,
		Session:    egress.Session,
		Mode:       e.Mode,
		ListenPort: port,
		Rules:      e.Rules,
		Script:     e.Script,
		Default:    e.Default,
	}
	cfg := caretakerConfig{Egress: role}
	cs := companionSpec{
		appName:   name,
		compID:    fmt.Sprintf("cornus-%s-egress-%d", name, replica),
		replica:   replica,
		role:      roleEgressCaretaker,
		netnsPath: netnsPath,
		agent:     agent,
		agentRef:  agentRef,
	}
	if e.Mode == "transparent" {
		// The companion programs the nftables redirect in the shared netns and marks
		// its own sockets so its relay/direct dials escape it — both need NET_ADMIN.
		role.SetupRedirect = true
		cfg.Mark = egressMark
		cs.caps = []string{"CAP_NET_ADMIN"}
	}
	cs.cfg = cfg
	if err := b.startCompanion(ctx, cs); err != nil {
		return fmt.Errorf("bare: start egress caretaker: %w", err)
	}
	return nil
}
