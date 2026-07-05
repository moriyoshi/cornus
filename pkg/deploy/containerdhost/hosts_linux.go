//go:build linux

package containerdhost

// Inter-container name resolution: the shared hosts-file machinery lives in
// cornus/pkg/deploy/internal/hostrun (HostsStore + SyncHosts). The only
// containerd-specific part is the peer source — the map is rebuilt from
// container LABELS (labelNetworks, labelNetIPs, labelAliases) each time
// (barehost reads its instance records instead).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// peerFromLabels decodes an instance's persisted network labels into a peer.
func peerFromLabels(id string, labels map[string]string) hostrun.HostsPeer {
	p := hostrun.HostsPeer{ID: id, App: labels[deploy.LabelApp], Replica: replicaIndex(id)}
	for _, n := range strings.Split(labels[labelNetworks], ",") {
		if n != "" {
			p.Networks = append(p.Networks, n)
		}
	}
	if len(p.Networks) == 0 {
		p.Networks = []string{hostrun.DefaultNetwork}
	}
	if raw := labels[labelNetIPs]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &p.IPs)
	}
	if p.IPs == nil {
		p.IPs = map[string]string{}
	}
	// Instances recorded before per-network IPs existed carry only the primary
	// IP; use it for every network (they had a single network in practice).
	if len(p.IPs) == 0 {
		if ip := labels[labelIP]; ip != "" {
			for _, n := range p.Networks {
				p.IPs[n] = ip
			}
		}
	}
	if raw := labels[labelAliases]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &p.Aliases)
	}
	return p
}

// replicaIndex parses the trailing replica ordinal of an instance ID
// ("cornus-<app>-<i>"); unparseable IDs sort last so a well-formed replica always
// wins the replica-0 pick.
func replicaIndex(id string) int {
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return math.MaxInt
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 0 {
		return math.MaxInt
	}
	return n
}

// syncHosts rewrites the managed /etc/hosts block of every instance so services
// resolve each other by name (and aliases) on shared networks, from the
// containerd backend's container labels.
func (b *Backend) syncHosts(ctx context.Context) error {
	nctx := b.ns(ctx)
	cs, err := b.client.Containers(nctx, fmt.Sprintf(`labels.%q==%q`, deploy.LabelManaged, "true"))
	if err != nil {
		return fmt.Errorf("containerd: hosts sync: list managed containers: %w", err)
	}
	var peers []hostrun.HostsPeer
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil {
			continue
		}
		if p := peerFromLabels(c.ID(), labels); p.App != "" {
			peers = append(peers, p)
		}
	}
	return hostrun.SyncHosts(b.hosts, peers)
}
