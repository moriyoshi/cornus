//go:build linux

package hostrun

import (
	"context"
	"reflect"
	"testing"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
)

func TestEnvListSorted(t *testing.T) {
	spec := api.DeploySpec{Env: map[string]string{"B": "2", "A": "1", "C": "3"}}
	got := envList(spec)
	want := []string{"A=1", "B=2", "C=3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envList = %v, want %v", got, want)
	}
	if envList(api.DeploySpec{}) != nil {
		t.Fatal("empty env should yield nil")
	}
}

func TestOCIBindMount(t *testing.T) {
	rw := OCIBindMount("/src", "/dst", false)
	if rw.Type != "bind" || rw.Source != "/src" || rw.Destination != "/dst" {
		t.Fatalf("mount = %+v", rw)
	}
	if !reflect.DeepEqual(rw.Options, []string{"rbind", "rw"}) {
		t.Fatalf("rw options = %v", rw.Options)
	}
	ro := OCIBindMount("/src", "/dst", true)
	if !reflect.DeepEqual(ro.Options, []string{"rbind", "ro"}) {
		t.Fatalf("ro options = %v", ro.Options)
	}
}

// applyRuntimeOpts applies the image-independent opts to a scratch spec (backend
// label "test", instance id "test").
func applyRuntimeOpts(t *testing.T, spec api.DeploySpec, netns string, mounts []specs.Mount) *oci.Spec {
	t.Helper()
	ctx := namespaces.WithNamespace(context.Background(), "test")
	c := &containers.Container{ID: "test"}
	s, err := oci.GenerateSpec(ctx, nil, c)
	if err != nil {
		t.Fatalf("generate default spec: %v", err)
	}
	for _, opt := range runtimeOpts(ctx, "test", "test", spec, netns, mounts) {
		if err := opt(ctx, nil, c, s); err != nil {
			t.Fatalf("apply opt: %v", err)
		}
	}
	return s
}

func TestRuntimeOptsResourcesAndEnv(t *testing.T) {
	s := applyRuntimeOpts(t, api.DeploySpec{
		Env:       map[string]string{"FOO": "bar"},
		Resources: &api.Resources{CPULimit: 1.5, MemoryLimit: 64 << 20},
	}, "", nil)
	found := false
	for _, e := range s.Process.Env {
		if e == "FOO=bar" {
			found = true
		}
	}
	if !found {
		t.Fatalf("env not applied: %v", s.Process.Env)
	}
	res := s.Linux.Resources
	if res == nil || res.Memory == nil || *res.Memory.Limit != 64<<20 {
		t.Fatalf("memory limit not applied: %+v", res)
	}
	if res.CPU == nil || *res.CPU.Quota != 150000 || *res.CPU.Period != 100000 {
		t.Fatalf("cpu cfs not applied: %+v", res.CPU)
	}
}

// TestRuntimeOptsSecurityKeys asserts the security batch maps onto the OCI
// process/linux spec: cap_add/cap_drop into the capability sets, numeric
// group_add into AdditionalGids (a group name skipped), sysctls into
// Linux.Sysctl, and no-new-privileges into Process.NoNewPrivileges.
func TestRuntimeOptsSecurityKeys(t *testing.T) {
	s := applyRuntimeOpts(t, api.DeploySpec{
		Name:        "web",
		CapAdd:      []string{"CAP_NET_ADMIN"},
		CapDrop:     []string{"CAP_MKNOD"},
		GroupAdd:    []string{"1001", "staff"}, // "staff" (a name) is skipped
		Sysctls:     map[string]string{"net.core.somaxconn": "1024"},
		SecurityOpt: []string{"no-new-privileges:true"},
	}, "", nil)

	if !hasCap(s.Process.Capabilities.Bounding, "CAP_NET_ADMIN") {
		t.Errorf("cap_add not applied: %v", s.Process.Capabilities.Bounding)
	}
	if hasCap(s.Process.Capabilities.Bounding, "CAP_MKNOD") {
		t.Errorf("cap_drop not applied: MKNOD still present: %v", s.Process.Capabilities.Bounding)
	}
	if !reflect.DeepEqual(s.Process.User.AdditionalGids, []uint32{1001}) {
		t.Errorf("AdditionalGids = %v, want [1001] (numeric only)", s.Process.User.AdditionalGids)
	}
	if s.Linux == nil || s.Linux.Sysctl["net.core.somaxconn"] != "1024" {
		t.Errorf("sysctls not applied: %+v", s.Linux)
	}
	if !s.Process.NoNewPrivileges {
		t.Error("no-new-privileges not applied")
	}
}

// TestRuntimeOptsResolverKeysNoOp confirms the resolver/hosts keys are inert:
// extra_hosts/dns/dns_search/dns_opt neither error nor mutate the spec's
// hosts/resolver state (the OCI spec has no field for them).
func TestRuntimeOptsResolverKeysNoOp(t *testing.T) {
	s := applyRuntimeOpts(t, api.DeploySpec{
		Name:       "web",
		ExtraHosts: []string{"somehost:10.0.0.1"},
		DNSServers: []string{"8.8.8.8"},
		DNSSearch:  []string{"example.com"},
		DNSOptions: []string{"use-vc"},
	}, "", nil)
	for _, m := range s.Mounts {
		if m.Destination == "/etc/hosts" || m.Destination == "/etc/resolv.conf" {
			t.Errorf("unexpected synthesised mount for resolver key: %+v", m)
		}
	}
}

// TestRuntimeOptsResourceKeys asserts the resource & host-namespace batch maps
// onto the OCI spec: ulimits -> Process.Rlimits, tmpfs -> tmpfs mounts, devices
// -> Linux.Devices + cgroup rule, shm_size -> the /dev/shm mount size= option,
// and pid/ipc "host" -> the PID/IPC namespace being dropped.
func TestRuntimeOptsResourceKeys(t *testing.T) {
	s := applyRuntimeOpts(t, api.DeploySpec{
		Name: "web",
		Ulimits: []api.Ulimit{
			{Name: "nofile", Soft: 20000, Hard: 40000},
		},
		Tmpfs:   []string{"/run", "/tmp:size=64m"},
		Devices: []string{"/dev/null:/dev/null:rwm"},
		ShmSize: 128 << 20,
		PIDMode: "host",
		IPCMode: "host",
	}, "", nil)

	var rl *specs.POSIXRlimit
	for i := range s.Process.Rlimits {
		if s.Process.Rlimits[i].Type == "RLIMIT_NOFILE" {
			rl = &s.Process.Rlimits[i]
		}
	}
	if rl == nil || rl.Soft != 20000 || rl.Hard != 40000 {
		t.Errorf("RLIMIT_NOFILE = %+v, want soft=20000 hard=40000", rl)
	}

	tmp := mountByDest(s.Mounts, "/tmp")
	if tmp == nil || tmp.Type != "tmpfs" {
		t.Fatalf("/tmp tmpfs mount missing: %+v", s.Mounts)
	}
	if !hasOption(tmp.Options, "size=64m") {
		t.Errorf("/tmp options = %v, want size=64m present", tmp.Options)
	}
	if run := mountByDest(s.Mounts, "/run"); run == nil || run.Type != "tmpfs" {
		t.Errorf("/run tmpfs mount missing: %+v", s.Mounts)
	}

	foundDev := false
	for _, d := range s.Linux.Devices {
		if d.Path == "/dev/null" {
			foundDev = true
		}
	}
	if !foundDev {
		t.Errorf("/dev/null device not applied: %+v", s.Linux.Devices)
	}

	shm := mountByDest(s.Mounts, "/dev/shm")
	if shm == nil {
		t.Fatalf("/dev/shm mount missing: %+v", s.Mounts)
	}
	if !hasOption(shm.Options, "size=134217728") {
		t.Errorf("/dev/shm options = %v, want size=134217728", shm.Options)
	}

	for _, ns := range s.Linux.Namespaces {
		if ns.Type == specs.PIDNamespace {
			t.Errorf("pid namespace still present, want dropped for pid: host")
		}
		if ns.Type == specs.IPCNamespace {
			t.Errorf("ipc namespace still present, want dropped for ipc: host")
		}
	}
}

// TestRuntimeOptsPidIpcNonHostNoOp asserts a non-host pid/ipc form leaves the
// namespaces intact (warned about, not applied).
func TestRuntimeOptsPidIpcNonHostNoOp(t *testing.T) {
	s := applyRuntimeOpts(t, api.DeploySpec{
		Name:    "web",
		PIDMode: "service:other",
		IPCMode: "shareable",
	}, "", nil)
	var hasPID, hasIPC bool
	for _, ns := range s.Linux.Namespaces {
		if ns.Type == specs.PIDNamespace {
			hasPID = true
		}
		if ns.Type == specs.IPCNamespace {
			hasIPC = true
		}
	}
	if !hasPID || !hasIPC {
		t.Errorf("non-host pid/ipc should leave namespaces intact (pid=%v ipc=%v)", hasPID, hasIPC)
	}
}

func TestRuntimeOptsNetnsAndMounts(t *testing.T) {
	m := OCIBindMount("/data", "/var/data", true)
	s := applyRuntimeOpts(t, api.DeploySpec{}, "/run/cornus/netns/x", []specs.Mount{m})
	var nsPath string
	for _, ns := range s.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			nsPath = ns.Path
		}
	}
	if nsPath != "/run/cornus/netns/x" {
		t.Fatalf("network namespace path = %q", nsPath)
	}
	if s.Hostname != "test" {
		t.Fatalf("hostname = %q, want the instance ID", s.Hostname)
	}
	foundMount := false
	for _, mt := range s.Mounts {
		if mt.Destination == "/var/data" && mt.Source == "/data" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Fatalf("bind mount not applied: %+v", s.Mounts)
	}
}

// mountByDest returns the first mount with the given destination, or nil.
func mountByDest(mounts []specs.Mount, dest string) *specs.Mount {
	for i := range mounts {
		if mounts[i].Destination == dest {
			return &mounts[i]
		}
	}
	return nil
}

// hasOption reports whether opt is present in the mount options.
func hasOption(opts []string, opt string) bool {
	for _, o := range opts {
		if o == opt {
			return true
		}
	}
	return false
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
