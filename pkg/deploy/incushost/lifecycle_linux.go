//go:build linux

package incushost

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// Apply converges the deployment to spec with recreate-on-Apply semantics: any
// existing instances for the app are torn down, then Replicas(spec) fresh
// instances are created and started. Published host ports go to replica 0 only.
//
// In remote mode each replica additionally gets a shared agent volume and a
// companion caretaker instance (companion_linux.go). The configuration remote
// mode needs is checked BEFORE anything is torn down, so a misconfigured server
// does not leave the operator with a deleted deployment and no replacement.
func (b *Backend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	if err := b.policy.Validate("incus", spec); err != nil {
		return api.DeployStatus{}, err
	}
	if b.remote {
		if err := b.remoteConfigError(); err != nil {
			return api.DeployStatus{}, err
		}
	}
	// Tear down any existing instances for this app (recreate semantics).
	if err := b.deleteApp(ctx, spec.Name); err != nil {
		return api.DeployStatus{}, err
	}
	n := deploy.Replicas(spec)
	for i := 0; i < n; i++ {
		if b.remote {
			if err := b.ensureAgentVolume(spec.Name, i); err != nil {
				return api.DeployStatus{}, err
			}
		}
		// Managed volumes come before the instance that mounts them: an instance
		// whose disk device names a volume that is not there fails to start.
		if err := b.ensureManagedVolumes(spec, i); err != nil {
			return api.DeployStatus{}, err
		}
		post, err := b.buildInstancesPost(ctx, spec, i)
		if err != nil {
			return api.DeployStatus{}, err
		}
		if err := b.conn.CreateInstance(post); err != nil {
			return api.DeployStatus{}, fmt.Errorf("incus: creating instance %s: %w", post.Name, err)
		}
		if b.remote {
			if err := b.startCompanionFor(ctx, spec.Name, i); err != nil {
				return api.DeployStatus{}, err
			}
		}
	}
	return b.Status(ctx, spec.Name)
}

// companionAddressTimeout bounds how long Apply waits for a freshly-created
// replica to acquire an address before giving up on its companion. It is a
// variable so tests can shrink it; the value is generous enough for a DHCP
// round trip on a busy host but short enough that a deploy onto a broken
// network fails with a real message instead of hanging on Apply's (usually
// deadline-free) context.
var companionAddressTimeout = 30 * time.Second

// startCompanionFor resolves the freshly-created replica's address and starts
// its companion against it. The address is what the companion's PortForwardRole
// dials, so it must exist before the companion does — a companion pointed at an
// instance with no address yet would relay into nothing.
func (b *Backend) startCompanionFor(ctx context.Context, app string, i int) error {
	waitCtx, cancel := context.WithTimeout(ctx, companionAddressTimeout)
	defer cancel()
	appHost, err := b.waitInstanceIPv4(waitCtx, instanceName(app, i))
	if err != nil {
		return err
	}
	return b.startCompanion(ctx, app, i, appHost)
}

// Start starts a stopped deployment's instances.
func (b *Backend) Start(ctx context.Context, name string) error {
	return b.actOnApp(ctx, name, "start", false)
}

// Stop stops a deployment's instances without removing them.
func (b *Backend) Stop(ctx context.Context, name string) error {
	return b.actOnApp(ctx, name, "stop", true)
}

// Restart restarts a deployment's instances.
func (b *Backend) Restart(ctx context.Context, name string) error {
	return b.actOnApp(ctx, name, "restart", true)
}

// Delete removes the named deployment. Delete-if-exists: a name with no live
// instances is a no-op success.
func (b *Backend) Delete(ctx context.Context, name string) error {
	return b.deleteApp(ctx, name)
}

// actOnApp applies a lifecycle action to every instance of an app, companions
// included — a stopped deployment whose companion kept running would keep
// answering port-forwards for a workload that is gone. It wraps
// deploy.ErrNotFound when the app has no REPLICAS (Start/Stop/Restart contract):
// a leftover companion alone is not a deployment.
func (b *Backend) actOnApp(ctx context.Context, name, action string, force bool) error {
	replicas, companions, err := b.appInstanceNames(name)
	if err != nil {
		return err
	}
	if len(replicas) == 0 {
		return fmt.Errorf("incus: deployment %q: %w", name, deploy.ErrNotFound)
	}
	for _, in := range append(replicas, companions...) {
		if err := b.conn.SetInstanceState(in, action, force, 0); err != nil {
			return err
		}
	}
	return nil
}

// deleteApp stops (best-effort) then deletes every instance of an app —
// companions included — and finally reaps the storage those instances owned: the
// shared agent volumes the companions used, and the ANONYMOUS managed volumes
// the replicas were created with.
// Returns nil when the app has no instances (delete-if-exists).
//
// Volumes are reaped after the instances are gone because Incus refuses to
// delete a volume that is still attached, and unconditionally rather than only
// in remote mode, so that turning remote mode OFF and redeploying still cleans
// up what the previous configuration created. A volume that was never created is
// a no-op delete at the seam.
//
// The anonymous volume names are read off the instances BEFORE they are deleted,
// because that is the only place they exist: their names hash the container
// paths from the spec, and Delete only ever gets a deployment name. Named
// volumes are deliberately NOT reaped — they are shared and project-scoped, and
// outliving a deployment is what makes them named (`compose down --volumes`
// removes them through RemoveVolume).
func (b *Backend) deleteApp(ctx context.Context, name string) error {
	replicas, companions, err := b.appInstances(name)
	if err != nil {
		return err
	}
	var anon []string
	for _, in := range replicas {
		anon = append(anon, anonVolumesOf(in.Config)...)
	}
	all := append(instanceNamesOf(replicas), instanceNamesOf(companions)...)
	for _, in := range all {
		// Incus refuses to delete a running instance; stop first (best-effort —
		// an already-stopped instance errors, which we ignore).
		_ = b.conn.SetInstanceState(in, "stop", true, 0)
		if err := b.conn.DeleteInstance(in); err != nil {
			return fmt.Errorf("incus: deleting instance %s: %w", in, err)
		}
	}
	if len(all) == 0 {
		return nil
	}
	// One volume per replica, but a torn-down deployment may have lost its
	// replicas and kept its companions (or the reverse), so reap for whichever
	// count is larger rather than trusting either alone.
	n := len(replicas)
	if len(companions) > n {
		n = len(companions)
	}
	if err := b.deleteAgentVolumes(name, n); err != nil {
		return err
	}
	return b.deleteManagedVolumes(anon)
}

// appInstances returns an app's instances, sorted by name and split into its
// replicas and its companion caretakers. Everything above it that indexes by
// replica ordinal (Status, Logs, Exec, Stats, ForwardPort) uses the replicas
// list only, so adding a companion never shifts a replica index; teardown and
// lifecycle actions use both.
func (b *Backend) appInstances(app string) (replicas, companions []incusapi.Instance, err error) {
	insts, err := b.conn.Instances()
	if err != nil {
		return nil, nil, fmt.Errorf("incus: listing instances: %w", err)
	}
	for _, in := range insts {
		if instanceApp(in) != app {
			continue
		}
		if isCompanion(in) {
			companions = append(companions, in)
		} else {
			replicas = append(replicas, in)
		}
	}
	byName := func(a, b incusapi.Instance) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(replicas, byName)
	slices.SortFunc(companions, byName)
	return replicas, companions, nil
}

// appInstanceNames is appInstances reduced to names, for the callers that only
// act on instances rather than reading their config.
func (b *Backend) appInstanceNames(app string) (replicas, companions []string, err error) {
	r, c, err := b.appInstances(app)
	if err != nil {
		return nil, nil, err
	}
	return instanceNamesOf(r), instanceNamesOf(c), nil
}

func instanceNamesOf(insts []incusapi.Instance) []string {
	out := make([]string, 0, len(insts))
	for _, in := range insts {
		out = append(out, in.Name)
	}
	return out
}

// instanceApp reads the app name an instance belongs to, or "" if it is not
// cornus-managed.
func instanceApp(in incusapi.Instance) string {
	if in.Config[configKeyPrefix+deploy.LabelManaged] != "true" {
		return ""
	}
	return in.Config[configKeyPrefix+deploy.LabelApp]
}
