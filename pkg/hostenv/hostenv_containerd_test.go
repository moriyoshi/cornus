package hostenv

import (
	"context"
	"strings"
	"testing"
)

// ctrMountinfo is a /proc/self/mountinfo as MEASURED inside a container created by
// `ctr run` on a containerd host (recorded in JOURNAL.md), not a docker one.
//
// The difference is the whole point: there is NO
// /var/lib/docker/containers/<64hex>/ bind here, because containerd binds the
// host's own /etc/hosts and /etc/resolv.conf straight in. So none of the markers
// looksContainerized checks are present, and none of hostenv's id miners have
// anything to read — the cgroup path below is "/default/<name>", not 64 hex.
const ctrMountinfo = `1801 1799 0:52 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/containerd/io.containerd.snapshotter.v1.native/snapshots/1/fs
1802 1801 0:56 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
1803 1801 0:57 / /sys rw,nosuid,nodev,noexec,relatime - sysfs sysfs rw
1810 1801 259:2 /etc/hosts /etc/hosts rw,relatime - ext4 /dev/nvme0n1p2 rw
1811 1801 259:2 /etc/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/nvme0n1p2 rw
1820 1801 259:2 /srv/cornus /var/lib/cornus rw,relatime shared:412 - ext4 /dev/nvme0n1p2 rw
`

// containerdSelfInspect is what containerdhost.SelfInspector produces for such a
// container: an EMPTY hostname (containerd's default spec sets none, so the
// container inherits the host's), host networking, and the operator's bind only.
func containerdSelfInspect(_ context.Context, id string) (SelfInspect, error) {
	return SelfInspect{
		ID:          id,
		Hostname:    "",
		NetworkMode: "host",
		Mounts:      []MountPoint{{Source: "/srv/cornus", Destination: "/var/lib/cornus"}},
	}, nil
}

// TestDetectSelfInspectsAContainerdContainerWithNoMarkers is the load-bearing
// case for the containerd backend: a caller-supplied candidate must be inspected
// even though every containerization marker is absent, and a CONFIRMED inspection
// is what establishes InContainer.
//
// The control half below is the part that matters — without the supplied
// candidate this exact environment is indistinguishable from a host process, so
// gating self-inspection on looksContainerized would leave the containerd path
// dead on the hosts it exists for.
func TestDetectSelfInspectsAContainerdContainerWithNoMarkers(t *testing.T) {
	proc := fakeProc(t, ctrMountinfo, "0::/default/cornus-server\n")
	opts := Options{
		Runtime:  RuntimeContainerd,
		procRoot: proc,
		getenv:   noEnv,
		hostname: "buildhost", // the HOST's hostname, inherited: measured
		exists:   noFile,
		Inspect:  containerdSelfInspect,
	}

	t.Run("with the backend's candidate", func(t *testing.T) {
		o := opts
		o.ExtraSelfIDs = []string{"cornus-server"}
		env, m, err := Detect(context.Background(), o)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if !env.InContainer {
			t.Error("InContainer = false: a confirmed self-inspection is itself evidence of being in a container")
		}
		if env.SelfID != "cornus-server" {
			t.Errorf("SelfID = %q, want cornus-server", env.SelfID)
		}
		if !env.Translating {
			t.Error("Translating = false: the reported bind proves the paths diverge")
		}
		if !env.HostNetworkKnown || !env.HostNetwork {
			t.Errorf("HostNetwork = %v (known %v), want true/true", env.HostNetwork, env.HostNetworkKnown)
		}
		if got, ok := m.ToHost("/var/lib/cornus/containerd/volumes/data"); !ok || got != "/srv/cornus/containerd/volumes/data" {
			t.Errorf("ToHost = (%q, %v), want (/srv/cornus/containerd/volumes/data, true)", got, ok)
		}
	})

	t.Run("control: without it, nothing is detected at all", func(t *testing.T) {
		env, m, err := Detect(context.Background(), opts)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if env.InContainer {
			t.Error("InContainer = true: this fixture deliberately carries no marker, so the candidate is the only route")
		}
		if env.Translating {
			t.Error("Translating = true without any confirmed candidate")
		}
		if got, _ := m.ToHost("/var/lib/cornus/x"); got != "/var/lib/cornus/x" {
			t.Errorf("ToHost = %q, want the identity", got)
		}
	})
}

// TestDetectDoesNotClaimContainerizationOnAnUnconfirmedCandidate keeps the new
// evidence rule from becoming a new way to be wrong: a candidate the daemon does
// not confirm must leave a host process reported as a host process, and must not
// manufacture an Env.Err for one either.
func TestDetectDoesNotClaimContainerizationOnAnUnconfirmedCandidate(t *testing.T) {
	// A host mount table and a systemd cgroup path, which selfCgroupRefs-style
	// mining would offer as {system.slice, cornus.service}.
	proc := fakeProc(t, "2038 1905 259:2 / / rw,relatime - ext4 /dev/nvme0n1p2 rw\n", "0::/system.slice/cornus.service\n")
	env, m, err := Detect(context.Background(), Options{
		Runtime: RuntimeContainerd, procRoot: proc, getenv: noEnv,
		hostname: "buildhost", exists: noFile,
		ExtraSelfIDs: []string{"cornus.service"},
		Inspect: func(context.Context, string) (SelfInspect, error) {
			return SelfInspect{}, errNoSelfCandidate // no such container anywhere
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if env.InContainer {
		t.Error("InContainer = true for a host process whose candidate was never confirmed")
	}
	if env.Err != nil {
		t.Errorf("Env.Err = %v: failing to confirm is not a problem to report when we were never in a container", env.Err)
	}
	if got, _ := m.ToHost("/var/lib/cornus/x"); got != "/var/lib/cornus/x" {
		t.Errorf("ToHost = %q, want the identity", got)
	}
}

// TestHostNetworkEnvOverridesInspection: the operator's word wins, because the
// isolated-netns verdict is fatal in pkg/hostcheck and a wrong one must not be
// unappealable.
func TestHostNetworkEnvOverridesInspection(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "0::/\n")
	env, _, err := Detect(context.Background(), Options{
		Runtime: RuntimeDocker, procRoot: proc,
		getenv:   envMap(map[string]string{HostNetworkEnv: "1"}),
		hostname: "cornus", exists: noFile,
		Inspect: func(_ context.Context, id string) (SelfInspect, error) {
			// Inspection says the opposite: its own netns.
			return SelfInspect{ID: id, NetworkMode: "bridge", Mounts: []MountPoint{
				{Source: "/srv/cornus", Destination: "/var/lib/cornus"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !env.HostNetworkKnown || !env.HostNetwork {
		t.Errorf("HostNetwork = %v (known %v), want true/true: %s must beat inspection",
			env.HostNetwork, env.HostNetworkKnown, HostNetworkEnv)
	}
}

// TestHostNetworkEnvAnswersWithNoInspectorAtAll is the bare backend's only route:
// it drives an OCI runtime as its own child, so there is no daemon to ask.
func TestHostNetworkEnvAnswersWithNoInspectorAtAll(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "0::/\n")
	env, _, err := Detect(context.Background(), Options{
		Runtime: RuntimeUnknown, procRoot: proc,
		getenv:   envMap(map[string]string{HostNetworkEnv: "0"}),
		hostname: "cornus", exists: noFile,
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !env.HostNetworkKnown {
		t.Fatal("HostNetworkKnown = false: an explicit setting is knowledge even with nothing to inspect")
	}
	if env.HostNetwork {
		t.Error("HostNetwork = true, want false")
	}
}

// TestDetectRejectsMalformedHostNetwork: a typo in a knob set to fix a problem
// must not quietly leave the problem, exactly as for CORNUS_HOST_PATH_MAP.
func TestDetectRejectsMalformedHostNetwork(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "0::/\n")
	_, _, err := Detect(context.Background(), Options{
		procRoot: proc,
		getenv:   envMap(map[string]string{HostNetworkEnv: "sure"}),
		hostname: "cornus", exists: noFile,
	})
	if err == nil {
		t.Fatalf("Detect accepted %s=sure", HostNetworkEnv)
	}
	if !strings.Contains(err.Error(), HostNetworkEnv) {
		t.Errorf("error %q does not name the variable at fault", err)
	}
}

// TestParseHostNetwork covers the accepted spellings and, more importantly, that
// "unset" is distinguishable from "set to false" — conflating them would make an
// unset variable assert an isolated netns, which is now a fatal verdict.
func TestParseHostNetwork(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		val, set  bool
		wantError bool
	}{
		{raw: "", val: false, set: false},
		{raw: "1", val: true, set: true},
		{raw: "true", val: true, set: true},
		{raw: "  YES  ", val: true, set: true},
		{raw: "on", val: true, set: true},
		{raw: "0", val: false, set: true},
		{raw: "False", val: false, set: true},
		{raw: "no", val: false, set: true},
		{raw: "maybe", wantError: true},
		{raw: "2", wantError: true},
	} {
		val, set, err := parseHostNetwork(tc.raw)
		if tc.wantError {
			if err == nil {
				t.Errorf("parseHostNetwork(%q) accepted it", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseHostNetwork(%q): %v", tc.raw, err)
			continue
		}
		if val != tc.val || set != tc.set {
			t.Errorf("parseHostNetwork(%q) = (%v, %v), want (%v, %v)", tc.raw, val, set, tc.val, tc.set)
		}
	}
}
