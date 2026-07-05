//go:build linux

package barehost

import (
	"errors"
	"testing"
)

func TestResolveSandboxed(t *testing.T) {
	// Name-based auto-detection (no env override).
	t.Setenv("CORNUS_BARE_STATS_SOURCE", "")
	cases := map[string]bool{
		"runc":                  false,
		"crun":                  false,
		"youki":                 false,
		"runsc":                 true,
		"gvisor":                true,
		"/usr/local/bin/runsc":  true,
		"/opt/gvisor/bin/runsc": true,
		"/usr/bin/runc":         false,
		"RunSC":                 true, // case-insensitive basename
	}
	for runtime, want := range cases {
		if got := resolveSandboxed(runtime); got != want {
			t.Errorf("resolveSandboxed(%q) = %v, want %v", runtime, got, want)
		}
	}
}

func TestResolveSandboxedEnvOverride(t *testing.T) {
	// "runtime" forces sandboxed even for runc; "cgroup" forces off even for runsc.
	t.Setenv("CORNUS_BARE_STATS_SOURCE", "runtime")
	if !resolveSandboxed("runc") {
		t.Error("CORNUS_BARE_STATS_SOURCE=runtime should force sandboxed for runc")
	}
	t.Setenv("CORNUS_BARE_STATS_SOURCE", "cgroup")
	if resolveSandboxed("runsc") {
		t.Error("CORNUS_BARE_STATS_SOURCE=cgroup should force cgroup source for runsc")
	}
	// Unknown values fall through to name-based detection.
	t.Setenv("CORNUS_BARE_STATS_SOURCE", "bogus")
	if !resolveSandboxed("runsc") {
		t.Error("unknown override should fall through to name detection (runsc ⇒ sandboxed)")
	}
}

func TestRuntimeStatsToSample(t *testing.T) {
	rs := runtimeStats{
		CPUTotal:  1_000,
		CPUUser:   600,
		CPUKernel: 400,
		MemUsage:  2 << 20,
		MemLimit:  8 << 20,
		MemStats:  map[string]uint64{"rss": 1 << 20},
		Pids:      7,
		Blkio: []blkioEntry{
			{Major: 8, Minor: 0, Op: "read", Value: 4096},
			{Major: 8, Minor: 0, Op: "write", Value: 8192},
		},
	}
	s := runtimeStatsToSample(rs)
	if s.CPUTotal != 1_000 || s.CPUUser != 600 || s.CPUKernel != 400 {
		t.Errorf("cpu mismatch: %+v", s)
	}
	if s.MemUsage != 2<<20 || s.MemLimit != 8<<20 || s.Pids != 7 {
		t.Errorf("mem/pids mismatch: %+v", s)
	}
	if s.MemStats["rss"] != 1<<20 {
		t.Errorf("mem stats not carried: %+v", s.MemStats)
	}
	if len(s.Blkio) != 2 || s.Blkio[0].Op != "read" || s.Blkio[0].Value != 4096 || s.Blkio[1].Value != 8192 {
		t.Errorf("blkio mismatch: %+v", s.Blkio)
	}
}

func TestSampleRuntime(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, true)
	rt.stats = runtimeStats{CPUTotal: 5_000, MemUsage: 4 << 20, MemLimit: 16 << 20, Pids: 3}

	s, err := b.sampleRuntime(ctx, instanceName("web", 0))
	if err != nil {
		t.Fatalf("sampleRuntime: %v", err)
	}
	if s.CPUTotal != 5_000 || s.MemUsage != 4<<20 || s.MemLimit != 16<<20 || s.Pids != 3 {
		t.Errorf("sample = %+v", s)
	}
	if s.Read.IsZero() {
		t.Error("Read timestamp not stamped")
	}
	if len(s.Networks) != 0 {
		t.Errorf("runtime source should report no networks, got %v", s.Networks)
	}
}

func TestSampleRuntimeMemLimitFallback(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, true)
	rt.stats = runtimeStats{MemLimit: 0} // no limit ⇒ host total substituted

	s, err := b.sampleRuntime(ctx, instanceName("web", 0))
	if err != nil {
		t.Fatalf("sampleRuntime: %v", err)
	}
	if s.MemLimit == 0 {
		t.Error("MemLimit=0 should fall back to host total")
	}
}

func TestSampleRuntimeNotRunning(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, false) // created, not running

	if _, err := b.sampleRuntime(ctx, instanceName("web", 0)); err == nil {
		t.Error("sampleRuntime on a non-running instance should error")
	}
}

func TestSampleRuntimeStatsError(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, true)
	rt.statsErr = errors.New("runtime boom")

	if _, err := b.sampleRuntime(ctx, instanceName("web", 0)); err == nil {
		t.Error("sampleRuntime should propagate a runtime Stats error")
	}
}
