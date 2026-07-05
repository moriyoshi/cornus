//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/pkg/netns"
	"github.com/containerd/containerd/runtime/restart"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/logging"
	"cornus/pkg/remotecompanion"
)

// Apply converges the deployment to spec: validate policy, pull the image,
// ensure networks, remove existing instances (recreate semantics, mirroring
// dockerhost), then create and start the desired replicas. In remote mode
// (WithRemote) it also starts the always-on remote companion per replica
// (mounts_linux.go) — with no mount roles, since a plain Apply carries no
// AttachMounts.
func (b *Backend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	return b.apply(ctx, spec, nil, nil)
}

// apply is Apply's shared implementation. extraMountsFor, when non-nil, is
// called once per replica index and its result is appended to that replica's
// own OCI mounts — used by ApplyWithMounts (mounts_linux.go) to bind each
// AttachMount's per-replica caretaker-provisioned host directory with
// propagation. Each replica needs its OWN backing directory: sharing one path
// across replicas would let a mount event from one replica's caretaker
// propagate into a DIFFERENT replica's app container.
//
// companionFor, when non-nil, is called once per replica index and supplies
// that replica's own remote-companion mount roles/binds (ApplyWithMounts);
// nil (a plain Apply) means the companion, if started at all (remote mode),
// carries no mount roles. Whenever b.remote is true, apply starts the
// always-on remote companion for every replica regardless of companionFor —
// see startRemoteCompanion in mounts_linux.go.
func (b *Backend) apply(ctx context.Context, spec api.DeploySpec, extraMountsFor func(replica int) []specs.Mount, companionFor func(replica int) remoteCompanionMounts) (api.DeployStatus, error) {
	if spec.Name == "" || spec.Image == "" {
		return api.DeployStatus{}, fmt.Errorf("containerd: spec requires name and image")
	}
	if err := b.policy.Validate("containerd", spec); err != nil {
		return api.DeployStatus{}, err
	}
	log := logging.FromContext(ctx, slog.Group("containerd", "deployment", spec.Name))
	if hc := spec.Healthcheck; hc != nil && !hc.Disabled() {
		// containerd has no healthcheck engine and nothing in cornus consumes
		// health, so the check is dropped rather than half-implemented.
		log.WarnContext(ctx, "backend ignores healthcheck")
	}
	if spec.Ingress != nil && (spec.Ingress.Enabled || len(spec.Ingress.Hosts) > 0) {
		// Ingress is a Kubernetes-only feature (it creates a networking.k8s.io
		// Ingress); on containerd there is no cluster ingress to program, so the
		// field is ignored rather than half-implemented. Compose files stay portable.
		log.WarnContext(ctx, "backend ignores ingress (kubernetes-only feature)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		// Knative Serving needs the Knative controllers on a Kubernetes cluster;
		// containerd runs the workload as an ordinary container, so the block is
		// ignored (no autoscaling / scale-to-zero).
		log.WarnContext(ctx, "backend ignores knative (kubernetes-only feature); running as an ordinary container without autoscaling")
	}
	if feats := hostrun.UnsupportedNetworkFeatures(spec); len(feats) > 0 {
		// Every network is a generated CNI bridge (driver/driverOpts have no
		// effect); names and aliases resolve fine via the hosts-file sync.
		log.WarnContext(ctx, "backend ignores unsupported network features",
			"features", strings.Join(feats, ", "))
	}
	// Telemetry: resolve the OTEL_* wiring once. Merge it into the app env now (the
	// OCI env is baked at container-create), and spawn a per-replica collector
	// companion after the instances' netns are pinned (below).
	telemetry, err := deploy.BuildTelemetryWiring(spec, spec.Name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	if telemetry != nil {
		if b.agentImage == "" {
			return api.DeployStatus{}, fmt.Errorf("containerd: telemetry needs the cornus agent image (set CORNUS_AGENT_IMAGE)")
		}
		env := make(map[string]string, len(spec.Env)+len(telemetry.Env))
		for k, v := range spec.Env {
			env[k] = v
		}
		for k, v := range telemetry.Env { // already excludes user-set OTEL_* keys
			env[k] = v
		}
		spec.Env = env
	}
	b.ensureReconciled(ctx)
	nctx := b.ns(ctx)
	img, err := b.pullImage(nctx, spec.Image)
	if err != nil {
		return api.DeployStatus{}, err
	}
	networks := hostrun.InstanceNetworks(spec)
	if err := b.net.EnsureNetworks(networks); err != nil {
		return api.DeployStatus{}, err
	}
	// Recreate semantics: remove existing instances first.
	if err := b.Delete(ctx, spec.Name); err != nil {
		return api.DeployStatus{}, err
	}
	replicas := deploy.Replicas(spec)
	netnsPaths := make([]string, replicas)
	// In remote mode, every replica gets its own dedicated scratch directory
	// for the companion's AgentRelayRole socket — independent of any --mount
	// backing dirs (ApplyWithMounts's own per-mount scratch dirs, see
	// mounts_linux.go), so the agent socket is visible inside the app
	// container even for an instance with no client-local mounts at all. Must
	// be bound into the app container's OWN mounts now, at create time.
	var agentAppBind []specs.Mount
	if b.remote {
		agentAppBind = make([]specs.Mount, replicas)
		for i := 0; i < replicas; i++ {
			dir := b.caretakerAgentDir(spec.Name, i)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return api.DeployStatus{}, fmt.Errorf("containerd: create agent-relay scratch dir: %w", err)
			}
			agentAppBind[i] = propagatedBindMount(dir, remotecompanion.AgentScratchDir, "rslave", false)
		}
	}
	for i := 0; i < replicas; i++ {
		// Published host ports go to replica 0 only: portmap DNATs a host port
		// to exactly one instance, and duplicate bindings would conflict.
		ports := spec.Ports
		if i > 0 {
			ports = nil
		}
		var extra []specs.Mount
		if extraMountsFor != nil {
			extra = extraMountsFor(i)
		}
		if b.remote {
			extra = append(extra, agentAppBind[i])
		}
		netnsPath, err := b.createInstance(ctx, spec, img, i, ports, extra)
		if err != nil {
			return api.DeployStatus{}, err
		}
		netnsPaths[i] = netnsPath
	}
	if b.remote {
		if b.agentImage == "" {
			return api.DeployStatus{}, fmt.Errorf("containerd: CORNUS_CONTAINERD_REMOTE is set but no agent image is configured (set CORNUS_AGENT_IMAGE)")
		}
		agentImg, err := b.pullImage(nctx, b.agentImage)
		if err != nil {
			return api.DeployStatus{}, fmt.Errorf("containerd: pull remote-companion agent image: %w", err)
		}
		for i, netnsPath := range netnsPaths {
			var cm remoteCompanionMounts
			if companionFor != nil {
				cm = companionFor(i)
			}
			cm.binds = append(append([]specs.Mount{}, cm.binds...), propagatedBindMount(b.caretakerAgentDir(spec.Name, i), remotecompanion.AgentScratchDir, "rshared", false))
			if err := b.startRemoteCompanion(ctx, spec.Name, netnsPath, i, agentImg, cm); err != nil {
				return api.DeployStatus{}, fmt.Errorf("containerd: start remote companion: %w", err)
			}
		}
	}
	// Telemetry collector companion per replica (each joins its app instance's
	// pinned netns to bind the OTLP receiver on that instance's loopback). Started
	// after the instances' netns are pinned.
	if telemetry != nil {
		agentImg, err := b.pullImage(nctx, b.agentImage)
		if err != nil {
			return api.DeployStatus{}, fmt.Errorf("containerd: pull telemetry agent image: %w", err)
		}
		for i, netnsPath := range netnsPaths {
			if err := b.startTelemetryCompanion(ctx, spec.Name, netnsPath, i, agentImg, telemetry.Role); err != nil {
				return api.DeployStatus{}, err
			}
		}
	}
	// Publish the new instances' names to every peer's /etc/hosts (and pick up
	// existing peers into theirs). Best-effort: a sync failure degrades name
	// resolution, not the deploy.
	if err := b.syncHosts(ctx); err != nil {
		log.WarnContext(ctx, "hosts sync failed", "error", err)
	}
	return b.Status(ctx, spec.Name)
}

// createInstance realizes one replica: netns + CNI, the managed /etc/hosts,
// volume backings (seeded from the image when fresh), container, and a
// started task logging through the cornus log shim. On error everything
// created so far is torn down. extraMounts, when non-empty, are appended after
// the spec/volume mounts (see apply's extraMountsFor). Returns the instance's
// own pinned netns path, so apply can join a remote companion to it.
func (b *Backend) createInstance(ctx context.Context, spec api.DeploySpec, img ctd.Image, replica int, ports []api.PortMapping, extraMounts []specs.Mount) (netnsPath string, retErr error) {
	nctx := b.ns(ctx)
	id := instanceName(spec.Name, replica)
	networks := hostrun.InstanceNetworks(spec)

	// Re-materialize the conflists before attaching: Apply's recreate path runs
	// Delete first, which reaps any managed network whose last member was just
	// removed (deleting its conflist and freeing its subnet). Without this the
	// CNI load below would read a now-missing conflist and fail. Idempotent, and
	// mirrors repairNetns.
	if err := b.net.EnsureNetworks(networks); err != nil {
		return "", err
	}
	att, err := b.net.Setup(ctx, id, networks, ports)
	if err != nil {
		return "", err
	}
	teardownLabels := map[string]string{
		labelNetNS:    att.Netns,
		labelNetworks: strings.Join(hostrun.NetworkNames(spec), ","),
	}
	defer func() {
		if retErr != nil {
			b.net.teardownInstance(nctx, id, teardownLabels)
		}
	}()

	hostsPath, err := b.hosts.Create(id, id, att.IP)
	if err != nil {
		return "", err
	}
	defer func() {
		if retErr != nil {
			b.hosts.Remove(id)
		}
	}()

	mounts, vols, err := b.instanceMounts(spec, replica)
	if err != nil {
		return "", err
	}
	if err := b.seedVolumes(nctx, img, vols); err != nil {
		return "", err
	}
	// The managed /etc/hosts goes first so an explicit user bind of the same
	// destination (mounts are applied in order) still wins.
	mounts = append([]specs.Mount{hostrun.OCIBindMount(hostsPath, "/etc/hosts", false)}, mounts...)
	mounts = append(mounts, extraMounts...)

	logURI, err := b.logURI(id)
	if err != nil {
		return "", err
	}
	labels, err := containerLabels(spec, att, ports, logURI)
	if err != nil {
		return "", err
	}

	c, err := b.client.CreateContainer(nctx, id, img, labels, hostrun.SpecOpts(ctx, "containerd", id, spec, img, att.Netns, mounts))
	if err != nil {
		return "", fmt.Errorf("containerd: create %s: %w", id, err)
	}
	defer func() {
		if retErr != nil {
			_ = c.Delete(nctx, ctd.WithSnapshotCleanup)
		}
	}()

	if err := b.startTask(nctx, c, logURI); err != nil {
		return "", fmt.Errorf("containerd: start %s: %w", id, err)
	}
	return att.Netns, nil
}

// startTask creates and starts a container's task with its stdio wired to the
// log shim. This is the one point where no shim holds the log file open (there
// is no task yet), so the size-capped log rotation happens here; a rotation
// failure is logged but never blocks the start.
func (b *Backend) startTask(nctx context.Context, c ctd.Container, logURI string) error {
	if err := rotateLogIfNeeded(b.logPath(c.ID()), logMaxBytes()); err != nil {
		logging.FromContext(nctx, slog.Group("containerd", "instance", c.ID())).
			WarnContext(nctx, "log rotation failed", "error", err)
	}
	u, err := url.Parse(logURI)
	if err != nil {
		return err
	}
	task, err := c.NewTask(nctx, cio.LogURI(u))
	if err != nil {
		return err
	}
	if err := task.Start(nctx); err != nil {
		_, _ = task.Delete(nctx)
		return err
	}
	return nil
}

// Start starts a deployment's stopped instances: clears the explicitly-stopped
// restart-monitor label, repairs the netns and CNI hostrun.Attachment when the pinned
// netns is gone (a host reboot), and starts a fresh task on the recorded log
// URI.
func (b *Backend) Start(ctx context.Context, name string) error {
	b.ensureReconciled(ctx)
	log := logging.FromContext(ctx, slog.Group("containerd", "deployment", name))
	nctx := b.ns(ctx)
	cs, err := b.instances(ctx, name)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return fmt.Errorf("containerd: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	anyRepaired := false
	for _, c := range cs {
		if task, err := c.Task(nctx, nil); err == nil {
			if st, err := task.Status(nctx); err == nil && st.Status == ctd.Running {
				continue
			}
			// A lingering non-running task blocks NewTask; clear it.
			if _, err := task.Delete(nctx); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("containerd: clear stale task %s: %w", c.ID(), err)
			}
		}
		labels, err := c.Labels(nctx)
		if err != nil {
			return err
		}
		repaired, err := b.repairNetns(ctx, c, labels)
		if err != nil {
			return err
		}
		anyRepaired = anyRepaired || repaired
		if _, ok := labels[restart.ExplicitlyStoppedLabel]; ok {
			if _, err := c.SetLabels(nctx, map[string]string{restart.ExplicitlyStoppedLabel: "false"}); err != nil {
				return fmt.Errorf("containerd: clear stop label %s: %w", c.ID(), err)
			}
		}
		logURI := labels[restart.LogURILabel]
		if logURI == "" {
			if logURI, err = b.logURI(c.ID()); err != nil {
				return err
			}
		}
		if err := b.startTask(nctx, c, logURI); err != nil {
			return fmt.Errorf("containerd: start %s: %w", c.ID(), err)
		}
	}
	if anyRepaired {
		// Repairs allocated fresh IPs; every peer's hosts file must follow.
		if err := b.syncHosts(ctx); err != nil {
			log.WarnContext(ctx, "hosts sync failed", "error", err)
		}
	}
	return nil
}

// repairNetns recreates an instance's netns and CNI hostrun.Attachment when the pinned
// netns bind mount no longer exists or is stale (host reboot; manual unmount),
// rewriting the container's OCI spec and labels to the fresh paths. It reports
// whether it actually repaired anything, so callers know when the recorded IPs
// changed. It is shared by Start and the startup reconcile pass.
func (b *Backend) repairNetns(ctx context.Context, c ctd.Container, labels map[string]string) (bool, error) {
	nsPath := labels[labelNetNS]
	if nsPath == "" {
		return false, nil
	}
	if hostrun.NetnsAlive(nsPath) {
		return false, nil
	}
	// A leftover bind target (an empty file no longer backed by a netns) is
	// dead weight; clear it best-effort before pinning afresh.
	if _, err := os.Stat(nsPath); err == nil {
		_ = netns.LoadNetNS(nsPath).Remove()
	}
	nctx := b.ns(ctx)
	var networks []string
	for _, n := range strings.Split(labels[labelNetworks], ",") {
		if n != "" {
			networks = append(networks, n)
		}
	}
	if len(networks) == 0 {
		networks = []string{hostrun.DefaultNetwork}
	}
	var ports []api.PortMapping
	if raw := labels[labelPorts]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &ports)
	}
	if err := b.net.EnsureNetworks(networks); err != nil {
		return false, err
	}
	att, err := b.net.Setup(ctx, c.ID(), networks, ports)
	if err != nil {
		return false, err
	}
	// Point the container's baked OCI spec at the fresh netns path.
	spec, err := c.Spec(nctx)
	if err != nil {
		return false, err
	}
	for i, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			spec.Linux.Namespaces[i].Path = att.Netns
		}
	}
	if err := c.Update(nctx, withSpecUpdate(spec)); err != nil {
		return false, fmt.Errorf("containerd: update spec of %s: %w", c.ID(), err)
	}
	update := map[string]string{labelNetNS: att.Netns}
	if att.IP != "" {
		update[labelIP] = att.IP
	}
	if len(att.IPs) > 0 {
		if ips, err := json.Marshal(att.IPs); err == nil {
			update[labelNetIPs] = string(ips)
		}
	}
	if _, err := c.SetLabels(nctx, update); err != nil {
		return false, err
	}
	return true, nil
}

// withSpecUpdate replaces a container record's OCI spec.
func withSpecUpdate(spec *oci.Spec) ctd.UpdateContainerOpts {
	return func(ctx context.Context, client *ctd.Client, c *containers.Container) error {
		any, err := typeurl.MarshalAny(spec)
		if err != nil {
			return err
		}
		c.Spec = any
		return nil
	}
}

// Stop stops a deployment's instances without removing them. The
// explicitly-stopped label keeps the restart monitor from resurrecting an
// unless-stopped/always task; the container, netns, CNI hostrun.Attachment, and log
// file all persist for Start.
func (b *Backend) Stop(ctx context.Context, name string) error {
	b.ensureReconciled(ctx)
	nctx := b.ns(ctx)
	cs, err := b.instances(ctx, name)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return fmt.Errorf("containerd: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil {
			return err
		}
		if _, ok := labels[restart.PolicyLabel]; ok {
			if _, err := c.SetLabels(nctx, map[string]string{restart.ExplicitlyStoppedLabel: "true"}); err != nil {
				return fmt.Errorf("containerd: mark stopped %s: %w", c.ID(), err)
			}
		}
		if err := stopTask(nctx, c); err != nil {
			return fmt.Errorf("containerd: stop %s: %w", c.ID(), err)
		}
	}
	return nil
}

// Restart restarts a deployment's instances.
func (b *Backend) Restart(ctx context.Context, name string) error {
	if err := b.Stop(ctx, name); err != nil {
		return err
	}
	return b.Start(ctx, name)
}
