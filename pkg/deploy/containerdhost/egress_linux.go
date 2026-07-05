//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
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
	if egress == nil || egress.Spec == nil {
		return b.Apply(ctx, spec)
	}
	e := egress.Spec
	if err := e.Validate(); err != nil {
		return api.DeployStatus{}, fmt.Errorf("containerd: %w", err)
	}
	if e.Mode != "proxy" && e.Mode != "transparent" {
		return api.DeployStatus{}, fmt.Errorf("containerd: client-side egress mode %q is not supported (want %q or %q)", e.Mode, "proxy", "transparent")
	}
	if egress.AgentImage == "" {
		return api.DeployStatus{}, fmt.Errorf("containerd: client-side egress needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	port := e.ListenPort
	if port == 0 {
		port = defaultEgressPort
	}
	nctx := b.ns(ctx)
	agentImg, err := b.pullImage(nctx, egress.AgentImage)
	if err != nil {
		return api.DeployStatus{}, fmt.Errorf("containerd: pull egress agent image: %w", err)
	}

	// Proxy mode points the app at the loopback proxy via env; transparent captures
	// all app TCP through the companion's nftables redirect (no env). Apply's recreate
	// reaps any prior companion (it lists by app label, which the companion carries).
	app := spec
	if e.Mode == "proxy" {
		app.Env = mergeEgressProxyEnv(spec.Env, *e, port)
	}
	if _, err := b.Apply(ctx, app); err != nil {
		return api.DeployStatus{}, err
	}
	// Each replica has its own pinned netns, so each needs its own companion task
	// joining that instance's netns.
	netnsPaths, err := b.appNetnsPaths(ctx, spec.Name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	for k, netnsPath := range netnsPaths {
		if err := b.startEgressCompanion(ctx, spec.Name, netnsPath, k, agentImg, egress, port); err != nil {
			return api.DeployStatus{}, fmt.Errorf("containerd: start egress caretaker: %w", err)
		}
	}
	return b.Status(ctx, spec.Name)
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

// appNetnsPaths returns the pinned netns paths of the deployment's app instances
// (one per replica), skipping the egress companions.
func (b *Backend) appNetnsPaths(ctx context.Context, name string) ([]string, error) {
	nctx := b.ns(ctx)
	cs, err := b.instances(ctx, name)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil {
			continue
		}
		if isCompanion(labels) {
			continue
		}
		if p := labels[labelNetNS]; p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("containerd: no pinned network namespace for %q's app instances", name)
	}
	return paths, nil
}

// startEgressCompanion creates and starts the companion caretaker task in the app's
// pinned netns. The companion has no netns of its own (it joins the app's), carries
// the restart-monitor labels so it is resurrected with the app, and is reaped by
// Delete before the app's netns is torn down.
func (b *Backend) startEgressCompanion(ctx context.Context, name, netnsPath string, replica int, img ctd.Image, egress *deploy.AttachEgress, port int) (retErr error) {
	nctx := b.ns(ctx)
	compID := fmt.Sprintf("cornus-%s-egress-%d", name, replica)
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
	opts := hostrun.SpecOpts(ctx, "containerd", compID, api.DeploySpec{
		Command: []string{"caretaker"}, // the cornus image entrypoint is `cornus`
	}, img, netnsPath, nil)
	if e.Mode == "transparent" {
		// The companion programs the nftables redirect in the shared netns and marks
		// its own sockets so its relay/direct dials escape it — both need NET_ADMIN.
		role.SetupRedirect = true
		cfg.Mark = egressMark
		opts = append(opts, oci.WithAddedCapabilities([]string{"CAP_NET_ADMIN"}))
	}
	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
		cfg.Token = tok
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	// The caretaker config rides an env var; append it after the base spec opts.
	opts = append(opts, oci.WithEnv([]string{"CORNUS_CARETAKER_CONFIG=" + string(raw)}))
	logURI, err := b.logURI(compID)
	if err != nil {
		return err
	}
	labels := map[string]string{
		deploy.LabelManaged: "true",
		deploy.LabelApp:     name,
		labelRole:           roleEgressCaretaker,
		restart.PolicyLabel: "unless-stopped",
		restart.StatusLabel: string(ctd.Running),
		restart.LogURILabel: logURI,
	}
	c, err := b.client.CreateContainer(nctx, compID, img, labels, opts)
	if err != nil {
		return fmt.Errorf("create %s: %w", compID, err)
	}
	defer func() {
		if retErr != nil {
			_ = c.Delete(nctx, ctd.WithSnapshotCleanup)
		}
	}()
	if err := b.startTask(nctx, c, logURI); err != nil {
		return fmt.Errorf("start %s: %w", compID, err)
	}
	return nil
}
