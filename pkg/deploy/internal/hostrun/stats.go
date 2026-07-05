// Docker-format container stats: the shared, daemon-agnostic ENCODER half of
// `docker stats`, plus the neutral sample type every backend fills in.
//
// This file carries NO build tag on purpose. The encoder and the sample type are
// pure data shaping, but the package they used to live in was Linux-only, which
// made them unreachable from the kubernetes backend — a backend that has to
// compile everywhere and whose metrics come from an HTTP API rather than from
// cgroups. The procfs readers that genuinely need Linux stay in stats_linux.go.
//
// Only the metrics SOURCE differs between backends (containerd reads
// task.Metrics; the bare backend parses the cgroup files directly, avoiding the
// cgroup MANAGER libraries that would drag cilium/ebpf + dbus into the
// buildkit-free deploy tree; kubernetes asks metrics-server).

package hostrun

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"time"

	"cornus/pkg/api"
)

// Docker-format container stats: the shared, daemon-agnostic ENCODER half of
// `docker stats`. Both host backends produce identical Docker JSON frames from a
// StatsSample; only the metrics SOURCE differs (containerd reads task.Metrics;
// the bare backend parses the cgroup files directly, avoiding the cgroup MANAGER
// libraries that would drag cilium/ebpf + dbus into the buildkit-free deploy
// tree). This file owns the wire types, the sample->frame projection, the
// /proc/<pid>/net/dev reader, the host CPU/mem fallbacks, and the streaming loop.

// DockerStats is the subset of Docker's StatsJSON shape the docker CLI needs
// (hand-rolled json tags; importing docker's types would drag its module in).
type DockerStats struct {
	Read     time.Time                 `json:"read"`
	Preread  time.Time                 `json:"preread"`
	ID       string                    `json:"id"`
	Name     string                    `json:"name"`
	NumProcs uint32                    `json:"num_procs"`
	CPU      DockerCPUStats            `json:"cpu_stats"`
	PreCPU   DockerCPUStats            `json:"precpu_stats"`
	Memory   DockerMemStats            `json:"memory_stats"`
	Pids     DockerPidStats            `json:"pids_stats"`
	Blkio    DockerBlkioStats          `json:"blkio_stats"`
	Networks map[string]DockerNetStats `json:"networks,omitempty"`
}

type DockerCPUStats struct {
	Usage       DockerCPUUsage `json:"cpu_usage"`
	SystemUsage uint64         `json:"system_cpu_usage"`
	OnlineCPUs  uint32         `json:"online_cpus"`
}

type DockerCPUUsage struct {
	Total  uint64 `json:"total_usage"`
	Kernel uint64 `json:"usage_in_kernelmode"`
	User   uint64 `json:"usage_in_usermode"`
}

// DockerMemStats carries usage/limit plus the per-counter stats map the docker
// CLI needs to compute "used" memory: it subtracts total_inactive_file (cgroup
// v1; the key's presence also marks the frame as v1) or inactive_file (cgroup
// v2) from usage. Without the map the CLI overstates MEM by the page cache.
type DockerMemStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats,omitempty"`
}

type DockerPidStats struct {
	Current uint64 `json:"current"`
}

// DockerBlkioStats mirrors docker's blkio_stats; the CLI renders BLOCK I/O
// from io_service_bytes_recursive alone.
type DockerBlkioStats struct {
	IoServiceBytesRecursive []DockerBlkioEntry `json:"io_service_bytes_recursive"`
}

type DockerBlkioEntry struct {
	Major uint64 `json:"major"`
	Minor uint64 `json:"minor"`
	Op    string `json:"op"`
	Value uint64 `json:"value"`
}

// DockerNetStats mirrors docker's per-interface network counters.
type DockerNetStats struct {
	RxBytes   uint64 `json:"rx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxBytes   uint64 `json:"tx_bytes"`
	TxPackets uint64 `json:"tx_packets"`
	TxErrors  uint64 `json:"tx_errors"`
	TxDropped uint64 `json:"tx_dropped"`
}

// StatsSample is one normalized metrics reading. The backend-specific sampler
// fills the cgroup-derived counters; the caller stamps Read, SysUsage, the
// MemLimit fallback, and Networks (see StreamStats usage in each backend).
type StatsSample struct {
	Read      time.Time
	CPUTotal  uint64
	CPUKernel uint64
	CPUUser   uint64
	SysUsage  uint64
	MemUsage  uint64
	MemLimit  uint64
	MemStats  map[string]uint64
	Pids      uint64
	Blkio     []DockerBlkioEntry
	Networks  map[string]DockerNetStats
}

// ToResourceSample projects a cgroup-derived sample onto the backend-neutral
// shape the observability collector records.
//
// The three host backends fill StatsSample identically, so this conversion is
// written once here rather than three times in the backends. Fields the sample
// does not carry are left absent, not zeroed: Networks stays nil when the
// sampler could not read them (a gVisor instance, whose guest netstack the host
// cannot see), because "no interfaces observed" and "no bytes moved" are
// different claims and only the second should produce a series reading zero.
func (s StatsSample) ToResourceSample() api.ResourceSample {
	out := api.ResourceSample{
		Time: s.Read,
		CPUTime: &api.CPUTime{
			Total:  s.CPUTotal,
			User:   s.CPUUser,
			System: s.CPUKernel,
		},
		MemUsage: workingSet(s.MemUsage, s.MemStats),
		MemLimit: s.MemLimit,
		Pids:     s.Pids,
	}
	if len(s.Networks) > 0 {
		out.Networks = make(map[string]api.NetCounters, len(s.Networks))
		for iface, n := range s.Networks {
			out.Networks[iface] = api.NetCounters{
				RxBytes:   n.RxBytes,
				RxPackets: n.RxPackets,
				TxBytes:   n.TxBytes,
				TxPackets: n.TxPackets,
			}
		}
	}
	// Blkio is Docker's recursive per-device list; the neutral shape wants the
	// two totals a reader actually asks for. "Read"/"Write" are the only ops the
	// docker CLI itself sums, and the sync/async breakdown would double-count.
	var read, write uint64
	var sawIO bool
	for _, e := range s.Blkio {
		switch e.Op {
		case "read", "Read":
			read += e.Value
			sawIO = true
		case "write", "Write":
			write += e.Value
			sawIO = true
		}
	}
	if sawIO {
		out.DiskRead, out.DiskWrite = &read, &write
	}
	return out
}

// ToResourceSample projects a Docker stats frame onto the neutral shape.
//
// This is the dockerhost path: the daemon is the sampler, so the reading arrives
// already in Docker's format rather than being built from cgroup files. Going
// through the same neutral type as the cgroup backends is what keeps a recorded
// series comparable across backends.
//
// Only `cur` matters. Docker's frame carries a previous sample so the CLI can
// compute a percentage, but the recorded metric is the cumulative counter — the
// derivative belongs to whoever queries it, and taking it here would throw away
// the ability to rate() over a window of the reader's choosing.
func (d DockerStats) ToResourceSample() api.ResourceSample {
	out := api.ResourceSample{
		Time: d.Read,
		CPUTime: &api.CPUTime{
			Total:  d.CPU.Usage.Total,
			User:   d.CPU.Usage.User,
			System: d.CPU.Usage.Kernel,
		},
		MemUsage: workingSet(d.Memory.Usage, d.Memory.Stats),
		MemLimit: d.Memory.Limit,
		Pids:     d.Pids.Current,
	}
	if len(d.Networks) > 0 {
		out.Networks = make(map[string]api.NetCounters, len(d.Networks))
		for iface, n := range d.Networks {
			out.Networks[iface] = api.NetCounters{
				RxBytes:   n.RxBytes,
				RxPackets: n.RxPackets,
				TxBytes:   n.TxBytes,
				TxPackets: n.TxPackets,
			}
		}
	}
	var read, write uint64
	var sawIO bool
	for _, e := range d.Blkio.IoServiceBytesRecursive {
		switch e.Op {
		case "read", "Read":
			read += e.Value
			sawIO = true
		case "write", "Write":
			write += e.Value
			sawIO = true
		}
	}
	if sawIO {
		out.DiskRead, out.DiskWrite = &read, &write
	}
	return out
}

// workingSet applies docker's own "used memory" rule: subtract the reclaimable
// page cache from the cgroup's reported usage.
//
// Without it the recorded number overstates memory by the file cache, sometimes
// by an order of magnitude on an I/O-heavy workload, and — worse — would disagree
// with the number the same operator sees in `docker stats` for the same
// container at the same moment. The key that is present also identifies the
// cgroup version (v1 spells it total_inactive_file, v2 inactive_file).
func workingSet(usage uint64, stats map[string]uint64) uint64 {
	for _, k := range []string{"total_inactive_file", "inactive_file"} {
		if v, ok := stats[k]; ok {
			if v > usage {
				return 0
			}
			return usage - v
		}
	}
	return usage
}

// ToDockerStats renders a pair of samples as one Docker stats frame.
func ToDockerStats(id, name string, prev, cur StatsSample) DockerStats {
	return DockerStats{
		Read:    cur.Read,
		Preread: prev.Read,
		ID:      id,
		Name:    "/" + name,
		CPU: DockerCPUStats{
			Usage:       DockerCPUUsage{Total: cur.CPUTotal, Kernel: cur.CPUKernel, User: cur.CPUUser},
			SystemUsage: cur.SysUsage,
			OnlineCPUs:  uint32(runtime.NumCPU()),
		},
		PreCPU: DockerCPUStats{
			Usage:       DockerCPUUsage{Total: prev.CPUTotal, Kernel: prev.CPUKernel, User: prev.CPUUser},
			SystemUsage: prev.SysUsage,
			OnlineCPUs:  uint32(runtime.NumCPU()),
		},
		Memory:   DockerMemStats{Usage: cur.MemUsage, Limit: cur.MemLimit, Stats: cur.MemStats},
		Pids:     DockerPidStats{Current: cur.Pids},
		Blkio:    DockerBlkioStats{IoServiceBytesRecursive: cur.Blkio},
		Networks: cur.Networks,
	}
}

// StreamStats writes Docker-format stats JSON for one instance to w: a single
// frame then EOF when stream is false (docker --no-stream semantics: the CLI
// shows 0% CPU for the first frame, so precpu is zeroed), else one frame per
// second until ctx ends. sample reads one normalized metrics sample each call.
func StreamStats(ctx context.Context, w io.Writer, id, name string, stream bool, sample func() (StatsSample, error)) error {
	enc := json.NewEncoder(w)
	prev, err := sample()
	if err != nil {
		return err
	}
	if !stream {
		return enc.Encode(ToDockerStats(id, name, StatsSample{}, prev))
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		cur, err := sample()
		if err != nil {
			return err
		}
		if err := enc.Encode(ToDockerStats(id, name, prev, cur)); err != nil {
			return err
		}
		prev = cur
	}
}
