//go:build linux

package barehost

// The two Stats entry points and the sampler behind them. The cgroup FILE
// parsing is covered by stats_linux_test.go against synthetic files; what is
// pinned here is the dispatch: which replica is sampled, which source is chosen,
// and what each entry point refuses.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// TestStatsEmitsOneDockerFrameThenEnds pins the --no-stream contract clients
// rely on to terminate, and that the sampled numbers actually reach the wire.
func TestStatsEmitsOneDockerFrameThenEnds(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true // read the runtime's own accounting: no cgroup files needed
	seedRunnableInstance(t, b, rt, "web", 0)
	rt.stats = runtimeStats{CPUTotal: 9_000, MemUsage: 4 << 20, MemLimit: 16 << 20, Pids: 5}

	var buf bytes.Buffer
	if err := b.Stats(t.Context(), "web", api.StatsOptions{}, &buf); err != nil {
		t.Fatalf("Stats: %v", err)
	}

	dec := json.NewDecoder(&buf)
	var frame hostrun.DockerStats
	if err := dec.Decode(&frame); err != nil {
		t.Fatalf("decode stats frame: %v", err)
	}
	// Docker's shape names the container with a leading slash.
	if frame.ID != instanceName("web", 0) || frame.Name != "/web" {
		t.Errorf("frame identifies %q/%q, want the instance and deployment", frame.ID, frame.Name)
	}
	if frame.Memory.Usage != 4<<20 || frame.Memory.Limit != 16<<20 {
		t.Errorf("memory = %d/%d, want the sampled values", frame.Memory.Usage, frame.Memory.Limit)
	}
	if frame.CPU.Usage.Total != 9_000 {
		t.Errorf("cpu total = %d, want 9000", frame.CPU.Usage.Total)
	}
	if frame.Pids.Current != 5 {
		t.Errorf("pids = %d, want 5", frame.Pids.Current)
	}
	if err := dec.Decode(&frame); err != io.EOF {
		t.Errorf("a non-streaming Stats must end after one frame, got %v", err)
	}
}

// TestStatsSelectsTheRequestedReplica is the reason StatsOptions.Instance
// exists: without it a per-replica collector records replica 0's numbers N times
// under N different ordinals, which reads as a perfectly balanced workload.
func TestStatsSelectsTheRequestedReplica(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	seedRunnableInstance(t, b, rt, "web", 0)
	seedRunnableInstance(t, b, rt, "web", 1)

	var buf bytes.Buffer
	if err := b.Stats(t.Context(), "web", api.StatsOptions{Instance: 1}, &buf); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var frame hostrun.DockerStats
	if err := json.NewDecoder(&buf).Decode(&frame); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if frame.ID != instanceName("web", 1) {
		t.Errorf("sampled %q, want replica 1", frame.ID)
	}
}

// TestStatsRejectsAnOutOfRangeReplica pins that a departed replica is an error,
// never a silent fallback to the first — a caller enumerating replicas must be
// able to tell one from another.
func TestStatsRejectsAnOutOfRangeReplica(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	seedRunnableInstance(t, b, rt, "web", 0)

	err := b.Stats(t.Context(), "web", api.StatsOptions{Instance: 3}, io.Discard)
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("Stats for a nonexistent replica = %v, want a wrap of ErrNotFound", err)
	}
}

// TestSampleMetricsReportsTheNeutralShape covers the collector's entry point,
// including the absent/zero distinction that shape exists for.
func TestSampleMetricsReportsTheNeutralShape(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	seedRunnableInstance(t, b, rt, "web", 0)
	rt.stats = runtimeStats{CPUTotal: 1_500, CPUUser: 900, CPUKernel: 600, MemUsage: 8 << 20, MemLimit: 32 << 20, Pids: 4}

	s, err := b.SampleMetrics(t.Context(), "web", 0)
	if err != nil {
		t.Fatalf("SampleMetrics: %v", err)
	}
	if s.MemUsage != 8<<20 || s.MemLimit != 32<<20 || s.Pids != 4 {
		t.Errorf("sample = %+v, want the runtime's memory and pid readings", s)
	}
	if s.CPUTime == nil {
		t.Fatal("the bare backend reports cumulative CPU time; CPUTime must not be nil")
	}
	if s.Time.IsZero() {
		t.Error("the sample carries no timestamp")
	}
	// A gVisor guest's netstack is invisible from the host, so Networks must stay
	// nil — "cannot observe" is a different claim from "moved no bytes".
	if s.Networks != nil {
		t.Errorf("networks = %v, want nil for a sandboxed runtime", s.Networks)
	}
}

func TestSampleMetricsRefusesANonRunningInstance(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	seedInstance(t, b, rt, "web", 0, false)

	if _, err := b.SampleMetrics(t.Context(), "web", 0); err == nil {
		t.Error("SampleMetrics on a created-but-not-running instance: want error")
	}
}

// TestSamplerChoosesTheSourceFromTheRuntime pins the routing decision: a
// cgroupfs runtime reads the cgroup files (and so refuses an instance with no
// live init), while a sandboxed one asks the runtime.
func TestSamplerChoosesTheSourceFromTheRuntime(t *testing.T) {
	ctx := t.Context()

	t.Run("cgroup source needs a live init", func(t *testing.T) {
		b, rt := newTestBackend(t) // sandboxed=false
		seedInstance(t, b, rt, "web", 0, false)
		if _, err := b.sampler(ctx, instanceName("web", 0))(); err == nil {
			t.Error("the cgroup sampler must refuse an instance with no running init")
		}
		// The runtime's own Stats must not have been consulted at all.
		for _, c := range rt.calls {
			if c == "stats:"+instanceName("web", 0) {
				t.Errorf("a cgroupfs runtime must never call runtime-native Stats; calls=%v", rt.calls)
			}
		}
	})

	t.Run("sandboxed source asks the runtime", func(t *testing.T) {
		b, rt := newTestBackend(t)
		b.sandboxed = true
		seedRunnableInstance(t, b, rt, "web", 0)
		rt.stats = runtimeStats{MemUsage: 123}
		s, err := b.sampler(ctx, instanceName("web", 0))()
		if err != nil {
			t.Fatalf("sampler: %v", err)
		}
		if s.MemUsage != 123 {
			t.Errorf("sample = %+v, want the runtime's reading", s)
		}
	})
}

// TestSampleCgroup1WithoutV1ControllerPaths covers the v1 reader's graceful
// answer on a host (or process) that has no v1 hierarchy: every counter simply
// stays zero rather than the read failing.
func TestSampleCgroup1WithoutV1ControllerPaths(t *testing.T) {
	// A pure cgroup v2 /proc/<pid>/cgroup: no controller lines for the v1 reader
	// to resolve, whatever v1 mounts the host may happen to have.
	s := sampleCgroup1([]byte("0::/cornus/cornus-web-0\n"))
	if s.CPUTotal != 0 || s.MemUsage != 0 || s.MemLimit != 0 || s.Pids != 0 || s.Blkio != nil {
		t.Errorf("sample = %+v, want an all-zero sample when no v1 controller resolves", s)
	}
}
