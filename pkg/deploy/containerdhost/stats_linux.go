//go:build linux

package containerdhost

import (
	"context"
	"fmt"
	"io"
	"time"

	cg1 "github.com/containerd/cgroups/v3/cgroup1/stats"
	cg2 "github.com/containerd/cgroups/v3/cgroup2/stats"
	ctd "github.com/containerd/containerd"
	"github.com/containerd/typeurl/v2"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

// Stats for the containerd backend: the metrics SOURCE. The Docker-JSON wire
// types, the sample->frame projection, the /proc/net/dev reader, the host
// CPU/mem fallbacks, and the streaming loop are shared in hostrun; here we only
// turn a containerd task's cgroup Metrics (v1 or v2) into a hostrun.StatsSample.

// sampleTask reads one metrics sample from the task, normalizing cgroup v1 and
// v2 shapes, and adds the host-side pieces (system CPU, memory-limit fallback,
// per-interface network counters read through the task PID's netns view).
func sampleTask(nctx context.Context, task ctd.Task) (hostrun.StatsSample, error) {
	m, err := task.Metrics(nctx)
	if err != nil {
		return hostrun.StatsSample{}, err
	}
	data, err := typeurl.UnmarshalAny(m.Data)
	if err != nil {
		return hostrun.StatsSample{}, fmt.Errorf("containerd: decode metrics: %w", err)
	}
	s, err := sampleFromMetrics(data)
	if err != nil {
		return hostrun.StatsSample{}, err
	}
	s.Read = time.Now()
	s.SysUsage = hostrun.SystemCPUUsage()
	if s.MemLimit == 0 || s.MemLimit > uint64(1)<<60 {
		// No cgroup limit: report the host's total, docker parity.
		s.MemLimit = hostrun.HostMemTotal()
	}
	// The workload's interfaces live in its pinned netns; the task PID's proc
	// view is the cheapest window into it (no setns required). Only trust the
	// PID while the task is actually Running: runningTask does not verify the
	// state, and a stopped task's init PID may be dead or reused by an
	// unrelated host process whose netns counters would then be misattributed
	// to this container. A non-running task simply reports no network stats.
	if st, err := task.Status(nctx); err == nil && st.Status == ctd.Running {
		s.Networks = hostrun.ReadNetDev(fmt.Sprintf("/proc/%d/net/dev", task.Pid()))
	}
	return s, nil
}

// sampleFromMetrics normalizes a decoded cgroup v1 or v2 metrics message into
// a hostrun.StatsSample. Pure: the caller stamps read time, host CPU usage, the
// memory-limit fallback, and network counters.
func sampleFromMetrics(data any) (hostrun.StatsSample, error) {
	var s hostrun.StatsSample
	switch v := data.(type) {
	case *cg1.Metrics:
		if cpu := v.GetCPU(); cpu != nil && cpu.GetUsage() != nil {
			s.CPUTotal = cpu.GetUsage().GetTotal()
			s.CPUKernel = cpu.GetUsage().GetKernel()
			s.CPUUser = cpu.GetUsage().GetUser()
		}
		if mem := v.GetMemory(); mem != nil {
			if mem.GetUsage() != nil {
				s.MemUsage = mem.GetUsage().GetUsage()
				s.MemLimit = mem.GetUsage().GetLimit()
			}
			s.MemStats = memStatsV1(mem)
		}
		s.Blkio = blkioV1(v.GetBlkio())
		if p := v.GetPids(); p != nil {
			s.Pids = p.GetCurrent()
		}
	case *cg2.Metrics:
		if cpu := v.GetCPU(); cpu != nil {
			// cgroup v2 reports microseconds; docker's shape is nanoseconds.
			s.CPUTotal = cpu.GetUsageUsec() * 1000
			s.CPUKernel = cpu.GetSystemUsec() * 1000
			s.CPUUser = cpu.GetUserUsec() * 1000
		}
		if mem := v.GetMemory(); mem != nil {
			s.MemUsage = mem.GetUsage()
			s.MemLimit = mem.GetUsageLimit()
			s.MemStats = memStatsV2(mem)
		}
		s.Blkio = blkioV2(v.GetIo())
		if p := v.GetPids(); p != nil {
			s.Pids = p.GetCurrent()
		}
	default:
		return hostrun.StatsSample{}, fmt.Errorf("containerd: unsupported metrics type %T", data)
	}
	return s, nil
}

// memStatsV1 maps cgroup v1 memory counters to docker's memory_stats.stats
// keys. total_inactive_file is the key the docker CLI subtracts from usage on
// cgroup v1 (its presence is also how the CLI detects a v1 frame), so it must
// only appear here, never in the v2 map.
func memStatsV1(m *cg1.MemoryStat) map[string]uint64 {
	if m == nil {
		return nil
	}
	return map[string]uint64{
		"cache":               m.GetCache(),
		"rss":                 m.GetRSS(),
		"inactive_file":       m.GetInactiveFile(),
		"active_file":         m.GetActiveFile(),
		"total_cache":         m.GetTotalCache(),
		"total_rss":           m.GetTotalRSS(),
		"total_inactive_file": m.GetTotalInactiveFile(),
		"total_active_file":   m.GetTotalActiveFile(),
		"pgfault":             m.GetPgFault(),
		"pgmajfault":          m.GetPgMajFault(),
	}
}

// memStatsV2 maps cgroup v2 memory.stat counters to docker's memory_stats.stats
// keys (dockerd passes memory.stat keys through verbatim on cgroup v2).
// inactive_file is the key the docker CLI subtracts from usage.
func memStatsV2(m *cg2.MemoryStat) map[string]uint64 {
	if m == nil {
		return nil
	}
	return map[string]uint64{
		"anon":           m.GetAnon(),
		"file":           m.GetFile(),
		"kernel_stack":   m.GetKernelStack(),
		"slab":           m.GetSlab(),
		"sock":           m.GetSock(),
		"shmem":          m.GetShmem(),
		"file_mapped":    m.GetFileMapped(),
		"file_dirty":     m.GetFileDirty(),
		"file_writeback": m.GetFileWriteback(),
		"inactive_anon":  m.GetInactiveAnon(),
		"active_anon":    m.GetActiveAnon(),
		"inactive_file":  m.GetInactiveFile(),
		"active_file":    m.GetActiveFile(),
		"unevictable":    m.GetUnevictable(),
		"pgfault":        m.GetPgfault(),
		"pgmajfault":     m.GetPgmajfault(),
	}
}

// blkioV1 passes cgroup v1 io_service_bytes_recursive entries through (the
// kernel emits "Read"/"Write"/... ops; the docker CLI matches them
// case-insensitively).
func blkioV1(b *cg1.BlkIOStat) []hostrun.DockerBlkioEntry {
	if b == nil {
		return nil
	}
	var out []hostrun.DockerBlkioEntry
	for _, e := range b.GetIoServiceBytesRecursive() {
		out = append(out, hostrun.DockerBlkioEntry{
			Major: e.GetMajor(),
			Minor: e.GetMinor(),
			Op:    e.GetOp(),
			Value: e.GetValue(),
		})
	}
	return out
}

// blkioV2 renders cgroup v2 io.stat usage as docker io_service_bytes_recursive
// read/write entries, the shape dockerd emits on cgroup v2 hosts.
func blkioV2(st *cg2.IOStat) []hostrun.DockerBlkioEntry {
	if st == nil {
		return nil
	}
	var out []hostrun.DockerBlkioEntry
	for _, e := range st.GetUsage() {
		out = append(out,
			hostrun.DockerBlkioEntry{Major: e.GetMajor(), Minor: e.GetMinor(), Op: "read", Value: e.GetRbytes()},
			hostrun.DockerBlkioEntry{Major: e.GetMajor(), Minor: e.GetMinor(), Op: "write", Value: e.GetWbytes()},
		)
	}
	return out
}

// Stats streams Docker-format stats JSON for the deployment's first instance:
// one object then EOF when opts.Stream is false, else one per second until ctx
// ends.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	c, err := b.firstInstance(ctx, name)
	if err != nil {
		return err
	}
	nctx := b.ns(ctx)
	task, err := runningTask(nctx, c)
	if err != nil {
		return err
	}
	return hostrun.StreamStats(ctx, w, c.ID(), name, opts.Stream, func() (hostrun.StatsSample, error) {
		return sampleTask(nctx, task)
	})
}
