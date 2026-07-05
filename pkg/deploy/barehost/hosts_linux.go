//go:build linux

package barehost

// Inter-container name resolution: the shared hosts-file machinery lives in
// cornus/pkg/deploy/internal/hostrun (HostsStore + SyncHosts). The only
// bare-specific part is the peer source — the instance RECORD store (containerd
// reads container labels instead).

import (
	"fmt"

	"cornus/pkg/deploy/internal/hostrun"
)

// peerFromRecord decodes an instance's persisted network fields into a peer.
func peerFromRecord(rec *instanceRecord) hostrun.HostsPeer {
	p := hostrun.HostsPeer{
		ID:       rec.ID,
		App:      rec.App,
		Replica:  rec.Replica,
		Networks: rec.Networks,
		IPs:      rec.NetIPs,
		Aliases:  rec.Aliases,
	}
	if len(p.Networks) == 0 {
		p.Networks = []string{hostrun.DefaultNetwork}
	}
	if p.IPs == nil {
		p.IPs = map[string]string{}
	}
	// Fall back to the primary IP for every network when per-network IPs are
	// absent (a single-network instance).
	if len(p.IPs) == 0 && rec.IP != "" {
		for _, n := range p.Networks {
			p.IPs[n] = rec.IP
		}
	}
	return p
}

// syncHosts rewrites the managed /etc/hosts block of every instance so services
// resolve each other by name (and aliases) on shared networks, from the bare
// backend's instance records.
func (b *Backend) syncHosts() error {
	recs, err := b.listRecords()
	if err != nil {
		return fmt.Errorf("bare: hosts sync: %w", err)
	}
	var peers []hostrun.HostsPeer
	for _, rec := range recs {
		if rec.App != "" {
			peers = append(peers, peerFromRecord(rec))
		}
	}
	return hostrun.SyncHosts(b.hosts, peers)
}
