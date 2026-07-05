//go:build linux

package incushost

import (
	"context"
	"fmt"
	"sort"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// Apply converges the deployment to spec with recreate-on-Apply semantics: any
// existing instances for the app are torn down, then Replicas(spec) fresh
// instances are created and started. Published host ports go to replica 0 only.
func (b *Backend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	if err := b.policy.Validate("incus", spec); err != nil {
		return api.DeployStatus{}, err
	}
	// Tear down any existing instances for this app (recreate semantics).
	if err := b.deleteApp(ctx, spec.Name); err != nil {
		return api.DeployStatus{}, err
	}
	n := deploy.Replicas(spec)
	for i := 0; i < n; i++ {
		post, err := b.buildInstancesPost(ctx, spec, i)
		if err != nil {
			return api.DeployStatus{}, err
		}
		if err := b.conn.CreateInstance(post); err != nil {
			return api.DeployStatus{}, fmt.Errorf("incus: creating instance %s: %w", post.Name, err)
		}
	}
	return b.Status(ctx, spec.Name)
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

// actOnApp applies a lifecycle action to every instance of an app. It wraps
// deploy.ErrNotFound when the app has no instances (Start/Stop/Restart contract).
func (b *Backend) actOnApp(ctx context.Context, name, action string, force bool) error {
	insts, err := b.appInstanceNames(name)
	if err != nil {
		return err
	}
	if len(insts) == 0 {
		return fmt.Errorf("incus: deployment %q: %w", name, deploy.ErrNotFound)
	}
	for _, in := range insts {
		if err := b.conn.SetInstanceState(in, action, force, 0); err != nil {
			return err
		}
	}
	return nil
}

// deleteApp stops (best-effort) then deletes every instance of an app. Returns
// nil when the app has no instances (delete-if-exists).
func (b *Backend) deleteApp(ctx context.Context, name string) error {
	insts, err := b.appInstanceNames(name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		// Incus refuses to delete a running instance; stop first (best-effort —
		// an already-stopped instance errors, which we ignore).
		_ = b.conn.SetInstanceState(in, "stop", true, 0)
		if err := b.conn.DeleteInstance(in); err != nil {
			return fmt.Errorf("incus: deleting instance %s: %w", in, err)
		}
	}
	return nil
}

// appInstanceNames returns the sorted instance names belonging to app, keyed off
// the user.cornus.app config value stamped at create time.
func (b *Backend) appInstanceNames(app string) ([]string, error) {
	insts, err := b.conn.Instances()
	if err != nil {
		return nil, fmt.Errorf("incus: listing instances: %w", err)
	}
	var names []string
	for _, in := range insts {
		if instanceApp(in) == app {
			names = append(names, in.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// instanceApp reads the app name an instance belongs to, or "" if it is not
// cornus-managed.
func instanceApp(in incusapi.Instance) string {
	if in.Config[configKeyPrefix+deploy.LabelManaged] != "true" {
		return ""
	}
	return in.Config[configKeyPrefix+deploy.LabelApp]
}
