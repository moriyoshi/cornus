//go:build linux

package barehost

// The translation layer between go-runc's types and the backend-local ones. It
// is the only place a unit mix-up (microseconds for nanoseconds, a dropped
// device counter) can hide, and it is pure — no runtime binary involved.

import (
	"testing"

	runc "github.com/containerd/go-runc"
)

func TestToRuntimeStateCarriesTheFieldsTheBackendActsOn(t *testing.T) {
	got := toRuntimeState(&runc.Container{
		ID: "cornus-web-0", Pid: 4242, Status: runcStateRunning,
		Bundle: "/data/bare/bundles/cornus-web-0", Rootfs: "ignored",
	})
	want := runtimeState{ID: "cornus-web-0", Pid: 4242, Status: runcStateRunning, Bundle: "/data/bare/bundles/cornus-web-0"}
	if got != want {
		t.Errorf("toRuntimeState = %+v, want %+v", got, want)
	}
}

// TestToRuntimeStatsNilIsAZeroSample guards the Stats caller, which passes
// through whatever go-runc returned: a nil must not panic the metrics path.
func TestToRuntimeStatsNilIsAZeroSample(t *testing.T) {
	if got := toRuntimeStats(nil); got.CPUTotal != 0 || got.MemUsage != 0 || got.Blkio != nil {
		t.Errorf("toRuntimeStats(nil) = %+v, want the zero sample", got)
	}
}

// TestToRuntimeStatsMapsEveryConsumedCounter pins the field wiring, including
// the per-device block-IO list the Docker encoder renders.
func TestToRuntimeStatsMapsEveryConsumedCounter(t *testing.T) {
	in := &runc.Stats{
		Cpu: runc.Cpu{Usage: runc.CpuUsage{Total: 9_000, User: 6_000, Kernel: 3_000}},
		Memory: runc.Memory{
			Usage: runc.MemoryEntry{Usage: 4 << 20, Limit: 16 << 20},
			Raw:   map[string]uint64{"rss": 1 << 20},
		},
		Pids: runc.Pids{Current: 11, Limit: 100},
		Blkio: runc.Blkio{IoServiceBytesRecursive: []runc.BlkioEntry{
			{Major: 8, Minor: 0, Op: "Read", Value: 512},
			{Major: 8, Minor: 0, Op: "Write", Value: 1024},
		}},
	}
	got := toRuntimeStats(in)
	if got.CPUTotal != 9_000 || got.CPUUser != 6_000 || got.CPUKernel != 3_000 {
		t.Errorf("cpu = %d/%d/%d, want 9000/6000/3000 nanoseconds", got.CPUTotal, got.CPUUser, got.CPUKernel)
	}
	if got.MemUsage != 4<<20 || got.MemLimit != 16<<20 {
		t.Errorf("memory = %d/%d", got.MemUsage, got.MemLimit)
	}
	if got.MemStats["rss"] != 1<<20 {
		t.Errorf("raw memory stats not carried: %v", got.MemStats)
	}
	if got.Pids != 11 {
		t.Errorf("pids = %d, want the current count (not the limit)", got.Pids)
	}
	if len(got.Blkio) != 2 || got.Blkio[0].Op != "Read" || got.Blkio[0].Value != 512 || got.Blkio[1].Value != 1024 {
		t.Errorf("blkio = %+v, want both device counters", got.Blkio)
	}
	// Only the recursive service-bytes list feeds the Docker shape; the other
	// go-runc lists are deliberately not merged in.
	if len(got.Blkio) > 2 {
		t.Errorf("blkio = %+v, want only ioServiceBytesRecursive", got.Blkio)
	}
}
