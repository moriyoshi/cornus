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

var (
	_ deploy.MetricsSampler      = (*Backend)(nil)
	_ deploy.MetricsCapabilities = (*Backend)(nil)
)

// UnsupportedMetrics implements deploy.MetricsCapabilities.
//
// CPUCores because the source is cumulative (see SampleMetrics), and Disk because
// InstanceState carries no block-I/O counters — sampler() leaves StatsSample.Blkio
// empty, so ToResourceSample never fills DiskRead/DiskWrite for this backend.
func (b *Backend) UnsupportedMetrics() []api.SampleField {
	return []api.SampleField{api.SampleFieldCPUCores, api.SampleFieldDisk}
}

// Stats streams Docker-format stats JSON for the deployment's opts.Instance-th
// replica, translating Incus's structured InstanceState into the shared hostrun
// Docker stats encoder (identical framing to the containerd/bare backends): one
// object then EOF when opts.Stream is false, else one per second.
//
// Incus reports total CPU usage but no host-wide system CPU total, so the CPU
// percentage the docker CLI computes will read low/zero — a documented
// limitation; memory, pids, and per-interface network counters are exact.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	id, err := b.instanceAt(name, opts.Instance)
	if err != nil {
		return err
	}
	return hostrun.StreamStats(ctx, w, id, name, opts.Stream, b.sampler(id))
}

// SampleMetrics implements deploy.MetricsSampler: one reading for one replica,
// in the neutral shape the observability collector records.
//
// CPUTime carries only a Total: Incus reports aggregate CPU usage with no
// user/system split, so those stay zero rather than being invented.
func (b *Backend) SampleMetrics(_ context.Context, name string, instance int) (api.ResourceSample, error) {
	id, err := b.instanceAt(name, instance)
	if err != nil {
		return api.ResourceSample{}, err
	}
	s, err := b.sampler(id)()
	if err != nil {
		return api.ResourceSample{}, err
	}
	return s.ToResourceSample(), nil
}

// sampler returns the per-read closure both Stats and SampleMetrics use.
func (b *Backend) sampler(id string) func() (hostrun.StatsSample, error) {
	return func() (hostrun.StatsSample, error) {
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
}

// firstInstance returns the first (sorted) instance name of an app, or a wrapped
// deploy.ErrNotFound when the app has none. Shared by the first-instance data
// plane methods (Stats/Logs/exec/cp/forwardport).
func (b *Backend) firstInstance(name string) (string, error) {
	return b.instanceAt(name, 0)
}

// instanceAt resolves a deployment's idx-th instance (0-based). appInstanceNames
// returns a stable ordering, so the index is the replica ordinal.
func (b *Backend) instanceAt(name string, idx int) (string, error) {
	// appInstanceNames returns replicas and companions separately; only replicas
	// are addressable by ordinal — a companion is an implementation detail of the
	// replica it serves, not an instance a caller can select.
	insts, _, err := b.appInstanceNames(name)
	if err != nil {
		return "", err
	}
	if idx < 0 || idx >= len(insts) {
		return "", fmt.Errorf("incus: deployment %q has no instance %d (%d running): %w", name, idx, len(insts), deploy.ErrNotFound)
	}
	return insts[idx], nil
}
