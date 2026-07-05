//go:build linux

package containerdhost

import (
	"testing"

	cg1 "github.com/containerd/cgroups/v3/cgroup1/stats"
	cg2 "github.com/containerd/cgroups/v3/cgroup2/stats"
)

// The Docker-JSON encoder tests (parseNetDev / toDockerStats) moved with those
// functions to cornus/pkg/deploy/internal/hostrun. What remains here is the
// containerd-specific metrics SOURCE: normalizing a task's cgroup v1/v2 Metrics
// into a hostrun.StatsSample.

func TestSampleFromMetricsCgroup2(t *testing.T) {
	m := &cg2.Metrics{
		CPU: &cg2.CPUStat{UsageUsec: 1500, UserUsec: 1000, SystemUsec: 500},
		Memory: &cg2.MemoryStat{
			Usage:        4096,
			UsageLimit:   8192,
			InactiveFile: 1024,
			ActiveFile:   512,
			File:         2048,
			Anon:         100,
		},
		Io: &cg2.IOStat{Usage: []*cg2.IOEntry{
			{Major: 8, Minor: 0, Rbytes: 111, Wbytes: 222},
		}},
		Pids: &cg2.PidsStat{Current: 7},
	}
	s, err := sampleFromMetrics(m)
	if err != nil {
		t.Fatalf("sampleFromMetrics: %v", err)
	}
	// usec -> nsec.
	if s.CPUTotal != 1500000 || s.CPUKernel != 500000 || s.CPUUser != 1000000 {
		t.Fatalf("cpu = %d/%d/%d", s.CPUTotal, s.CPUKernel, s.CPUUser)
	}
	if s.MemUsage != 4096 || s.MemLimit != 8192 || s.Pids != 7 {
		t.Fatalf("mem/pids = %d/%d/%d", s.MemUsage, s.MemLimit, s.Pids)
	}
	// inactive_file is what the docker CLI subtracts from usage on cgroup v2.
	if s.MemStats["inactive_file"] != 1024 || s.MemStats["active_file"] != 512 ||
		s.MemStats["file"] != 2048 || s.MemStats["anon"] != 100 {
		t.Fatalf("memStats = %v", s.MemStats)
	}
	// total_inactive_file marks a cgroup v1 frame to the CLI; must be absent.
	if _, ok := s.MemStats["total_inactive_file"]; ok {
		t.Fatal("cgroup v2 memStats must not carry total_inactive_file")
	}
	if len(s.Blkio) != 2 {
		t.Fatalf("blkio = %+v", s.Blkio)
	}
	if s.Blkio[0].Op != "read" || s.Blkio[0].Value != 111 || s.Blkio[0].Major != 8 {
		t.Fatalf("blkio read entry = %+v", s.Blkio[0])
	}
	if s.Blkio[1].Op != "write" || s.Blkio[1].Value != 222 {
		t.Fatalf("blkio write entry = %+v", s.Blkio[1])
	}
}

func TestSampleFromMetricsCgroup1(t *testing.T) {
	m := &cg1.Metrics{
		CPU: &cg1.CPUStat{Usage: &cg1.CPUUsage{Total: 2000, Kernel: 500, User: 1500}},
		Memory: &cg1.MemoryStat{
			Cache:             2048,
			TotalCache:        2048,
			TotalInactiveFile: 1024,
			Usage:             &cg1.MemoryEntry{Usage: 4096, Limit: 8192},
		},
		Blkio: &cg1.BlkIOStat{IoServiceBytesRecursive: []*cg1.BlkIOEntry{
			{Op: "Read", Major: 8, Minor: 0, Value: 111},
			{Op: "Write", Major: 8, Minor: 0, Value: 222},
		}},
		Pids: &cg1.PidsStat{Current: 3},
	}
	s, err := sampleFromMetrics(m)
	if err != nil {
		t.Fatalf("sampleFromMetrics: %v", err)
	}
	if s.CPUTotal != 2000 || s.CPUKernel != 500 || s.CPUUser != 1500 {
		t.Fatalf("cpu = %d/%d/%d", s.CPUTotal, s.CPUKernel, s.CPUUser)
	}
	if s.MemUsage != 4096 || s.MemLimit != 8192 || s.Pids != 3 {
		t.Fatalf("mem/pids = %d/%d/%d", s.MemUsage, s.MemLimit, s.Pids)
	}
	// total_inactive_file is what the docker CLI subtracts from usage on v1.
	if s.MemStats["total_inactive_file"] != 1024 || s.MemStats["total_cache"] != 2048 {
		t.Fatalf("memStats = %v", s.MemStats)
	}
	// v1 entries pass through verbatim, including the kernel's capitalized ops.
	if len(s.Blkio) != 2 || s.Blkio[0].Op != "Read" || s.Blkio[0].Value != 111 ||
		s.Blkio[1].Op != "Write" || s.Blkio[1].Value != 222 {
		t.Fatalf("blkio = %+v", s.Blkio)
	}
}

func TestSampleFromMetricsUnsupportedType(t *testing.T) {
	if _, err := sampleFromMetrics(struct{}{}); err == nil {
		t.Fatal("unsupported metrics type must error")
	}
}
