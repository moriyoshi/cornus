//go:build linux

package containerdhost

import (
	"encoding/json"
	"fmt"
	"strings"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// The OCI spec-opt library (envList/runtimeOpts/ociBindMount/... ) this file used
// to hold now lives in cornus/pkg/deploy/internal/hostrun (shared with barehost);
// specOpts/ociBindMount/networkNames/specAliases callers use the hostrun.*
// exports. containerLabels below stays here — it is containerd-specific (the
// restart-monitor label set has no barehost analogue, which supervises via its
// own records + monitor).

// containerLabels builds the label set for one instance: cornus ownership and
// network-record labels (the persisted store the hosts-file sync and the startup
// reconcile pass rebuild their state from) plus containerd restart-monitor
// labels. The restart monitor (a stock containerd plugin) resurrects tasks per
// policy with no cornus involvement; logURI keeps monitor-restarted tasks logging
// through the cornus log shim.
//
// ports are the instance's actually-published host ports (nil for replica>0,
// where Apply withholds host-port publishing so portmap DNATs a host port to a
// single instance). They are recorded verbatim so netns repair re-attaches the
// exact same publishing and never installs a conflicting DNAT for a replica.
func containerLabels(spec api.DeploySpec, att hostrun.Attachment, ports []api.PortMapping, logURI string) (map[string]string, error) {
	l := map[string]string{}
	// User labels (compose `labels`) first; cornus's own management/network labels
	// are written afterwards so they always win on a key clash.
	for k, v := range spec.Labels {
		l[k] = v
	}
	l[deploy.LabelManaged] = "true"
	l[deploy.LabelApp] = spec.Name
	for k, v := range deploy.OriginToLabels(spec.Origin) {
		l[k] = v
	}
	if names := hostrun.NetworkNames(spec); len(names) > 0 {
		l[labelNetworks] = strings.Join(names, ",")
	}
	if att.Netns != "" {
		l[labelNetNS] = att.Netns
	}
	if att.IP != "" {
		l[labelIP] = att.IP
	}
	if len(att.IPs) > 0 {
		ips, err := json.Marshal(att.IPs)
		if err != nil {
			return nil, fmt.Errorf("containerd: encode net-IPs label: %w", err)
		}
		l[labelNetIPs] = string(ips)
	}
	if aliases := hostrun.SpecAliases(spec); len(aliases) > 0 {
		data, err := json.Marshal(aliases)
		if err != nil {
			return nil, fmt.Errorf("containerd: encode aliases label: %w", err)
		}
		l[labelAliases] = string(data)
	}
	if len(ports) > 0 {
		data, err := json.Marshal(ports)
		if err != nil {
			return nil, fmt.Errorf("containerd: encode ports label: %w", err)
		}
		l[labelPorts] = string(data)
	}
	// The restart policy word carries the compose deploy.restart_policy.condition
	// mapping already folded into spec.Restart by the planner (none->"no",
	// on-failure->"on-failure", any->"always"). spec.RestartMaxAttempts
	// (deploy.restart_policy.max_attempts) is NOT applied: the containerd restart
	// monitor takes only a policy word, with no per-container retry cap, so an
	// attempt limit has no field here — it is a no-op on this backend.
	policy := deploy.RestartPolicy(spec)
	if _, err := restart.NewPolicy(policy); err != nil {
		return nil, fmt.Errorf("containerd: restart policy %q: %w", policy, err)
	}
	if policy != "no" {
		l[restart.PolicyLabel] = policy
		l[restart.StatusLabel] = string(ctd.Running)
		if logURI != "" {
			l[restart.LogURILabel] = logURI
		}
	}
	return l, nil
}
