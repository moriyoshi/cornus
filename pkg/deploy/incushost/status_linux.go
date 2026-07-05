//go:build linux

package incushost

import (
	"context"
	"fmt"
	"sort"
	"strings"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// imageConfigKey records a deployment's image reference on each instance so
// Status/List can report it (Incus does not surface the OCI ref otherwise).
const imageConfigKey = configKeyPrefix + "cornus.image"

// Status reports the observed state of the named deployment. A name with no
// live instances is not an error: the status simply has no Instances. Instance
// State strings are Incus's own vocabulary (running/stopped/frozen/error,
// lowercased); only running (with Running==true) is portable.
//
// Companion caretakers are excluded: they are cornus's own infrastructure, not
// replicas of the workload. Reporting them would inflate the replica count a
// caller sees and shift the ordinals every replica-indexed operation (--instance
// on logs/exec/stats) resolves against.
func (b *Backend) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	insts, err := b.conn.Instances()
	if err != nil {
		return api.DeployStatus{}, fmt.Errorf("incus: listing instances: %w", err)
	}
	var mine []incusapi.Instance
	for _, in := range insts {
		if instanceApp(in) == name && !isCompanion(in) {
			mine = append(mine, in)
		}
	}
	return deployStatus(name, mine, b.health.State), nil
}

// List reports all cornus-managed deployments, one per distinct app.
func (b *Backend) List(ctx context.Context) ([]api.DeployStatus, error) {
	insts, err := b.conn.Instances()
	if err != nil {
		return nil, fmt.Errorf("incus: listing instances: %w", err)
	}
	byApp := map[string][]incusapi.Instance{}
	for _, in := range insts {
		if isCompanion(in) {
			continue // cornus infrastructure, not a replica — see Status
		}
		if app := instanceApp(in); app != "" {
			byApp[app] = append(byApp[app], in)
		}
	}
	apps := make([]string, 0, len(byApp))
	for app := range byApp {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	out := make([]api.DeployStatus, 0, len(apps))
	for _, app := range apps {
		out = append(out, deployStatus(app, byApp[app], b.health.State))
	}
	return out, nil
}

// deployStatus assembles a DeployStatus from an app's instances (sorted by name
// so replica order is stable).
// healthOf reports an instance's probe state; nil means this backend reports no
// health, which is the "" api.InstanceStatus.Health already means when absent.
// Passed in rather than reached for, because deployStatus is a pure function two
// callers share and a test builds directly.
func deployStatus(name string, insts []incusapi.Instance, healthOf func(string) string) api.DeployStatus {
	sort.Slice(insts, func(i, j int) bool { return insts[i].Name < insts[j].Name })
	st := api.DeployStatus{Name: name, Backend: "incus"}
	for _, in := range insts {
		if st.Image == "" {
			st.Image = in.Config[imageConfigKey]
		}
		if st.Origin == nil {
			st.Origin = originFromConfig(in.Config)
		}
		st.Instances = append(st.Instances, api.InstanceStatus{
			ID:      in.Name,
			State:   strings.ToLower(in.Status),
			Running: in.StatusCode == incusapi.Running,
			Health:  instHealth(healthOf, in.Name),
		})
	}
	return st
}

// originFromConfig reconstructs the deployment origin from an instance's
// cornus.origin.* config keys (the user.-prefixed inverse of
// deploy.OriginToLabels stamped at create time).
func originFromConfig(config map[string]string) *api.Origin {
	m := map[string]string{}
	for k, v := range config {
		if strings.HasPrefix(k, configKeyPrefix) {
			m[strings.TrimPrefix(k, configKeyPrefix)] = v
		}
	}
	return deploy.OriginFromLabels(m)
}

// instHealth applies healthOf when there is one.
func instHealth(healthOf func(string) string, name string) string {
	if healthOf == nil {
		return ""
	}
	return healthOf(name)
}
