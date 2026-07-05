//go:build linux

package barehost

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

// Stats for the bare backend: the metrics SOURCE. There is no daemon to ask for
// task.Metrics, and loading the container cgroup through containerd's cgroup
// MANAGER libraries would drag cilium/ebpf + go-systemd/dbus into the
// deliberately buildkit-free deploy tree. So the bare sampler reads the cgroup
// pseudo-files directly (cgroup v2 unified fully; cgroup v1 best-effort per
// controller) and hands a hostrun.StatsSample to the shared Docker-JSON encoder
// + streaming loop (hostrun.StreamStats), identical framing to containerdhost.

const cgroupRoot = "/sys/fs/cgroup"

// Stats streams Docker-format stats JSON for the deployment's first instance:
// one object then EOF when opts.Stream is false, else one per second.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	rec, err := b.firstRunningInstance(ctx, name)
	if err != nil {
		return err
	}
	unified := cgroupUnified()
	sample := func() (hostrun.StatsSample, error) {
		if b.sandboxed {
			// gVisor/runsc: the guest's usage is not reflected in the host cgroup
			// files, so read the runtime's own accounting instead.
			return b.sampleRuntime(ctx, rec.ID)
		}
		return b.sampleCgroup(ctx, rec.ID, unified)
	}
	return hostrun.StreamStats(ctx, w, rec.ID, name, opts.Stream, sample)
}

// sampleRuntime reads one metrics sample from the runtime itself (`runc events
// --stats`) — the source for sandboxed runtimes (gVisor) whose guest accounting
// the host cgroup pseudo-files do not reflect. As with sampleCgroup, the
// container must be Running (re-checked each sample so a restart between frames
// is tolerated). The runtime stats carry no network counters and the guest's
// netstack is invisible on the host, so Networks is left empty — a documented
// gVisor limitation, not an error.
func (b *Backend) sampleRuntime(ctx context.Context, id string) (hostrun.StatsSample, error) {
	st, err := b.rt.State(ctx, id)
	if err != nil || st.Status != runcStateRunning {
		return hostrun.StatsSample{}, fmt.Errorf("bare: instance %s is not running", id)
	}
	rs, err := b.rt.Stats(ctx, id)
	if err != nil {
		return hostrun.StatsSample{}, fmt.Errorf("bare: runtime stats for %s: %w", id, err)
	}
	s := runtimeStatsToSample(rs)
	s.Read = time.Now()
	s.SysUsage = hostrun.SystemCPUUsage()
	if s.MemLimit == 0 || s.MemLimit > uint64(1)<<60 {
		// No cgroup limit (or the "unlimited" sentinel): report host total.
		s.MemLimit = hostrun.HostMemTotal()
	}
	return s, nil
}

// runtimeStatsToSample maps the runtime-native stats onto a hostrun.StatsSample
// for the shared Docker-JSON encoder. Field units already match (CPU ns, memory
// and blkio bytes); SysUsage/Read/MemLimit-fallback are applied by the caller.
func runtimeStatsToSample(rs runtimeStats) hostrun.StatsSample {
	s := hostrun.StatsSample{
		CPUTotal:  rs.CPUTotal,
		CPUUser:   rs.CPUUser,
		CPUKernel: rs.CPUKernel,
		MemUsage:  rs.MemUsage,
		MemLimit:  rs.MemLimit,
		MemStats:  rs.MemStats,
		Pids:      rs.Pids,
	}
	for _, e := range rs.Blkio {
		s.Blkio = append(s.Blkio, hostrun.DockerBlkioEntry{Major: e.Major, Minor: e.Minor, Op: e.Op, Value: e.Value})
	}
	return s
}

// sampleCgroup reads one metrics sample from the instance's cgroup and its
// netns view. The init PID is re-resolved each sample (a restart between
// samples yields a fresh PID and cgroup) and network counters are only trusted
// while the container is Running: a dead/reused PID's netns would misattribute
// another process's traffic to this container.
func (b *Backend) sampleCgroup(ctx context.Context, id string, unified bool) (hostrun.StatsSample, error) {
	st, err := b.rt.State(ctx, id)
	if err != nil || st.Status != runcStateRunning || st.Pid == 0 {
		return hostrun.StatsSample{}, fmt.Errorf("bare: instance %s is not running", id)
	}
	proc, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", st.Pid))
	if err != nil {
		return hostrun.StatsSample{}, fmt.Errorf("bare: read cgroup for %s: %w", id, err)
	}
	var s hostrun.StatsSample
	if unified {
		if rel := parseProcCgroup(proc, ""); rel != "" {
			s = sampleCgroup2(filepath.Join(cgroupRoot, rel))
		}
	} else {
		s = sampleCgroup1(proc)
	}
	s.Read = time.Now()
	s.SysUsage = hostrun.SystemCPUUsage()
	if s.MemLimit == 0 || s.MemLimit > uint64(1)<<60 {
		// No cgroup limit (or the "unlimited" sentinel): report host total.
		s.MemLimit = hostrun.HostMemTotal()
	}
	s.Networks = hostrun.ReadNetDev(fmt.Sprintf("/proc/%d/net/dev", st.Pid))
	return s, nil
}

// cgroupUnified reports whether /sys/fs/cgroup is a cgroup v2 unified mount.
func cgroupUnified() bool {
	var st unix.Statfs_t
	if err := unix.Statfs(cgroupRoot, &st); err != nil {
		return false
	}
	return uint32(st.Type) == uint32(unix.CGROUP2_SUPER_MAGIC)
}

// parseProcCgroup returns the cgroup path for a controller from /proc/<pid>/
// cgroup content. controller=="" selects the cgroup v2 unified line
// ("0::<path>"); otherwise it matches a v1 line whose comma-separated
// controller list contains the name.
func parseProcCgroup(data []byte, controller string) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if controller == "" {
			if parts[0] == "0" && parts[1] == "" {
				return parts[2]
			}
			continue
		}
		for _, c := range strings.Split(parts[1], ",") {
			if c == controller {
				return parts[2]
			}
		}
	}
	return ""
}

// --- cgroup v2 (unified) ---

// sampleCgroup2 reads the unified-hierarchy files under dir into a StatsSample.
// Missing files leave their counters zero (docker parity: a container without a
// memory limit simply reports the host total via the caller's fallback).
func sampleCgroup2(dir string) hostrun.StatsSample {
	var s hostrun.StatsSample
	if data, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
		s.CPUTotal, s.CPUUser, s.CPUKernel = parseCPUStatV2(data)
	}
	s.MemUsage = readUintFile(filepath.Join(dir, "memory.current"))
	s.MemLimit = readMemMaxV2(filepath.Join(dir, "memory.max"))
	if data, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
		s.MemStats = parseKVUint(data)
	}
	s.Pids = readUintFile(filepath.Join(dir, "pids.current"))
	if data, err := os.ReadFile(filepath.Join(dir, "io.stat")); err == nil {
		s.Blkio = parseIOStatV2(data)
	}
	return s
}

// parseCPUStatV2 pulls cpu.stat's usage/user/system microsecond counters and
// converts them to docker's nanosecond shape.
func parseCPUStatV2(data []byte) (total, user, kernel uint64) {
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "usage_usec":
			total = v * 1000
		case "user_usec":
			user = v * 1000
		case "system_usec":
			kernel = v * 1000
		}
	}
	return
}

// parseIOStatV2 renders io.stat's per-device rbytes/wbytes as docker
// io_service_bytes_recursive read/write entries, the shape dockerd emits on
// cgroup v2 hosts.
func parseIOStatV2(data []byte) []hostrun.DockerBlkioEntry {
	var out []hostrun.DockerBlkioEntry
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		maj, min, ok := parseMajMin(f[0])
		if !ok {
			continue
		}
		var rbytes, wbytes uint64
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			switch k {
			case "rbytes":
				rbytes = n
			case "wbytes":
				wbytes = n
			}
		}
		out = append(out,
			hostrun.DockerBlkioEntry{Major: maj, Minor: min, Op: "read", Value: rbytes},
			hostrun.DockerBlkioEntry{Major: maj, Minor: min, Op: "write", Value: wbytes},
		)
	}
	return out
}

// readMemMaxV2 reads memory.max, mapping the "max" sentinel (no limit) to 0 so
// the caller substitutes the host total.
func readMemMaxV2(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// --- cgroup v1 (best-effort, per-controller) ---

// sampleCgroup1 reads the v1 controllers (cpuacct/memory/pids/blkio) for the
// process. Controller mount points come from /proc/self/mountinfo and the
// per-controller path from the process's own /proc/<pid>/cgroup, so no host
// layout is assumed. Any missing controller simply leaves its counters zero.
func sampleCgroup1(proc []byte) hostrun.StatsSample {
	var s hostrun.StatsSample
	if dir := cgroup1Dir("cpuacct", proc); dir != "" {
		s.CPUTotal = readUintFile(filepath.Join(dir, "cpuacct.usage"))
		if data, err := os.ReadFile(filepath.Join(dir, "cpuacct.stat")); err == nil {
			s.CPUUser, s.CPUKernel = parseCPUAcctStatV1(data)
		}
	}
	if dir := cgroup1Dir("memory", proc); dir != "" {
		s.MemUsage = readUintFile(filepath.Join(dir, "memory.usage_in_bytes"))
		s.MemLimit = readUintFile(filepath.Join(dir, "memory.limit_in_bytes"))
		if data, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
			s.MemStats = parseKVUint(data)
		}
	}
	if dir := cgroup1Dir("pids", proc); dir != "" {
		s.Pids = readUintFile(filepath.Join(dir, "pids.current"))
	}
	if dir := cgroup1Dir("blkio", proc); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "blkio.throttle.io_service_bytes")); err == nil {
			s.Blkio = parseBlkioV1(data)
		}
	}
	return s
}

// cgroup1Dir joins a v1 controller's mount point with the process's path in that
// controller's hierarchy.
func cgroup1Dir(controller string, proc []byte) string {
	mp := cgroup1Mount(controller)
	if mp == "" {
		return ""
	}
	rel := parseProcCgroup(proc, controller)
	if rel == "" {
		return ""
	}
	return filepath.Join(mp, rel)
}

// cgroup1Mount finds a v1 controller's mount point by scanning
// /proc/self/mountinfo for the cgroup mount whose super-options list it.
func cgroup1Mount(controller string) string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(data), "\n") {
		sep := strings.Index(ln, " - ")
		if sep < 0 {
			continue
		}
		pre := strings.Fields(ln[:sep])
		post := strings.Fields(ln[sep+3:])
		if len(pre) < 5 || len(post) < 3 || post[0] != "cgroup" {
			continue
		}
		for _, o := range strings.Split(post[2], ",") {
			if o == controller {
				return pre[4]
			}
		}
	}
	return ""
}

// parseCPUAcctStatV1 reads cpuacct.stat's user/system counters (USER_HZ ticks
// at 100 Hz) and converts them to docker's nanosecond shape.
func parseCPUAcctStatV1(data []byte) (user, system uint64) {
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "user":
			user = v * (1e9 / 100)
		case "system":
			system = v * (1e9 / 100)
		}
	}
	return
}

// parseBlkioV1 reads blkio.throttle.io_service_bytes ("MAJ:MIN Op Value" per
// line; the trailing bare "Total N" summary is 2 fields and skipped). The
// docker CLI matches Read/Write ops case-insensitively.
func parseBlkioV1(data []byte) []hostrun.DockerBlkioEntry {
	var out []hostrun.DockerBlkioEntry
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		if len(f) != 3 {
			continue
		}
		maj, min, ok := parseMajMin(f[0])
		if !ok {
			continue
		}
		v, err := strconv.ParseUint(f[2], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, hostrun.DockerBlkioEntry{Major: maj, Minor: min, Op: f[1], Value: v})
	}
	return out
}

// --- shared helpers ---

// parseKVUint parses "key value" lines (memory.stat on both cgroup versions)
// into a map, skipping malformed lines. Returns nil when empty.
func parseKVUint(data []byte) map[string]uint64 {
	out := map[string]uint64{}
	for _, ln := range strings.Split(string(data), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		out[f[0]] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readUintFile reads a single-integer cgroup file, yielding 0 on any error.
func readUintFile(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n
}

// parseMajMin splits a "MAJ:MIN" device token.
func parseMajMin(s string) (uint64, uint64, bool) {
	maj, min, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, false
	}
	mj, err1 := strconv.ParseUint(maj, 10, 64)
	mn, err2 := strconv.ParseUint(min, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return mj, mn, true
}
