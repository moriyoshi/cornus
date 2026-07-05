//go:build linux

package containerdhost

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/errdefs"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/logging"
	"cornus/pkg/remotecompanion"
)

// Backend deploys onto a containerd host via the containerd client API.
type Backend struct {
	client    clientAPI
	policy    hostpolicy.Policy
	dataDir   string
	address   string
	namespace string
	// snapshotter is Config.Snapshotter; empty means containerd's default.
	snapshotter string
	// remote is WithRemote's value: opts every instance into an always-on
	// remote companion (ApplyWithMounts/apply, mounts_linux.go) instead of
	// having client-local mounts be unsupported.
	remote bool
	// agentImage is WithAgentImage's value, used for a remote companion
	// started with no mount roles (a plain Apply in remote mode).
	agentImage string
	// companions is the server's per-instance companion-connection registry
	// (WithCompanionRegistry), so ForwardPort can reroute through a
	// remote-mode instance's companion instead of dialing it directly.
	companions *remotecompanion.Registry
	execs      *execRegistry
	net        networkManager
	hosts      *hostrun.HostsStore
	vols       *hostrun.VolumeStore

	// Startup netns reconcile (reconcile_linux.go): ran at most once per
	// backend, guarded so a pass that cannot reach containerd is retried on
	// the next API call.
	reconcileMu sync.Mutex
	reconciled  bool
}

var _ deploy.Backend = (*Backend)(nil)

// New connects to the containerd daemon per cfg (empty fields resolve from the
// environment; see Config). By default it enforces a default-deny policy; pass
// WithPolicy to relax it. The returned backend is a deploy.Backend so the
// non-linux stub can share the signature.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	cfg, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	client, err := ctd.New(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("containerd: connect %s: %w (is containerd running? set CORNUS_CONTAINERD_ADDRESS)", cfg.Address, err)
	}
	b := newBackend(realClient{Client: client, snapshotter: cfg.Snapshotter}, cfg, opts...)
	// Repair netns pins lost to a host reboot right away so containerd's
	// restart monitor can resurrect tasks without waiting for a deploy API
	// call. Graceful when containerd is not actually reachable yet: the pass
	// logs a warning and is retried on first use.
	b.ensureReconciled(context.Background())
	return b, nil
}

// NewWithClient builds a backend over an injected client. It is the seam unit
// tests use to run against an in-memory fake instead of a daemon.
func NewWithClient(client clientAPI, cfg Config, opts ...Option) (*Backend, error) {
	cfg, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	return newBackend(client, cfg, opts...), nil
}

func newBackend(client clientAPI, cfg Config, opts ...Option) *Backend {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return &Backend{
		client:      client,
		policy:      o.policy,
		dataDir:     cfg.DataDir,
		address:     cfg.Address,
		namespace:   cfg.Namespace,
		snapshotter: cfg.Snapshotter,
		remote:      o.remote,
		agentImage:  o.agentImage,
		companions:  o.companions,
		execs:       newExecRegistry(),
		net:         newCNIManager(cfg.DataDir),
		hosts:       hostrun.NewHostsStore(cfg.DataDir, "containerd", "containerd"),
		vols:        hostrun.NewVolumeStore(cfg.DataDir, "containerd", "containerd"),
	}
}

// Name returns the backend identifier (the CORNUS_DEPLOY_BACKEND value).
func (b *Backend) Name() string { return "containerd" }

// Remote implements deploy.RemoteCapable.
func (b *Backend) Remote() bool { return b.remote }

// Close releases the containerd client. Workloads, their netns bind mounts,
// CNI attachments, and log shims deliberately survive — a cornus server restart
// must not kill deployments.
func (b *Backend) Close() error { return b.client.Close() }

// appFilter is a containerd list filter matching a deployment's instances.
func appFilter(name string) string {
	return fmt.Sprintf(`labels.%q==%q`, deploy.LabelApp, name)
}

// instances lists a deployment's containers.
func (b *Backend) instances(ctx context.Context, name string) ([]ctd.Container, error) {
	cs, err := b.client.Containers(b.ns(ctx), appFilter(name))
	if err != nil {
		return nil, fmt.Errorf("containerd: list instances of %q: %w", name, err)
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID() < cs[j].ID() })
	return cs, nil
}

// firstInstance resolves a deployment name to its first labeled APP container
// (the same first-instance convention dockerhost uses; multi-instance fan-in
// is not implemented for logs/exec/copy/forward), skipping any companion
// containers (egress, remote-companion) — a companion is not addressable by
// exec/logs/stats/ForwardPort.
func (b *Backend) firstInstance(ctx context.Context, name string) (ctd.Container, error) {
	cs, err := b.instances(ctx, name)
	if err != nil {
		return nil, err
	}
	nctx := b.ns(ctx)
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil {
			continue
		}
		if !isCompanion(labels) {
			return c, nil
		}
	}
	return nil, fmt.Errorf("containerd: no instances for deployment %q: %w", name, deploy.ErrNotFound)
}

// runningTask returns the container's task, or an error naming the deployment
// when there is none (the instance is stopped).
func runningTask(ctx context.Context, c ctd.Container) (ctd.Task, error) {
	t, err := c.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("containerd: instance %s is not running", c.ID())
		}
		return nil, err
	}
	return t, nil
}

// instanceStatus reports one container's observed state in Docker-ish terms.
func (b *Backend) instanceStatus(ctx context.Context, c ctd.Container) api.InstanceStatus {
	st := api.InstanceStatus{ID: c.ID(), State: "exited"}
	task, err := c.Task(ctx, nil)
	if err != nil {
		return st
	}
	status, err := task.Status(ctx)
	if err != nil {
		return st
	}
	switch status.Status {
	case ctd.Running:
		st.State = "running"
		st.Running = true
	case ctd.Created:
		st.State = "created"
	case ctd.Paused, ctd.Pausing:
		st.State = "paused"
	case ctd.Stopped:
		st.State = "exited"
		// A stopped task retains its process exit status until the task is
		// deleted; surface it so compose service_completed_successfully gating
		// has a code to check. Health stays "" — containerd has no healthcheck
		// engine (the backend drops healthchecks at deploy time).
		ec := int(status.ExitStatus)
		st.ExitCode = &ec
	default:
		st.State = string(status.Status)
	}
	return st
}

// containerImage best-effort resolves the image ref a container was created from.
func containerImage(ctx context.Context, c ctd.Container) string {
	info, err := c.Info(ctx)
	if err != nil {
		return ""
	}
	return info.Image
}

// Status reports the observed state of a deployment's instances.
func (b *Backend) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	b.ensureReconciled(ctx)
	cs, err := b.instances(ctx, name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	st := api.DeployStatus{Name: name, Backend: b.Name()}
	nctx := b.ns(ctx)
	for _, c := range cs {
		labels, _ := c.Labels(nctx)
		if isCompanion(labels) {
			continue // a companion container is not an app instance
		}
		if st.Image == "" {
			st.Image = containerImage(nctx, c)
		}
		if st.Origin == nil {
			st.Origin = deploy.OriginFromLabels(labels)
		}
		st.Instances = append(st.Instances, b.instanceStatus(nctx, c))
	}
	return st, nil
}

// List reports all cornus-managed deployments in the namespace.
func (b *Backend) List(ctx context.Context) ([]api.DeployStatus, error) {
	b.ensureReconciled(ctx)
	nctx := b.ns(ctx)
	cs, err := b.client.Containers(nctx, fmt.Sprintf(`labels.%q==%q`, deploy.LabelManaged, "true"))
	if err != nil {
		return nil, fmt.Errorf("containerd: list managed containers: %w", err)
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID() < cs[j].ID() })
	byApp := map[string]*api.DeployStatus{}
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil {
			continue
		}
		if isCompanion(labels) {
			continue // a companion container is not an app instance
		}
		app := labels[deploy.LabelApp]
		if app == "" {
			continue
		}
		st, ok := byApp[app]
		if !ok {
			st = &api.DeployStatus{Name: app, Image: containerImage(nctx, c), Backend: b.Name(), Origin: deploy.OriginFromLabels(labels)}
			byApp[app] = st
		}
		st.Instances = append(st.Instances, b.instanceStatus(nctx, c))
	}
	out := make([]api.DeployStatus, 0, len(byApp))
	for _, st := range byApp {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// stopTimeout is how long a task gets to exit after SIGTERM before SIGKILL.
const stopTimeout = 10 * time.Second

// stopTask gracefully stops and deletes a container's task: SIGTERM, wait up
// to stopTimeout, SIGKILL, then delete the task. A container without a task is
// not an error (already stopped).
func stopTask(ctx context.Context, c ctd.Container) error {
	task, err := c.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	waitCh, err := task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("wait %s: %w", c.ID(), err)
	}
	if err := task.Kill(ctx, syscall.SIGTERM); err != nil && !errdefs.IsNotFound(err) {
		// A task that already exited can race the kill; treat as stopped.
		if !errdefs.IsFailedPrecondition(err) {
			return fmt.Errorf("kill %s: %w", c.ID(), err)
		}
	}
	select {
	case <-waitCh:
	case <-time.After(stopTimeout):
		if err := task.Kill(ctx, syscall.SIGKILL); err != nil && !errdefs.IsNotFound(err) && !errdefs.IsFailedPrecondition(err) {
			return fmt.Errorf("kill -9 %s: %w", c.ID(), err)
		}
		select {
		case <-waitCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := task.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("delete task %s: %w", c.ID(), err)
	}
	return nil
}

// Delete stops and removes all instances of a deployment: task teardown, CNI
// detach + netns removal, container (and snapshot) removal, hosts-file and
// anonymous-volume reaping, then best-effort reaping of managed networks whose
// last member is gone (mirroring dockerhost / `docker compose down`). Finally
// the surviving peers' hosts files drop the deleted instances' names.
func (b *Backend) Delete(ctx context.Context, name string) error {
	b.ensureReconciled(ctx)
	nctx := b.ns(ctx)
	cs, err := b.instances(ctx, name)
	if err != nil {
		return err
	}
	nets := map[string]bool{}
	// A companion (egress joins the app's pinned netns; the mount-relay
	// companion uses host networking) owns no netns of its own, so remove it
	// FIRST — before the app's teardownInstance unpins the app's own netns out
	// from under an egress companion depending on it. A companion carries no
	// netns/networks labels, so it needs no teardownInstance and contributes no
	// networks to reap.
	removeCompanion := func(c ctd.Container) error {
		if err := stopTask(nctx, c); err != nil {
			return fmt.Errorf("containerd: stop %s: %w", c.ID(), err)
		}
		if err := c.Delete(nctx, ctd.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("containerd: remove %s: %w", c.ID(), err)
		}
		return nil
	}
	for _, c := range cs {
		if labels, _ := c.Labels(nctx); isCompanion(labels) {
			if err := removeCompanion(c); err != nil {
				return err
			}
		}
	}
	for _, c := range cs {
		labels, _ := c.Labels(nctx)
		if isCompanion(labels) {
			continue
		}
		for _, n := range strings.Split(labels[labelNetworks], ",") {
			if n != "" {
				nets[n] = true
			}
		}
		if err := stopTask(nctx, c); err != nil {
			return fmt.Errorf("containerd: stop %s: %w", c.ID(), err)
		}
		b.net.teardownInstance(nctx, c.ID(), labels)
		if err := c.Delete(nctx, ctd.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("containerd: remove %s: %w", c.ID(), err)
		}
		b.hosts.Remove(c.ID())
	}
	b.reapAnonymousVolumes(name)
	names := make([]string, 0, len(nets))
	for n := range nets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.reapNetwork(nctx, n)
	}
	if len(cs) > 0 {
		if err := b.syncHosts(ctx); err != nil {
			logging.FromContext(ctx, slog.Group("containerd", "deployment", name)).
				WarnContext(ctx, "hosts sync failed", "error", err)
		}
	}
	return nil
}

// reapNetwork removes a managed network's conflist (and allocator entry) if no
// container is attached to it anymore. Best-effort: any error leaves it alone.
func (b *Backend) reapNetwork(nctx context.Context, network string) {
	cs, err := b.client.Containers(nctx, fmt.Sprintf(`labels.%q~=%q`, labelNetworks, ".+"))
	if err != nil {
		return
	}
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil {
			continue
		}
		for _, n := range strings.Split(labels[labelNetworks], ",") {
			if n == network {
				return // still has a member
			}
		}
	}
	_ = b.net.RemoveNetwork(network)
}
