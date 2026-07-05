// Package dockerhost implements cornus's deploy.Backend against a container
// host, converging containers labeled for a deployment to the desired spec
// (create or recreate).
//
// It hosts TWO runtime engines behind one orchestration, selected by Flavor
// (flavor.go):
//
//   - "dockerhost" speaks the Docker Engine REST API over the host socket.
//   - "podman" speaks Podman's NATIVE libpod REST API — deliberately not
//     Podman's Docker-compat endpoints, whose known defects land on the very
//     paths this backend depends on (stats, attach, and archive all have open
//     compat-only bugs).
//
// Both are hand-rolled over net/http rather than a vendored SDK, which is what
// makes a second engine cheap: the heavy moby client would have dragged in the
// buildkit <-> docker <-> go-connections dependency diamond for one of them and
// bought nothing for the other.
//
// `dockerhost` is therefore the package's HISTORICAL name, not a claim about
// what it drives. The split is at engine_iface.go: everything else here is
// runtime-agnostic orchestration reaching the daemon only through Engine, so a
// reader who finds a libpod implementation in a package called dockerhost is in
// the right place.
package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/ingressroute"
	"cornus/pkg/logging"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// Backend deploys onto a Docker host via the Engine API.
type Backend struct {
	// api is the runtime seam (engine_iface.go). It is an interface, not the
	// concrete Docker REST client, so a second engine can be dropped in without
	// the orchestration in this package learning a second vocabulary.
	api Engine
	// flavor names the runtime this Backend drives (flavor.go). The zero value
	// reads as FlavorDocker, so every existing construction site is unchanged.
	flavor Flavor
	// podmanSvc is the `podman system service` THIS process started
	// (CORNUS_PODMAN_SERVICE=1), nil otherwise. Close stops it; without that it
	// outlives the backend it was started for.
	podmanSvc  *podmanService
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
	// registryCredentials resolves a fresh credential at the single app-image
	// pull site. nil preserves Docker's existing anonymous behavior.
	registryCredentials deploy.RegistryCredentials
	// isolatedNetwork records that this server runs in a container with its own
	// network namespace, so it cannot route to a workload's container IP. Used
	// only to explain a ForwardPort dial failure (see WithIsolatedNetwork).
	isolatedNetwork bool
	// selfID is the id of the container this cornus runs in, and selfIDKnown that
	// it has been settled (either pinned by WithSelfContainerID or resolved once
	// by selfContainerID). Guards the destructive paths against tearing down the
	// server itself — see self.go.
	selfMu      sync.Mutex
	selfID      string
	selfIDKnown bool
	// driverCache memoizes network name -> Docker driver, for deciding which of a
	// workload's addresses a HOST-process server can actually dial (see
	// hostisolation.go). Safe to cache for the process's lifetime: a network is
	// created with a driver and destroyed with it, never re-driven.
	driverMu    sync.Mutex
	driverCache map[string]string
	// rootlessOnce caches whether a podman daemon runs rootless, which decides
	// whether a workload's IP is routable from here at all (podman_rootless.go).
	rootlessOnce rootlessState
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

// WithIsolatedNetwork tells the backend that this server sits in a container
// with a network namespace of its own, so it has no route to a workload's
// container IP. It changes no behaviour — it only lets ForwardPort explain a
// dial failure that would otherwise surface as a bare timeout with nothing
// pointing at the cause.
func WithIsolatedNetwork(isolated bool) Option {
	return func(b *Backend) { b.isolatedNetwork = isolated }
}

// WithSkipPullIfLocal installs a predicate that marks image refs cornus's own
// registry serves (re-export mode). Apply skips pulling such a ref when the
// daemon already has it, avoiding a pointless round-trip through cornus's
// registry back to the daemon. nil (the default) always pulls.
func WithSkipPullIfLocal(pred func(ref string) bool) Option {
	return func(b *Backend) { b.skipPullIfLocal = pred }
}

// WithRegistryCredentials installs the per-pull registry credential resolver.
func WithRegistryCredentials(creds deploy.RegistryCredentials) Option {
	return func(b *Backend) { b.registryCredentials = creds }
}

// New connects to the runtime this Backend's flavor names: a Docker daemon from
// DOCKER_HOST (default unix:///var/run/docker.sock), or Podman via the libpod
// API (see WithFlavor and resolvePodmanAccess). By default it enforces a
// default-deny Policy; pass WithPolicy to relax it.
//
// Options are applied BEFORE the engine is built, which they were not
// originally. That ordering is required rather than tidy: WithFlavor decides
// which engine to construct, so an engine built first could only ever be the
// Docker one.
func New(opts ...Option) (*Backend, error) {
	b := &Backend{}
	for _, o := range opts {
		o(b)
	}
	if b.api == nil {
		eng, svc, err := newEngineFor(context.Background(), b.flavor)
		if err != nil {
			return nil, err
		}
		b.api, b.podmanSvc = eng, svc
	}
	return b, nil
}

// WithEngine injects a pre-built engine, bypassing flavor-based construction.
// Used by tests that drive a fake runtime; production callers use WithFlavor.
func WithEngine(e Engine) Option {
	return func(b *Backend) { b.api = e }
}

// newEngineFor builds the engine a flavor names, plus the service this process
// owns (non-nil only when cornus started `podman system service` itself, in
// which case Close must stop it).
func newEngineFor(ctx context.Context, f Flavor) (Engine, *podmanService, error) {
	if f != FlavorPodman {
		eng, err := newEngineClient()
		return eng, nil, err
	}
	access, err := resolvePodmanAccess(podmanEnvOrOS)
	if err != nil {
		return nil, nil, err
	}
	ep, svc := access.Endpoint, (*podmanService)(nil)
	if access.SSH != "" {
		ep, err = podmanSSHEndpoint(ctx, access.SSH)
		if err != nil {
			return nil, nil, err
		}
	}
	if access.Spawn {
		svc, err = startPodmanService(ctx)
		if err != nil {
			return nil, nil, err
		}
		ep = svc.Endpoint
	}
	eng, err := newPodmanEngine(ctx, ep)
	if err != nil {
		// The service was started for an engine that never materialized; leaving
		// it running would hold a socket nothing will ever connect to.
		svc.Stop()
		return nil, nil, err
	}
	return eng, svc, nil
}

// Name returns the backend identifier — the flavor this Backend drives
// ("dockerhost" or "podman"). It reaches operators through DeployStatus.Backend
// and ServerInfo.Backend.
func (b *Backend) Name() string { return b.tag() }

// ReportsHealth implements deploy.HealthReporter: Docker runs the container
// HEALTHCHECK itself (toCreateBody maps api.Healthcheck onto Config.Healthcheck)
// and Status reads State.Health.Status back into InstanceStatus.Health, so a
// `depends_on: condition: service_healthy` is satisfiable here.
func (b *Backend) ReportsHealth() bool { return true }

var _ deploy.HealthReporter = (*Backend)(nil)

// Close releases the client, and stops the podman service if this process
// started one.
func (b *Backend) Close() error {
	b.podmanSvc.Stop() // nil-safe
	return nil
}

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
// planFor, when non-nil, is called once per replica index and supplies that
// replica's companion roles/binds (mounts from ApplyWithMounts, egress from
// ApplyWithEgress, both from ApplyWithAttachments). ONE companion carries all
// of them — see startCompanion in mounts.go. It is started when the plan
// contributes any role, and unconditionally in remote mode (where it also
// carries PortForward/AgentRelay), so a plain Apply in remote mode still gets
// its always-on companion with an empty plan.
func (b *Backend) apply(ctx context.Context, spec api.DeploySpec, extraMountsFor func(replica int) []mountSpec, planFor func(replica int) companionPlan) (api.DeployStatus, error) {
	if spec.Name == "" || spec.Image == "" {
		return api.DeployStatus{}, b.errf("spec requires name and image")
	}
	if err := b.policy.Validate("dockerhost", spec); err != nil {
		return api.DeployStatus{}, err
	}
	b.warnUnsupported(ctx, logging.FromContext(ctx, slog.Group("dockerhost", "deployment", spec.Name)), spec)
	// Telemetry: resolve the OTEL_* wiring once. Merge it into the app env now
	// (Docker env is baked at container-create, not patchable after), and spawn a
	// per-replica collector companion after the app containers exist (below).
	telemetry, err := deploy.BuildTelemetryWiring(spec, spec.Name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	if telemetry != nil {
		if b.agentImage == "" {
			return api.DeployStatus{}, b.errf("telemetry needs the cornus agent image (set CORNUS_AGENT_IMAGE)")
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
		var credential *deploy.RegistryCredential
		if b.registryCredentials != nil {
			resolved, ok, err := b.registryCredentials(ctx, spec.Image)
			if err != nil {
				return api.DeployStatus{}, fmt.Errorf("resolve registry credential for %s: %w", spec.Image, err)
			}
			if ok {
				credential = &resolved
			}
		}
		if err := b.api.imagePull(ctx, spec.Image, credential); err != nil {
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
	// A cornus in a container of its own has no route to a user-defined network
	// it is not a member of — Docker's isolation chains drop it — so it joins the
	// workload's networks here, before the containers that will live on them
	// exist. Inert for a server on the host. See selfnet.go.
	b.joinWorkloadNetworks(ctx, spec.Name, spec.Networks)
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
	replicas := deploy.Replicas(spec)

	// Idempotence, `docker compose up` style: if the live containers were created
	// for exactly this configuration and this image content, keep them. See
	// reuse.go for the fingerprint and for reusableInstances, which is the whole
	// decision as a pure function.
	//
	// The fast path is confined to the plain, co-located Apply. An
	// attachment-bearing call (client mounts, egress, remote mode) hands each
	// replica binds and a companion plan that are NOT part of the spec, so the
	// spec fingerprint cannot speak for them and reuse would silently keep a
	// container wired to the previous call's attachments. reusableInstances
	// refuses any live set containing a companion for the same reason; this gate
	// is the belt to that braces, stated at the point where the caller's shape is
	// still visible.
	specHash := ""
	if extraMountsFor == nil && planFor == nil && !b.remote {
		// Resolved AFTER the pull above, so a mutable tag that now names different
		// bytes yields a different id and therefore a different fingerprint. An
		// unresolvable image means we cannot tell what the ref names; that must read
		// as "recreate", which is what an empty specHash produces (both here and,
		// via the absent label, on the NEXT apply).
		if id, present, err := b.api.imageInspect(ctx, spec.Image); err == nil && present && id != "" {
			if h, err := fingerprintSpec(spec, id); err == nil {
				specHash = h
			}
		}
	}
	if specHash != "" {
		live, err := b.api.containerList(ctx, deploy.LabelApp+"="+spec.Name)
		if err != nil {
			return api.DeployStatus{}, err
		}
		if keep, ok := reusableInstances(live, replicas, specHash, b.selfContainerID(ctx)); ok {
			// `up` on a stopped project must bring it back up without replacing it,
			// so a matching instance that is not running is started rather than
			// recreated. If it will not start, fall through to the full recreate:
			// that is exactly what this Apply did before the fast path existed, so
			// the fast path cannot leave a deployment worse off than it found it.
			startErr := b.startInstances(ctx, keep)
			if startErr == nil {
				logging.FromContext(ctx, slog.Group("dockerhost", "deployment", spec.Name)).
					InfoContext(ctx, "deployment already matches the desired configuration; keeping the running containers")
				return b.Status(ctx, spec.Name)
			}
			logging.FromContext(ctx, slog.Group("dockerhost", "deployment", spec.Name)).
				DebugContext(ctx, "an up-to-date instance would not start; recreating the deployment", "error", startErr)
		}
	}

	// Recreate semantics: remove existing instances first. This is a
	// container-only teardown — it must NOT reap the deployment's own networks
	// (as the full Delete does), because the spec's networks were just ensured
	// above and the recreated containers are about to reattach to them. Reaping
	// here would delete the just-ensured network (its last member is gone) and
	// break the create body's NetworkMode reference.
	_, withheld, err := b.removeInstances(ctx, spec.Name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	// The recreate could not clear an instance because it IS this cornus server
	// (self.go). Stop here rather than pressing on: the create below would fail
	// on the surviving container's name with dockerd's opaque "name is already in
	// use", and a recreate that did succeed would have killed the process serving
	// the request. Recreating the server is an operation from outside the server.
	if len(withheld) > 0 {
		return api.DeployStatus{}, b.errf("refusing to recreate deployment %q: %s is this cornus server's own container; redeploy it from outside this server", spec.Name, strings.Join(withheld, ", "))
	}

	// Published host ports go to replica 0 only (matching the containerd
	// backend): a host port can be bound by exactly one container, so
	// duplicating PortBindings across replicas would make dockerd fail replica
	// 1+ at start with "port is already allocated" — after the old instances
	// were already removed. Replicas 1+ share the container config (including
	// ExposedPorts) but publish nothing.
	body := toCreateBody(spec)
	// Stamp the fingerprint the next Apply compares against. One value for the
	// whole deployment (every replica realizes the same spec; which replica
	// publishes the host ports is derived from the spec, not chosen), so the
	// shared Labels map behind body/unpublished is written exactly once here.
	// Empty when the fast path was not eligible or the image could not be
	// resolved: an unstamped container never matches, so it is recreated, which is
	// the pre-fix behaviour.
	if specHash != "" {
		body.Labels[specHashLabel] = specHash
	}
	unpublished := body
	unpublished.HostConfig.PortBindings = nil
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
				return api.DeployStatus{}, b.errf("create agent-relay scratch volume: %w", err)
			}
			mp, err := b.api.volumeInspect(ctx, volName)
			if err != nil {
				return api.DeployStatus{}, b.errf("inspect agent-relay scratch volume: %w", err)
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
			return api.DeployStatus{}, fmt.Errorf("start %s: %w%s", instanceName(spec.Name, i), err, b.addressPoolHint(ctx, err))
		}
	}
	for i, appID := range appIDs {
		var plan companionPlan
		if planFor != nil {
			plan = planFor(i)
		}
		if !b.remote && !plan.hasRoles() {
			continue
		}
		if b.remote {
			plan.binds = append(append([]mountSpec{}, plan.binds...), agentCompanionBind[i])
		}
		if err := b.startCompanion(ctx, spec.Name, appID, i, plan); err != nil {
			return api.DeployStatus{}, b.errf("start companion caretaker: %w", err)
		}
	}
	// Telemetry collector companion per replica (each joins its app's netns, so it
	// binds the OTLP receiver on that replica's loopback). Started after the app
	// containers exist — NetworkMode: container:<appID> needs the target running.
	if telemetry != nil {
		// In "proxy" egress mode the app is steered to the loopback proxy by env
		// alone, so the collector — a separate process that never saw those vars —
		// silently exported straight out, bypassing the client-side egress policy
		// the operator configured. Hand it the same variables (both OTLP exporters
		// honour them: confighttp clones http.DefaultTransport, and grpc-go dials
		// through HTTPS_PROXY). "transparent" mode needs nothing here — its
		// nftables redirect is netns-scoped and already captures the collector.
		proxyEnv := egresspolicy.ProxyEnvFrom(spec.Env)
		for i, appID := range appIDs {
			if err := b.startTelemetryCompanion(ctx, spec.Name, appID, i, telemetry.Role, telemetry.Relay, proxyEnv); err != nil {
				return api.DeployStatus{}, err
			}
		}
	}
	return b.Status(ctx, spec.Name)
}

// startInstances starts every container in cs that is not already running. It is
// what makes the idempotent-apply fast path a real `up`: a project that was
// stopped (`compose stop`, or a container that exited under restart: "no") comes
// back up without being replaced. A container already running is left completely
// alone — no restart, which is the whole point.
func (b *Backend) startInstances(ctx context.Context, cs []containerSummary) error {
	for _, c := range cs {
		if c.State == "running" {
			continue
		}
		if err := b.api.containerStart(ctx, c.ID); err != nil {
			return fmt.Errorf("start %s: %w", c.ID, err)
		}
	}
	return nil
}

// warnUnsupported emits one warning per api.DeploySpec field this backend cannot
// honor, before anything is created. It is the whole refusal surface of the
// dockerhost backend in one place, which matters because dockerhost is the
// DEFAULT backend: a field accepted and dropped in silence here is invisible to
// every gate — the build passes, the tests pass, the deploy succeeds, and the
// workload is not what the operator asked for. See the "warn per-field, never a
// silent drop" rule in .agents/docs/LTM/deploy-backend-contract.md.
//
// Anything NOT listed here is either mapped into the create body (toCreateBody
// in engine.go), consumed by a shared helper (deploy.Replicas,
// deploy.RestartPolicy, deploy.StopGracePeriodSeconds,
// deploy.BuildTelemetryWiring), or realized outside this backend entirely
// (spec.Egress — env mode is injected client-side into spec.Env, and the relay
// modes reach this backend as a deploy.AttachEgress through ApplyWithEgress /
// ApplyWithAttachments, with spec.Egress still set on the spec that arrives
// here, so warning about it would report a failure to do something this backend
// is in fact doing).
//
// The kubernetes-only fields deliberately have no branch of their own: they are
// covered ONCE by deploy.WarnKubernetesOnlyFields, and adding a second branch
// for Proxy/DNS/Hub/Docker/AgentForward/UpdateConfig here would emit two
// warnings that contradict each other. TestWarnUnsupportedNeverRepeatsAWarning
// counts warnings for exactly that reason.
func (b *Backend) warnUnsupported(ctx context.Context, log *slog.Logger, spec api.DeploySpec) {
	deploy.WarnKubernetesOnlyFields(ctx, log, spec, "CORNUS_DOCKER_REMOTE")
	if spec.Credentials != nil {
		// Only a deploy that realized NOTHING reaches here with the block still
		// set: the attachment path clears it once the env deliveries are merged
		// (deploy.RealizeCredentials) and refuses outright for the kinds it
		// cannot serve. So this fires on a stateless apply, whose spec declares
		// credentials with no client session behind them to mint from. Without
		// it, such a deploy comes up looking healthy with none of its
		// credentials present.
		log.WarnContext(ctx, "backend ignores credentials: minting a client-sourced credential needs a live deploy-attach session, and a stateless apply has none, so nothing is injected and the workload sees none of the declared credentials")
	}
	if r := spec.Resources; r != nil && r.ReservedCPU > 0 {
		// Docker has only CPU shares (a relative weight) and hard quota; neither is
		// the guaranteed floor a reservation asks for, so the request is dropped.
		// The memory reservation is NOT affected — it maps to MemoryReservation.
		log.WarnContext(ctx, "backend ignores a CPU reservation: Docker has no guaranteed-CPU-floor concept (only shares and hard limits), so no capacity is set aside for this workload; the CPU/memory limits and any memory reservation still apply",
			"reservedCpu", r.ReservedCPU)
	}
	if ingressroute.Enabled(spec.Ingress) {
		// Ingress is a Kubernetes-only feature (it programs a networking.k8s.io
		// Ingress); a Docker host has no cluster ingress to create, so the field is
		// ignored rather than half-implemented, keeping compose files portable.
		//
		// Gated on the canonical predicate, not an open-coded one: a CLIENT-EMULATED
		// ingress is realized entirely on the client and asks nothing of any backend,
		// so warning about it would report a failure to do something nobody requested.
		log.WarnContext(ctx, "backend creates no cluster Ingress; the server serves this ingress itself (reach it with an ingress tunnel or CORNUS_INGRESS_LISTEN)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		// Knative Serving (autoscaling / scale-to-zero) needs the Knative
		// controllers on a Kubernetes cluster; a Docker host runs the workload as
		// an ordinary container instead, so the block is ignored.
		log.WarnContext(ctx, "backend ignores knative (kubernetes-only feature); running as an ordinary container without autoscaling")
	}
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
func (b *Backend) forEachInstance(ctx context.Context, name string, verb string, fn func(id string) error) error {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return err
	}
	// Never act on this cornus server's own container (self.go): a Stop or
	// Restart here would kill the process mid-request, and a Start of it is
	// vacuous anyway.
	containers, withheld := b.withoutSelf(ctx, verb, name, containers)
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
		// Distinguish "nothing here" from "the only thing here was me": a bare
		// not-found would send the operator looking for a deployment that plainly
		// exists.
		if len(withheld) > 0 {
			return b.errf("refusing to %s deployment %q: %s is this cornus server's own container", verb, name, strings.Join(withheld, ", "))
		}
		return b.errf("deployment %q: %w", name, deploy.ErrNotFound)
	}
	return nil
}

// Start starts a deployment's instances.
func (b *Backend) Start(ctx context.Context, name string) error {
	return b.forEachInstance(ctx, name, "start", func(id string) error { return b.api.containerStart(ctx, id) })
}

// Stop stops a deployment's instances without removing them.
func (b *Backend) Stop(ctx context.Context, name string) error {
	return b.forEachInstance(ctx, name, "stop", func(id string) error { return b.api.containerStop(ctx, id) })
}

// Restart restarts a deployment's instances.
func (b *Backend) Restart(ctx context.Context, name string) error {
	return b.forEachInstance(ctx, name, "restart", func(id string) error { return b.api.containerRestart(ctx, id) })
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
			return b.errf("%w", err)
		}
		opts.Since = fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
	}
	if opts.Until != "" {
		t, err := deploy.ParseSince(opts.Until, time.Now())
		if err != nil {
			return b.errf("%w", err)
		}
		opts.Until = fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
	}
	id, err := b.instanceID(ctx, name, opts.Instance)
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
	return b.instanceID(ctx, name, 0)
}

// instanceID resolves a deployment's idx-th app container (0-based).
//
// The listing order the daemon returns is not specified, so the containers are
// sorted by name first. That matters more than it looks: instance names are
// `cornus-<app>-<i>`, so sorting by name IS sorting by replica ordinal, and it is
// what makes "replica 1" mean the same container from one call to the next —
// which the log recorder depends on to resume a replica's tail where it left off.
func (b *Backend) instanceID(ctx context.Context, name string, idx int) (string, error) {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return "", err
	}
	var apps []containerSummary
	for _, c := range containers {
		if !isCompanion(c) {
			apps = append(apps, c)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return instanceSortKey(apps[i]) < instanceSortKey(apps[j]) })
	if idx < 0 || idx >= len(apps) {
		return "", b.errf("deployment %q has no instance %d (%d running): %w", name, idx, len(apps), deploy.ErrNotFound)
	}
	return apps[idx].ID, nil
}

// instanceSortKey is a container's stable ordering key: its first name, or its id
// when the daemon reported none.
func instanceSortKey(c containerSummary) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.ID
}

// Stats streams a deployment's container metrics to w. It resolves the name to
// the labeled container at opts.Instance and passes Docker's stats JSON through
// unchanged (the docker CLI parses Docker's own format). ctx cancellation stops
// a live stream.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	id, err := b.instanceID(ctx, name, opts.Instance)
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

var (
	_ deploy.MetricsSampler      = (*Backend)(nil)
	_ deploy.MetricsCapabilities = (*Backend)(nil)
)

// UnsupportedMetrics implements deploy.MetricsCapabilities. Docker's stats frame
// carries every family except the instantaneous core count: its CPU is a pair of
// cumulative readings for the caller to difference.
func (b *Backend) UnsupportedMetrics() []api.SampleField {
	return []api.SampleField{api.SampleFieldCPUCores}
}

// SampleMetrics implements deploy.MetricsSampler: one reading for one replica,
// in the neutral shape the observability collector records.
//
// The daemon is the sampler here, so this asks for a single non-streaming frame
// (?stream=0) and decodes it. hostrun.DockerStats already models that exact wire
// shape for the encoding direction, so decoding through it costs no new types
// and cannot drift from what the streaming path emits.
func (b *Backend) SampleMetrics(ctx context.Context, name string, instance int) (api.ResourceSample, error) {
	id, err := b.instanceID(ctx, name, instance)
	if err != nil {
		return api.ResourceSample{}, err
	}
	rc, err := b.api.containerStats(ctx, id, false)
	if err != nil {
		return api.ResourceSample{}, err
	}
	defer rc.Close()
	var frame hostrun.DockerStats
	err = json.NewDecoder(rc).Decode(&frame)
	// A container that is not running yet (or has just stopped) has no reading to
	// give, and Docker says so in two different ways depending on how far along it
	// is: an EMPTY body — a 200 that closes without a single JSON object, which
	// surfaces here as io.EOF — or a well-formed frame with every field zeroed.
	//
	// Both are "no sample", not a fault, and must be reported as ErrNotFound so a
	// collector enumerating replicas skips this one quietly. Treating either as an
	// error would make a normal deploy look like a broken backend; recording the
	// zeroed frame would be worse still, planting a fake "0 bytes of memory"
	// datapoint that is indistinguishable from a workload that genuinely
	// collapsed.
	if errors.Is(err, io.EOF) || (err == nil && frame.Read.IsZero()) {
		return api.ResourceSample{}, b.errf("instance %d of %q has no stats yet (not running): %w", instance, name, deploy.ErrNotFound)
	}
	if err != nil {
		return api.ResourceSample{}, b.errf("decoding stats for %q instance %d: %w", name, instance, err)
	}
	return frame.ToResourceSample(), nil
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
	stream, err := b.api.execStart(ctx, execID, cfg.Tty)
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
	stream, err := b.api.containerAttach(ctx, id, cfg)
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
		return b.errf("unsupported port-forward protocol %q (only tcp and udp)", proto)
	}
	if b.remote {
		return b.forwardPortViaCompanion(ctx, name, port, proto, conn)
	}
	// Refuse BEFORE dialing on a rootless podman daemon: the workload's netns is
	// not routable from here, so the dial can only time out, and a timeout reads
	// as "the workload is down" rather than "this topology cannot work".
	if err := b.rootlessForwardPortRefusal(ctx, name); err != nil {
		return err
	}
	id, err := b.firstInstanceID(ctx, name)
	if err != nil {
		return err
	}
	ip, err := b.instanceIP(ctx, name, id)
	if err != nil {
		return fmt.Errorf("%w%s", err, b.unreachableHint())
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var d net.Dialer
	if proto == "udp" {
		upstream, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return b.errf("dial container udp %s: %w%s", addr, err, b.unreachableHint())
		}
		wire.BridgeDatagramStream(conn, upstream)
		return nil
	}
	upstream, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return b.errf("dial container %s:%d: %w%s", ip, port, err, b.unreachableHint())
	}
	return deploy.Bridge(conn, upstream)
}

// unreachableHint explains a failed dial to a workload's container IP when the
// likely cause is that this server has no route to it at all.
//
// Worth the extra clause because the bare error is actively misleading: a
// timeout dialing a container IP reads as "the workload is down", when the
// workload may be perfectly healthy and simply on a network this container
// cannot see. Empty whenever cornus shares the runtime's network view, which is
// every non-containerized deployment.
func (b *Backend) unreachableHint() string {
	if b.isolatedNetwork {
		return " (this cornus runs in a container with its own network namespace, so it has no route to the workload's docker network:" +
			" run it with --network host, or set CORNUS_DOCKER_REMOTE=1 to reach the workload through a per-instance companion instead)"
	}
	// The same failure, one topology over: a HOST-process server pointed at a
	// daemon over TCP. If that daemon is on another machine, its container IPs
	// mean nothing here and the dial can only ever time out. Worth saying,
	// because this case had no hint at all — the clause above is gated on being
	// containerized, so the operator who forgot CORNUS_DOCKER_REMOTE got a bare
	// timeout and no pointer to the setting that exists for exactly this.
	if b.rootless(context.Background()) && !b.remote {
		return podmanRootlessHint
	}
	if b.api != nil && b.api.nonLocal() && !b.remote {
		return " (DOCKER_HOST names this daemon over TCP; if it is on another machine, its container IPs are not reachable from here —" +
			" set CORNUS_DOCKER_REMOTE=1 to reach workloads through a per-instance companion instead)"
	}
	return ""
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
		return b.errf("remote mode has no companion registry configured")
	}
	instance := remotecompanion.InstanceKey(name, 0)
	sess := b.companions.Get(instance)
	if sess == nil {
		return b.errf("remote companion for %q is not connected yet", instance)
	}
	stream, err := wire.OpenPortForward(sess, port, proto)
	if err != nil {
		return b.errf("open port-forward relay to companion: %w", err)
	}
	// UDP takes the same byte-for-byte copy as TCP here, NOT BridgeDatagramStream:
	// that function converts between cornus's framed-datagram encoding and a real
	// packet socket, and there is no packet socket on this side of a COMPANION
	// relay. Both ends are already framed — the caller's tunnel carries wire
	// datagrams, and the companion's PortForwardRole reads wire datagrams off the
	// stream before writing them to the real UDP socket — so converting stripped
	// the framing the companion was about to parse, and UDP port-forward through a
	// remote companion could not work at all. (The direct, non-companion path above
	// DOES bridge, because there the far side is a real packet socket.)
	//
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
	// A withheld self container is not an error here: Delete is what the compose
	// orphan sweep and `down` call, and those must keep making progress on the
	// rest of the deployment. withoutSelf has already warned about it, and the
	// still-attached container keeps reapNetwork from removing the network under
	// the running server.
	nets, _, err := b.removeInstances(ctx, name)
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
// cornus.networks label), plus the ids it refused to touch because they are this
// cornus server's own container (self.go). It deliberately does NOT reap
// networks: callers that want `docker compose down` network GC (Delete) reap the
// returned set afterwards, while Apply's recreate step reuses this to clear old
// containers without touching the networks it just ensured.
func (b *Backend) removeInstances(ctx context.Context, name string) (map[string]bool, []string, error) {
	containers, err := b.api.containerList(ctx, deploy.LabelApp+"="+name)
	if err != nil {
		return nil, nil, err
	}
	containers, withheld := b.withoutSelf(ctx, "remove", name, containers)
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
			// SIGTERM the companion and wait for it to exit BEFORE the force
			// remove below, which would SIGKILL it. A companion holding a client
			// local mount performed the kernel 9P mount inside its own rshared
			// view of a shared volume, so the mount is in a shared peer group with
			// the HOST's view of that volume — killing the process outright skips
			// the caretaker's unmount (pkg/caretaker's runMountStream unmounts via
			// defer once its context is cancelled) and strands a live 9P mount at
			// the volume's mountpoint whose backing socket is gone. The next apply
			// of the same deployment then fails at container create with
			// `invalid mount config for type "bind": stat ...: input/output
			// error`, and clearing it needs root on the daemon host.
			//
			// Graceful-stopping every companion rather than just mount-bearing ones
			// also lets the egress companion tear its own state down; the caretaker
			// exits promptly on SIGTERM, so this costs milliseconds. Best-effort: a
			// companion that is already gone (or wedged past dockerd's stop timeout)
			// must not block the remove that follows. Mirrors barehost's
			// teardownSupervised, which graceful-stops for the same reason.
			if err := b.api.containerStop(ctx, c.ID); err != nil {
				logging.FromContext(ctx, slog.Group("dockerhost", "deployment", name)).
					DebugContext(ctx, "companion did not stop gracefully; forcing removal",
						"container", c.ID, "error", err)
			}
			if err := remove(c); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, c := range containers {
		if !isCompanion(c) {
			if err := remove(c); err != nil {
				return nil, nil, err
			}
		}
	}
	return nets, withheld, nil
}

// reapNetwork removes a network if cornus created it (managed label) and no
// container is attached anymore. Best-effort: any error leaves it in place.
//
// This cornus's OWN container does not count as a member. An in-container server
// attaches itself to every network it deploys onto (selfnet.go), so counting it
// would leave the network — and the server's endpoint on it — behind after every
// `cornus delete` and every `compose down`, growing without bound. It leaves
// first, then reaps; and it leaves even when the network turns out to be
// external and survives, because the reason to be on it went away with the
// workload.
func (b *Backend) reapNetwork(ctx context.Context, name string) {
	labels, members, err := b.api.networkInspect(ctx, name)
	if err != nil {
		return
	}
	if self, ok := b.selfNetworkScope(ctx); ok {
		kept := members[:0]
		for _, id := range members {
			if sameContainer(id, self) {
				if err := b.api.networkLeave(ctx, name, id); err != nil {
					logging.FromContext(ctx, slog.Group("dockerhost", "network", name)).
						DebugContext(ctx, "could not detach this cornus server's own container from the workload's network",
							"container", id, "error", err)
					kept = append(kept, id)
				}
				continue
			}
			kept = append(kept, id)
		}
		members = kept
	}
	if labels[deploy.LabelManaged] != "true" || len(members) > 0 {
		return
	}
	_ = b.api.networkRemove(ctx, name)
}

// nonLocal is checked alongside remote mode because they are different questions:
// Remote() reports the mode the operator selected, while DOCKER_HOST=tcp://... and
// CORNUS_PODMAN_SOCKET=ssh://... reach a daemon on another machine with no mode set
// at all. Docker CREATES a missing bind source rather than failing, so without this
// the workload would come up healthy with an empty credential directory.
//
// Rootless podman was excluded until 2026-08-08, and the exclusion is now the
// mapping instead. The runtime runs as an ordinary user and maps container ids
// into its subuid range, so a file owned by a container-side uid is unreadable —
// measured on the rootless leg, which answered
// `statfs .../mounts/creds-<session>/0: permission denied`. Loosening the
// directory never helped, because the FILE was the problem too.
//
// What changed is that the server now asks this backend for the map
// (deploy.IDMapper, from libpod /info idMappings) and owns the file as the ids
// the workload actually runs with. Note that container root maps to the podman
// USER and everything above it to that user's subuid range, so the answer for a
// non-root workload is not the range base.
//
// BindsCredentialDir implements deploy.CredentialBinder: this backend resolves
// host paths the server writes, so a file-kind credential can be realized as an
// ordinary read-only bind rather than by a caretaker.
//
// False in remote mode, and that is the load-bearing half. A remote daemon may be
// on another machine, where the server's path names nothing — and Docker CREATES
// a missing bind source rather than refusing, so the workload would come up with
// an empty directory where its credential should be. Declining here sends the
// delivery back to a refusal the operator can read.
func (b *Backend) BindsCredentialDir(ctx context.Context) bool {
	return !b.remote && !b.api.nonLocal()
}
