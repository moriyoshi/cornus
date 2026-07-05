// Package dockerhost implements cornus's deploy.Backend against a Docker /
// containerd host. It speaks the Docker Engine REST API directly over the host
// socket (no heavy moby client dependency), converging containers labeled for a
// deployment to the desired spec (create or recreate).
package dockerhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// Backend deploys onto a Docker host via the Engine API.
type Backend struct {
	api        *engineClient
	policy     Policy
	remote     bool
	agentImage string
	companions *remotecompanion.Registry
	// skipPullIfLocal, when set, returns true for image refs that cornus's own
	// registry serves (bare or loopback-host refs, in docker-daemon re-export
	// mode). For such a ref already present in the daemon, Apply skips the pull —
	// the daemon has the image and pulling it would round-trip through cornus's
	// registry back to the daemon. nil disables the shortcut (always pull).
	skipPullIfLocal func(ref string) bool
}

// Option configures a Backend.
type Option func(*Backend)

// WithPolicy sets the host-privilege policy enforced at Apply time. Without it,
// the zero Policy applies: default-deny (no privileged containers, no host binds).
func WithPolicy(p Policy) Option {
	return func(b *Backend) { b.policy = p }
}

// WithRemote opts this Backend into the caretaker-sidecar mount-relay path
// (ApplyWithMounts, mounts.go) instead of the default co-located fast path
// (applyWithHostMounts, in pkg/server). There is no way to detect daemon
// co-location automatically, so this is always an explicit operator choice
// (see CORNUS_DOCKER_REMOTE) — never inferred.
func WithRemote(remote bool) Option {
	return func(b *Backend) { b.remote = remote }
}

// Remote implements deploy.RemoteCapable.
func (b *Backend) Remote() bool { return b.remote }

// WithAgentImage sets the cornus-embedding image (CORNUS_AGENT_IMAGE) used for
// the always-on remote-companion sidecar in remote mode (see mounts.go). It is
// consulted only when WithRemote(true) is also set; ApplyWithMounts/
// ApplyWithEgress take their own AgentImage per call instead (unaffected).
func WithAgentImage(image string) Option {
	return func(b *Backend) { b.agentImage = image }
}

// WithCompanionRegistry sets the server's per-instance companion-connection
// registry (pkg/remotecompanion), so ForwardPort can look up a remote-mode
// instance's companion connection instead of dialing it directly. Required
// for WithRemote(true) to make ForwardPort/cornus tunnel/cornus port-forward
// work; nil (the zero value) makes ForwardPort always error in remote mode.
func WithCompanionRegistry(r *remotecompanion.Registry) Option {
	return func(b *Backend) { b.companions = r }
}

// WithSkipPullIfLocal installs a predicate that marks image refs cornus's own
// registry serves (re-export mode). Apply skips pulling such a ref when the
// daemon already has it, avoiding a pointless round-trip through cornus's
// registry back to the daemon. nil (the default) always pulls.
func WithSkipPullIfLocal(pred func(ref string) bool) Option {
	return func(b *Backend) { b.skipPullIfLocal = pred }
}

// New connects to the Docker host from DOCKER_HOST (default
// unix:///var/run/docker.sock). By default it enforces a default-deny Policy;
// pass WithPolicy to relax it.
func New(opts ...Option) (*Backend, error) {
	c, err := newEngineClient()
	if err != nil {
		return nil, err
	}
	b := &Backend{api: c}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// Name returns the backend identifier.
func (b *Backend) Name() string { return "dockerhost" }

// Close releases the client.
func (b *Backend) Close() error { return nil }

func instanceName(app string, i int) string { return fmt.Sprintf("cornus-%s-%d", app, i) }

// labelNetworks records a container's user-defined network memberships
// (comma-joined) so Delete can garbage-collect networks whose last member is
// gone without inspecting every container.
const labelNetworks = "cornus.networks"

// pullSkipped reports whether Apply should skip pulling image: only when a
// skip-pull predicate is installed (docker-daemon re-export mode), the ref is one
// cornus's registry serves, and the daemon already has it. An inspect error is
// treated as "do not skip" so a transient failure falls back to a normal pull
// rather than deploying a stale or absent image.
func (b *Backend) pullSkipped(ctx context.Context, image string) bool {
	if b.skipPullIfLocal == nil || !b.skipPullIfLocal(image) {
		return false
	}
	present, err := b.api.imageExists(ctx, image)
	if err != nil || !present {
		return false
	}
	logging.FromContext(ctx, slog.Group("dockerhost", "image", image)).
		InfoContext(ctx, "using image already present in the local daemon (docker-daemon re-export); skipping registry pull")
	return true
}

// Apply pulls the image, ensures the spec's user-defined networks exist,
// removes any existing instances, then creates and starts the desired number
// of replicas attached to those networks. In remote mode (WithRemote) it also
// starts the always-on remote companion per replica (mounts.go) — with no
// mount roles, since a plain Apply carries no AttachMounts.
func (b *Backend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	return b.apply(ctx, spec, nil, nil)
}

// apply is Apply's shared implementation. extraMountsFor, when non-nil, is
// called once per replica index and its result is appended to that replica's
// own HostConfig.Mounts — used by ApplyWithMounts (mounts.go) to bind each
// AttachMount's per-replica caretaker-provisioned volume with propagation.
// Each replica needs its OWN bind of its OWN volume: sharing one volume's
// source path across replicas would let a mount event from one replica's
// caretaker propagate into a DIFFERENT replica's app container.
//
// companionFor, when non-nil, is called once per replica index and supplies
// that replica's own remote-companion mount roles/binds (ApplyWithMounts);
// nil (a plain Apply) means the companion, if started at all (remote mode),
// carries no mount roles. Whenever b.remote is true, apply starts the
// always-on remote companion for every replica regardless of companionFor —
// see startRemoteCompanion in mounts.go.
func (b *Backend) apply(ctx context.Context, spec api.DeploySpec, extraMountsFor func(replica int) []mountSpec, companionFor func(replica int) remoteCompanionMounts) (api.DeployStatus, error) {
	if spec.Name == "" || spec.Image == "" {
		return api.DeployStatus{}, fmt.Errorf("dockerhost: spec requires name and image")
	}
	if err := b.policy.Validate("dockerhost", spec); err != nil {
		return api.DeployStatus{}, err
	}
	if spec.Ingress != nil && (spec.Ingress.Enabled || len(spec.Ingress.Hosts) > 0) {
		// Ingress is a Kubernetes-only feature (it programs a networking.k8s.io
		// Ingress); a Docker host has no cluster ingress to create, so the field is
		// ignored rather than half-implemented, keeping compose files portable.
		logging.FromContext(ctx, slog.Group("dockerhost", "deployment", spec.Name)).
			WarnContext(ctx, "backend ignores ingress (kubernetes-only feature)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		// Knative Serving (autoscaling / scale-to-zero) needs the Knative
		// controllers on a Kubernetes cluster; a Docker host runs the workload as
		// an ordinary container instead, so the block is ignored.
		logging.FromContext(ctx, slog.Group("dockerhost", "deployment", spec.Name)).
			WarnContext(ctx, "backend ignores knative (kubernetes-only feature); running as an ordinary container without autoscaling")
	}
	// Telemetry: resolve the OTEL_* wiring once. Merge it into the app env now
	// (Docker env is baked at container-create, not patchable after), and spawn a
	// per-replica collector companion after the app containers exist (below).
	telemetry, err := deploy.BuildTelemetryWiring(spec, spec.Name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	if telemetry != nil {
		if b.agentImage == "" {
			return api.DeployStatus{}, fmt.Errorf("dockerhost: telemetry needs the cornus agent image (set CORNUS_AGENT_IMAGE)")
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
	if !b.pullSkipped(ctx, spec.Image) {
		if err := b.api.imagePull(ctx, spec.Image); err != nil {
			return api.DeployStatus{}, fmt.Errorf("pull %s: %w", spec.Image, err)
		}
	}
	// Order network attachments by compose `priority` (highest first) so the
	// highest-priority network becomes the container's primary interface — the
	// one whose gateway is the default route — matching Compose. spec is a value
	// copy, so reordering its slice here is local to this Apply and keeps
	// networkEnsure, the create body's primary endpoint, and the connect loop
	// below all consistent. Stable sort preserves the planner's name order for
	// equal priorities.
	sort.SliceStable(spec.Networks, func(i, j int) bool {
		return spec.Networks[i].Priority > spec.Networks[j].Priority
	})
	for _, n := range spec.Networks {
		if err := b.api.networkEnsure(ctx, n); err != nil {
			return api.DeployStatus{}, fmt.Errorf("network %s: %w", n.Name, err)
		}
	}
	// Named volumes carrying a compose driver / driver_opts / labels must be
	// created with them before the container mounts them — dockerd's implicit
	// mount-time provisioning would otherwise make a plain default-driver volume.
	// A plain named volume (no driver/opts/labels) still rides the mount as before.
	for _, v := range spec.Volumes {
		if v.Name == "" || (v.Driver == "" && len(v.DriverOpts) == 0 && len(v.Labels) == 0) {
			continue
		}
		if err := b.api.volumeEnsure(ctx, v); err != nil {
			return api.DeployStatus{}, fmt.Errorf("volume %s: %w", v.Name, err)
		}
	}
	// Recreate semantics: remove existing instances first. This is a
	// container-only teardown — it must NOT reap the deployment's own networks
	// (as the full Delete does), because the spec's networks were just ensured
	// above and the recreated containers are about to reattach to them. Reaping
	// here would delete the just-ensured network (its last member is gone) and
	// break the create body's NetworkMode reference.
	if _, err := b.removeInstances(ctx, spec.Name); err != nil {
		return api.DeployStatus{}, err
	}

	// Published host ports go to replica 0 only (matching the containerd
	// backend): a host port can be bound by exactly one container, so
	// duplicating PortBindings across replicas would make dockerd fail replica
	// 1+ at start with "port is already allocated" — after the old instances
	// were already removed. Replicas 1+ share the container config (including
	// ExposedPorts) but publish nothing.
	body := toCreateBody(spec)
	unpublished := body
	unpublished.HostConfig.PortBindings = nil
	replicas := deploy.Replicas(spec)
	appIDs := make([]string, replicas)
	// In remote mode, every replica gets its own dedicated scratch volume for
	// the companion's AgentRelayRole socket — independent of any --mount
	// volumes (ApplyWithMounts's own per-mount scratch dirs), so the agent
	// socket is visible inside the app container even for an instance with no
	// client-local mounts at all. Must be provisioned and bound into the app
	// container's OWN create body now: Docker mounts can't be added to an
	// already-created container, so this can't wait until after the create
	// loop below the way starting the companion itself can.
	var agentAppBind, agentCompanionBind []mountSpec
	if b.remote {
		agentAppBind = make([]mountSpec, replicas)
		agentCompanionBind = make([]mountSpec, replicas)
		for i := 0; i < replicas; i++ {
			volName := fmt.Sprintf("cornus-%s-agent-%d", spec.Name, i)
			if err := b.api.volumeEnsure(ctx, api.VolumeSpec{Name: volName}); err != nil {
				return api.DeployStatus{}, fmt.Errorf("dockerhost: create agent-relay scratch volume: %w", err)
			}
			mp, err := b.api.volumeInspect(ctx, volName)
			if err != nil {
				return api.DeployStatus{}, fmt.Errorf("dockerhost: inspect agent-relay scratch volume: %w", err)
			}
			agentAppBind[i] = mountSpec{Type: "bind", Source: mp, Target: remotecompanion.AgentScratchDir, BindOptions: &bindOptions{Propagation: "rslave"}}
			agentCompanionBind[i] = mountSpec{Type: "bind", Source: mp, Target: remotecompanion.AgentScratchDir, BindOptions: &bindOptions{Propagation: "rshared"}}
		}
	}
	for i := 0; i < replicas; i++ {
		body := body
		if i > 0 {
			body = unpublished
		}
		if extraMountsFor != nil || b.remote {
			// Copy into a fresh backing array before appending: body.HostConfig is a
			// value copy, but its Mounts slice header still points at the original's
			// backing array, so appending in place could alias across replicas.
			mounts := append([]mountSpec{}, body.HostConfig.Mounts...)
			if extraMountsFor != nil {
				mounts = append(mounts, extraMountsFor(i)...)
			}
			if b.remote {
				mounts = append(mounts, agentAppBind[i])
			}
			body.HostConfig.Mounts = mounts
		}
		id, err := b.api.containerCreate(ctx, instanceName(spec.Name, i), body)
		if err != nil {
			return api.DeployStatus{}, fmt.Errorf("create %s: %w", instanceName(spec.Name, i), err)
		}
		appIDs[i] = id
		// The primary network rides the create body; connect the rest before
		// start so every network's DNS aliases are live when the workload boots.
		for j := 1; j < len(spec.Networks); j++ {
			n := spec.Networks[j]
			if err := b.api.networkConnect(ctx, n, id); err != nil {
				return api.DeployStatus{}, fmt.Errorf("connect %s to %s: %w", instanceName(spec.Name, i), n.Name, err)
			}
		}
		if err := b.api.containerStart(ctx, id); err != nil {
			return api.DeployStatus{}, fmt.Errorf("start %s: %w", instanceName(spec.Name, i), err)
		}
	}
	if b.remote {
		for i, appID := range appIDs {
			var cm remoteCompanionMounts
			if companionFor != nil {
				cm = companionFor(i)
			}
			cm.binds = append(append([]mountSpec{}, cm.binds...), agentCompanionBind[i])
			if err := b.startRemoteCompanion(ctx, spec.Name, appID, i, b.agentImage, cm); err != nil {
				return api.DeployStatus{}, fmt.Errorf("dockerhost: start remote companion: %w", err)
			}
		}
	}
	// Telemetry collector companion per replica (each joins its app's netns, so it
	// binds the OTLP receiver on that replica's loopback). Started after the app
	// containers exist — NetworkMode: container:<appID> needs the target running.
	if telemetry != nil {
		for i, appID := range appIDs {
			if err := b.startTelemetryCompanion(ctx, spec.Name, appID, i, telemetry.Role); err != nil {
				return api.DeployStatus{}, err
			}
		}
	}
	return b.Status(ctx, spec.Name)
}

// Status reports the observed state of a deployment's instances.
func (b *Backend) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	st := api.DeployStatus{Name: name, Backend: b.Name()}
	for _, c := range containers {
		if isCompanion(c) {
			continue // a companion container is not an app instance
		}
		if st.Image == "" {
			st.Image = c.Image
		}
		if st.Origin == nil {
			st.Origin = deploy.OriginFromLabels(c.Labels)
		}
		inst := api.InstanceStatus{
			ID:      c.ID,
			State:   c.State,
			Running: c.State == "running",
		}
		// The container-list summary carries no health or exit code, so inspect
		// each instance for its structured State (one GET per instance; Status
		// may be polled but this stays simple and correct). Health is "" when the
		// image declares no HEALTHCHECK; ExitCode is only meaningful — and only
		// surfaced — once the container has terminated.
		if insp, err := b.api.containerInspect(ctx, c.ID); err == nil {
			inst.Health = insp.Health
			if !insp.Running {
				ec := insp.ExitCode
				inst.ExitCode = &ec
			}
		}
		st.Instances = append(st.Instances, inst)
	}
	return st, nil
}

// List reports all cornus-managed deployments on the host.
func (b *Backend) List(ctx context.Context) ([]api.DeployStatus, error) {
	containers, err := b.api.containerList(ctx, deploy.LabelManaged+"=true")
	if err != nil {
		return nil, err
	}
	byApp := map[string]*api.DeployStatus{}
	for _, c := range containers {
		if isCompanion(c) {
			continue // a companion container is not an app instance
		}
		app := c.Labels[deploy.LabelApp]
		if app == "" {
			continue
		}
		st, ok := byApp[app]
		if !ok {
			st = &api.DeployStatus{Name: app, Image: c.Image, Backend: b.Name(), Origin: deploy.OriginFromLabels(c.Labels)}
			byApp[app] = st
		}
		st.Instances = append(st.Instances, api.InstanceStatus{
			ID:      c.ID,
			State:   c.State,
			Running: c.State == "running",
		})
	}
	out := make([]api.DeployStatus, 0, len(byApp))
	for _, st := range byApp {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// forEachInstance applies fn to every container of a deployment. A name with
// no containers at all is an error wrapping deploy.ErrNotFound — the
// Stop/Start/Restart contract. (Delete does its own listing and stays
// delete-if-exists.)
func (b *Backend) forEachInstance(ctx context.Context, name string, fn func(id string) error) error {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return err
	}
	// Lifecycle verbs (Start/Stop/Restart) act on app instances only; the egress
	// companion is managed by Apply/Delete (its netns is bound to the app).
	appN := 0
	for _, c := range containers {
		if isCompanion(c) {
			continue
		}
		appN++
		if err := fn(c.ID); err != nil {
			return err
		}
	}
	if appN == 0 {
		return fmt.Errorf("dockerhost: deployment %q: %w", name, deploy.ErrNotFound)
	}
	return nil
}

// Start starts a deployment's instances.
func (b *Backend) Start(ctx context.Context, name string) error {
	return b.forEachInstance(ctx, name, func(id string) error { return b.api.containerStart(ctx, id) })
}

// Stop stops a deployment's instances without removing them.
func (b *Backend) Stop(ctx context.Context, name string) error {
	return b.forEachInstance(ctx, name, func(id string) error { return b.api.containerStop(ctx, id) })
}

// Restart restarts a deployment's instances.
func (b *Backend) Restart(ctx context.Context, name string) error {
	return b.forEachInstance(ctx, name, func(id string) error { return b.api.containerRestart(ctx, id) })
}

// Logs streams a deployment's container logs to w. It resolves the deployment
// name to its labeled container(s) and streams the first instance (a documented
// limitation; multi-instance log fan-in is not implemented). Docker returns a
// stdcopy-multiplexed stream for a non-TTY container, which already satisfies
// the deploy.Backend.Logs framing contract, so the bytes are passed through
// unchanged. ctx cancellation stops a follow.
//
// opts.Since is parsed with deploy.ParseSince (the shared cross-backend
// grammar) and normalized to Docker's canonical "seconds.nanoseconds" form
// before it reaches the daemon, so a malformed value fails here — identically
// on every backend — rather than depending on dockerd's parser.
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	if opts.Since != "" {
		t, err := deploy.ParseSince(opts.Since, time.Now())
		if err != nil {
			return fmt.Errorf("dockerhost: %w", err)
		}
		opts.Since = fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
	}
	if opts.Until != "" {
		t, err := deploy.ParseSince(opts.Until, time.Now())
		if err != nil {
			return fmt.Errorf("dockerhost: %w", err)
		}
		opts.Until = fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
	}
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return err
	}
	rc, err := b.api.containerLogs(ctx, id, opts)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

// firstInstanceID resolves a deployment name to its first labeled APP
// container ID (the same lookup Logs/ForwardPort/exec use; multi-instance
// fan-in is not implemented), skipping any companion containers (egress,
// remote-companion) — a companion is not addressable by exec/logs/stats/
// ForwardPort, and container-list order is not guaranteed to put the app
// instance first once a companion also carries the deployment's label.
func (b *Backend) firstInstanceID(ctx context.Context, name string) (string, error) {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		if !isCompanion(c) {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("dockerhost: no instances for deployment %q: %w", name, deploy.ErrNotFound)
}

// Stats streams a deployment's container metrics to w. It resolves the name to
// its first labeled container and passes Docker's stats JSON through unchanged
// (the docker CLI parses Docker's own format). ctx cancellation stops a live
// stream.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return err
	}
	rc, err := b.api.containerStats(ctx, id, opts.Stream)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

// StatPath returns metadata for path inside the deployment's first instance
// (docker cp / archive HEAD).
func (b *Backend) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	return b.api.containerArchiveStat(ctx, id, path)
}

// CopyFrom writes a tar of path (from the deployment's first instance) to w and
// returns the path's stat. Docker's archive tar bytes are passed through
// unchanged (docker cp from container / archive GET).
func (b *Backend) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	rc, st, err := b.api.containerArchiveGet(ctx, id, path)
	if err != nil {
		return api.PathStat{}, err
	}
	defer rc.Close()
	if _, err := io.Copy(w, rc); err != nil {
		return api.PathStat{}, err
	}
	return st, nil
}

// CopyTo extracts the tar read from r into path inside the deployment's first
// instance (docker cp into container / archive PUT).
func (b *Backend) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return err
	}
	return b.api.containerArchivePut(ctx, id, path, r, opts)
}

// ExecCreate creates an exec in the deployment's first instance and returns
// Docker's exec id (docker exec create).
func (b *Backend) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return "", err
	}
	return b.api.execCreate(ctx, id, cfg)
}

// ExecStart runs a created exec and bridges conn to its raw bidirectional stdio
// stream (docker exec start). It hijacks POST /exec/{id}/start and copies bytes
// in both directions until either side closes; for a non-TTY exec the process
// output is Docker's stdcopy-multiplexed stream, passed through unchanged.
func (b *Backend) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig, conn io.ReadWriteCloser) error {
	body, err := json.Marshal(struct {
		Detach bool `json:"Detach"`
		Tty    bool `json:"Tty"`
	}{Detach: false, Tty: cfg.Tty})
	if err != nil {
		return err
	}
	stream, err := b.api.hijack(ctx, "POST", "/exec/"+execID+"/start", body)
	if err != nil {
		return err
	}
	return deploy.Bridge(conn, stream)
}

// ExecInspect reports an exec's state (docker exec inspect).
func (b *Backend) ExecInspect(ctx context.Context, execID string) (api.ExecState, error) {
	return b.api.execInspect(ctx, execID)
}

// ExecResize resizes the exec's TTY to height rows by width columns (docker
// exec resize). It is an out-of-band control-plane call, separate from the
// ExecStart stdio stream.
func (b *Backend) ExecResize(ctx context.Context, execID string, height, width uint) error {
	return b.api.execResize(ctx, execID, height, width)
}

// Attach bridges conn to the deployment's first instance raw stdio stream
// (docker attach). It hijacks POST /containers/{id}/attach with the requested
// stream selection and copies bytes both ways until either side closes.
func (b *Backend) Attach(ctx context.Context, name string, cfg api.AttachConfig, conn io.ReadWriteCloser) error {
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return err
	}
	q := url.Values{}
	setBool := func(k string, v bool) {
		if v {
			q.Set(k, "1")
		} else {
			q.Set(k, "0")
		}
	}
	setBool("stream", cfg.Stream)
	setBool("stdin", cfg.Stdin)
	setBool("stdout", cfg.Stdout)
	setBool("stderr", cfg.Stderr)
	setBool("logs", cfg.Logs)
	stream, err := b.api.hijack(ctx, "POST", "/containers/"+id+"/attach?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	return deploy.Bridge(conn, stream)
}

// ForwardPort bridges conn to a port inside the deployment's first instance
// (kubectl port-forward parity). proto is "tcp" (or empty) or "udp": tcp
// splices the raw byte stream; udp opens a connected UDP socket to the
// container and bridges conn's length-prefixed datagram frames
// (wire.WriteDatagram) to it, one tunnel per client flow.
//
// In remote mode (WithRemote) it reroutes through that instance's always-on
// remote-companion caretaker instead: the companion shares the instance's
// network namespace (mounts.go), so the server opens a server-initiated
// TagPortForward stream on the companion's connection (looked up in the
// per-instance registry) and relays through THAT — the server itself never
// dials the instance's IP. Co-located mode (the default) is unchanged: it
// resolves the container and dials its IP:port directly, which assumes the
// server can route to the Docker bridge (holds when the dockerhost server
// runs on/with the Docker host).
func (b *Backend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if proto != "" && proto != "tcp" && proto != "udp" {
		return fmt.Errorf("dockerhost: unsupported port-forward protocol %q (only tcp and udp)", proto)
	}
	if b.remote {
		return b.forwardPortViaCompanion(ctx, name, port, proto, conn)
	}
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return err
	}
	ip, err := b.api.containerIP(ctx, id)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var d net.Dialer
	if proto == "udp" {
		upstream, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return fmt.Errorf("dockerhost: dial container udp %s: %w", addr, err)
		}
		wire.BridgeDatagramStream(conn, upstream)
		return nil
	}
	upstream, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dockerhost: dial container %s:%d: %w", ip, port, err)
	}
	return deploy.Bridge(conn, upstream)
}

// forwardPortViaCompanion reroutes ForwardPort through the deployment's first
// instance's remote-companion caretaker connection (looked up in the
// per-instance registry by ForwardPort's caller — always replica 0, matching
// firstInstanceID's existing "first instance only" scope). The companion
// shares that instance's network namespace, so its PortForwardRole accept
// loop can dial 127.0.0.1:port even though the server itself cannot reach the
// instance directly.
func (b *Backend) forwardPortViaCompanion(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if b.companions == nil {
		return fmt.Errorf("dockerhost: remote mode has no companion registry configured")
	}
	instance := remotecompanion.InstanceKey(name, 0)
	sess := b.companions.Get(instance)
	if sess == nil {
		return fmt.Errorf("dockerhost: remote companion for %q is not connected yet", instance)
	}
	stream, err := wire.OpenPortForward(sess, port, proto)
	if err != nil {
		return fmt.Errorf("dockerhost: open port-forward relay to companion: %w", err)
	}
	if proto == "udp" {
		wire.BridgeDatagramStream(conn, stream)
		return nil
	}
	// wire.Pipe, not deploy.Bridge: a yamux stream has no CloseWrite, so
	// Bridge's half-close-on-client-EOF branch would silently no-op and leak
	// this stream until the companion's own upstream connection happens to
	// end for unrelated reasons. A port-forward tunnel has no stdin/stdout
	// asymmetry to preserve anyway — tear down as soon as either side ends.
	wire.Pipe(conn, stream)
	return nil
}

// SupportsUDPPortForward reports that this backend can bridge proto "udp"
// ForwardPort tunnels (framed datagrams to a connected UDP socket). The server's
// port-forward handler probes for this optional capability before acking a UDP
// tunnel.
func (b *Backend) SupportsUDPPortForward() bool { return true }

// Delete stops and removes all instances of a deployment, then best-effort
// reaps cornus-managed networks whose last member is gone (mirroring
// `docker compose down`). External networks — anything without the managed
// label — and networks that still have members are left alone.
func (b *Backend) Delete(ctx context.Context, name string) error {
	nets, err := b.removeInstances(ctx, name)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(nets))
	for n := range nets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.reapNetwork(ctx, n)
	}
	return nil
}

// RemoveVolume removes a named Docker volume by its project-scoped name
// (deploy.VolumeRemover, for `compose down --volumes`). The name passed is the
// VolumeSpec.Name a compose project assigns, which dockerhost uses verbatim as
// the Docker volume name (see toCreateBody). Delete-if-exists.
func (b *Backend) RemoveVolume(ctx context.Context, name string) error {
	return b.api.volumeRemove(ctx, name)
}

// removeInstances stops and removes all containers of a deployment and returns
// the set of user-defined networks those containers belonged to (from the
// cornus.networks label). It deliberately does NOT reap networks: callers that
// want `docker compose down` network GC (Delete) reap the returned set
// afterwards, while Apply's recreate step reuses this to clear old containers
// without touching the networks it just ensured.
func (b *Backend) removeInstances(ctx context.Context, name string) (map[string]bool, error) {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return nil, err
	}
	nets := map[string]bool{}
	// The egress companion shares the app's netns (NetworkMode container:<app>), and
	// Docker refuses to remove a netns-provider while a dependent still exists — so
	// remove any companion FIRST, then the app instances.
	remove := func(c containerSummary) error {
		for _, n := range strings.Split(c.Labels[labelNetworks], ",") {
			if n != "" {
				nets[n] = true
			}
		}
		if err := b.api.containerRemove(ctx, c.ID); err != nil {
			return fmt.Errorf("remove %s: %w", c.ID, err)
		}
		return nil
	}
	for _, c := range containers {
		if isCompanion(c) {
			if err := remove(c); err != nil {
				return nil, err
			}
		}
	}
	for _, c := range containers {
		if !isCompanion(c) {
			if err := remove(c); err != nil {
				return nil, err
			}
		}
	}
	return nets, nil
}

// reapNetwork removes a network if cornus created it (managed label) and no
// container is attached anymore. Best-effort: any error leaves it in place.
func (b *Backend) reapNetwork(ctx context.Context, name string) {
	labels, members, err := b.api.networkInspect(ctx, name)
	if err != nil || labels[deploy.LabelManaged] != "true" || members > 0 {
		return
	}
	_ = b.api.networkRemove(ctx, name)
}
