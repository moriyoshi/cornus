//go:build linux

package incushost

import (
	"context"
	"fmt"
	"io"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// Stats streams Docker-format stats JSON for the deployment's first instance,
// translating Incus's structured InstanceState into the shared hostrun Docker
// stats encoder (identical framing to the containerd/bare backends): one object
// then EOF when opts.Stream is false, else one per second.
//
// Incus reports total CPU usage but no host-wide system CPU total, so the CPU
// percentage the docker CLI computes will read low/zero — a documented
// limitation; memory, pids, and per-interface network counters are exact.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	id, err := b.firstInstance(name)
	if err != nil {
		return err
	}
	sample := func() (hostrun.StatsSample, error) {
		st, err := b.conn.InstanceState(id)
		if err != nil {
			return hostrun.StatsSample{}, fmt.Errorf("incus: reading instance state: %w", err)
		}
		if st == nil {
			return hostrun.StatsSample{}, fmt.Errorf("incus: instance %q: %w", id, deploy.ErrNotFound)
		}
		s := hostrun.StatsSample{
			Read:     time.Now(),
			CPUTotal: uint64(st.CPU.Usage),
			MemUsage: uint64(st.Memory.Usage),
			MemLimit: uint64(st.Memory.Total),
			Pids:     uint64(st.Processes),
			Networks: map[string]hostrun.DockerNetStats{},
		}
		for iface, n := range st.Network {
			c := n.Counters
			s.Networks[iface] = hostrun.DockerNetStats{
				RxBytes:   uint64(c.BytesReceived),
				RxPackets: uint64(c.PacketsReceived),
				RxErrors:  uint64(c.ErrorsReceived),
				RxDropped: uint64(c.PacketsDroppedInbound),
				TxBytes:   uint64(c.BytesSent),
				TxPackets: uint64(c.PacketsSent),
				TxErrors:  uint64(c.ErrorsSent),
				TxDropped: uint64(c.PacketsDroppedOutbound),
			}
		}
		return s, nil
	}
	return hostrun.StreamStats(ctx, w, id, name, opts.Stream, sample)
}

// firstInstance returns the first (sorted) instance name of an app, or a wrapped
// deploy.ErrNotFound when the app has none. Shared by the first-instance data
// plane methods (Stats/Logs/exec/cp/forwardport).
func (b *Backend) firstInstance(name string) (string, error) {
	insts, err := b.appInstanceNames(name)
	if err != nil {
		return "", err
	}
	if len(insts) == 0 {
		return "", fmt.Errorf("incus: deployment %q: %w", name, deploy.ErrNotFound)
	}
	return insts[0], nil
}
