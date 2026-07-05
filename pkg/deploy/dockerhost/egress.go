package dockerhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/egresspolicy"
)

// Labels and defaults for cornus-managed companion containers — not app
// instances — that run alongside a deployment: the client-side-egress
// companion (this file) and the client-local-mount relay companion (mounts.go).
const (
	// labelRole distinguishes cornus-managed containers that are NOT app
	// instances (the egress companion, the mount-relay companion) so
	// Status/List/lifecycle skip them.
	labelRole = "cornus.role"
	// roleEgressCaretaker marks the egress companion caretaker container.
	roleEgressCaretaker = "egress-caretaker"
	// defaultEgressPort is the loopback port the companion proxy listens on.
	defaultEgressPort = 15002
	// egressMark is the SO_MARK the transparent companion stamps on its own sockets;
	// the nftables redirect exempts it so the caretaker's relay/direct dials escape.
	egressMark = 15002
)

// ApplyWithEgress implements deploy.EgressBackend: it deploys the workload and,
// beside it, a companion caretaker container that SHARES the workload's network
// namespace (NetworkMode container:<app>) and runs the client-side-egress forward
// proxy. The app reaches the proxy on loopback via injected proxy env vars, and its
// outbound connections are relayed through the cornus server back to the client (or
// a gateway) per the routing policy — exactly like the kubernetes sidecar, but as a
// second container instead of a pod sidecar.
//
// Both "proxy" and "transparent" modes are supported, and each replica gets its own
// companion sharing that instance's netns. Companions are reaped by removeInstances
// (Delete / recreate); manual Stop/Start/Restart act on the app instances only.
func (b *Backend) ApplyWithEgress(ctx context.Context, spec api.DeploySpec, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	if egress == nil || egress.Spec == nil {
		return b.Apply(ctx, spec)
	}
	e := egress.Spec
	if err := e.Validate(); err != nil {
		return api.DeployStatus{}, fmt.Errorf("dockerhost: %w", err)
	}
	if e.Mode != "proxy" && e.Mode != "transparent" {
		return api.DeployStatus{}, fmt.Errorf("dockerhost: client-side egress mode %q is not supported (want %q or %q)", e.Mode, "proxy", "transparent")
	}
	if egress.AgentImage == "" {
		return api.DeployStatus{}, fmt.Errorf("dockerhost: client-side egress needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	port := e.ListenPort
	if port == 0 {
		port = defaultEgressPort
	}

	// Proxy mode points the app at the loopback proxy via env; transparent captures
	// all app TCP through the companion's nftables redirect (no env). Deploy the app;
	// Apply's recreate semantics also reap any prior companions.
	appSpec := spec
	if e.Mode == "proxy" {
		appSpec = withEgressProxyEnv(spec, *e, port)
	}
	if _, err := b.Apply(ctx, appSpec); err != nil {
		return api.DeployStatus{}, err
	}
	// Each replica has its own network namespace, so each needs its own companion
	// sharing that instance's netns (NetworkMode container:<id>).
	appIDs, err := b.appInstanceIDs(ctx, spec.Name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	for k, appID := range appIDs {
		if err := b.startEgressCompanion(ctx, spec.Name, appID, k, egress, port); err != nil {
			return api.DeployStatus{}, fmt.Errorf("dockerhost: start egress caretaker: %w", err)
		}
	}
	return b.Status(ctx, spec.Name)
}

// withEgressProxyEnv returns a copy of spec with the caretaker proxy env vars merged
// into spec.Env. In proxy mode the caretaker proxy is authoritative, so its vars
// overwrite any caller-set proxy vars.
func withEgressProxyEnv(spec api.DeploySpec, e api.EgressSpec, port int) api.DeploySpec {
	env := make(map[string]string, len(spec.Env)+8)
	for k, v := range spec.Env {
		env[k] = v
	}
	for k, v := range egresspolicy.ProxyEnv(e, port) {
		env[k] = v
	}
	spec.Env = env
	return spec
}

// appInstanceIDs returns the ids of a deployment's app containers (one per
// replica), skipping any companion containers (egress, mount-relay).
func (b *Backend) appInstanceIDs(ctx context.Context, name string) ([]string, error) {
	cs, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range cs {
		if !isCompanion(c) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("dockerhost: no app instances found for %q", name)
	}
	return ids, nil
}

// startEgressCompanion creates and starts the companion caretaker sharing appID's
// network namespace.
func (b *Backend) startEgressCompanion(ctx context.Context, name, appID string, replica int, egress *deploy.AttachEgress, port int) error {
	e := egress.Spec
	role := &caretaker.EgressRole{
		Server:     egress.RelayURL,
		Session:    egress.Session,
		Mode:       e.Mode,
		ListenPort: port,
		Rules:      e.Rules,
		Script:     e.Script,
		Default:    e.Default,
	}
	cfg := caretaker.Config{Egress: role}
	hc := hostConfig{
		NetworkMode:   "container:" + appID,
		RestartPolicy: restartPolicy{Name: "unless-stopped"},
	}
	if e.Mode == "transparent" {
		// The companion programs the nftables redirect in the shared netns and marks
		// its own sockets so its relay/direct dials escape it — both need NET_ADMIN.
		role.SetupRedirect = true
		cfg.Mark = egressMark
		hc.CapAdd = []string{"NET_ADMIN"}
	}
	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
		cfg.Token = tok
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	body := createBody{
		Image: egress.AgentImage,
		Cmd:   []string{"caretaker"}, // the cornus image entrypoint is `cornus`
		Env:   []string{"CORNUS_CARETAKER_CONFIG=" + string(raw)},
		Labels: map[string]string{
			deploy.LabelManaged: "true",
			deploy.LabelApp:     name,
			labelRole:           roleEgressCaretaker,
		},
		HostConfig: hc,
	}
	id, err := b.api.containerCreate(ctx, fmt.Sprintf("cornus-%s-egress-%d", name, replica), body)
	if err != nil {
		return err
	}
	return b.api.containerStart(ctx, id)
}

// isCompanion reports whether a listed container is a cornus-managed companion
// (the egress companion or the mount-relay companion, see mounts.go), not an
// app instance.
func isCompanion(c containerSummary) bool {
	return c.Labels[labelRole] != ""
}
