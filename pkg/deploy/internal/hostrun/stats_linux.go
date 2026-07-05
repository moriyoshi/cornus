//go:build linux

package hostrun

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
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

// ReadNetDev parses the /proc/<pid>/net/dev-style file at path into docker
// per-interface counters. Best-effort: any error yields nil and the stats
// frame simply omits networks.
func ReadNetDev(path string) map[string]DockerNetStats {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseNetDev(f)
}

// parseNetDev parses /proc/net/dev content: two header lines, then one line
// per interface carrying 16 counters (8 receive, 8 transmit). The loopback
// interface is excluded, docker parity.
func parseNetDev(r io.Reader) map[string]DockerNetStats {
	out := map[string]DockerNetStats{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			continue
		}
		n := make([]uint64, 16)
		for i := range n {
			n[i], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		out[name] = DockerNetStats{
			RxBytes:   n[0],
			RxPackets: n[1],
			RxErrors:  n[2],
			RxDropped: n[3],
			TxBytes:   n[8],
			TxPackets: n[9],
			TxErrors:  n[10],
			TxDropped: n[11],
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SystemCPUUsage reads the host's cumulative CPU time from /proc/stat in
// nanoseconds (docker's system_cpu_usage semantics; jiffies at USER_HZ=100).
func SystemCPUUsage() uint64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "cpu" {
			continue
		}
		var total uint64
		for _, v := range fields[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			total += n
		}
		return total * (1e9 / 100)
	}
	return 0
}

// HostMemTotal reads MemTotal from /proc/meminfo in bytes.
func HostMemTotal() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb * 1024
		}
	}
	return 0
}
