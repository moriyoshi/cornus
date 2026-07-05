//go:build linux

package incushost

import (
	"strings"
	"testing"

	incusapi "github.com/lxc/incus/v6/shared/api"
)

// TestSelfInspectIdentifiesTheInstanceWeAre pins the lookup: an incus instance's
// hostname IS its name (measured in a live application container), so the name we
// are handed is the instance to fetch, and its own name comes back as the Hostname
// that lets hostenv.confirmSelf accept the candidate.
//
// The Hostname half is load-bearing here in a way it is not on containerd. An
// instance whose only disk device is the pool-backed root reports NO mounts, so
// confirmSelf has nothing to match against our mount table and falls through to the
// hostname comparison. Return an empty Hostname and every such instance goes
// unconfirmed.
func TestSelfInspectIdentifiesTheInstanceWeAre(t *testing.T) {
	f := &fakeConn{insts: map[string]*incusapi.Instance{
		"cornus-srv": {
			Name: "cornus-srv",
			ExpandedDevices: map[string]map[string]string{
				"root":   {"type": "disk", "path": "/", "pool": "default"},
				"incusd": {"type": "proxy", "listen": "unix:/var/lib/incus/unix.socket"},
				"data":   {"type": "disk", "source": "/srv/cornus", "path": "/var/lib/cornus"},
			},
		},
	}}

	self, err := selfInspect(f, "cornus-srv")
	if err != nil {
		t.Fatalf("selfInspect: %v", err)
	}
	if self.ID != "cornus-srv" || self.Hostname != "cornus-srv" {
		t.Errorf("ID/Hostname = %q/%q, want cornus-srv/cornus-srv", self.ID, self.Hostname)
	}
	if len(self.Mounts) != 1 || self.Mounts[0].Source != "/srv/cornus" || self.Mounts[0].Destination != "/var/lib/cornus" {
		t.Errorf("Mounts = %+v, want just /srv/cornus -> /var/lib/cornus", self.Mounts)
	}
	// An instance is NOT in the host's network namespace — it has its own, on
	// incusd's bridge. Claiming "host" would be false, and would make
	// hostSetup.isolatedNetwork() report a route the server does not have.
	if self.NetworkMode == "host" {
		t.Error("NetworkMode = host: an incus instance has its own netns on incusd's bridge")
	}
}

// TestInstanceDiskMountsSkipsPoolBackedAndRelativeDevices: a pool-backed device's
// `source` is a volume NAME, not a path. Reporting it as a host path would hand
// confirmSelf — and the path map — a correspondence that means nothing.
func TestInstanceDiskMountsSkipsPoolBackedAndRelativeDevices(t *testing.T) {
	got := instanceDiskMounts(map[string]map[string]string{
		"root":   {"type": "disk", "path": "/", "pool": "default"},
		"vol":    {"type": "disk", "source": "myvolume", "path": "/data", "pool": "default"},
		"tmp":    {"type": "disk", "source": "tmpfs:", "path": "/tmp"},
		"nic":    {"type": "nic", "network": "incusbr0"},
		"proxy":  {"type": "proxy", "listen": "unix:/var/lib/incus/unix.socket"},
		"real":   {"type": "disk", "source": "/srv/cornus", "path": "/var/lib/cornus"},
		"relsrc": {"type": "disk", "source": "relative", "path": "/x"},
	})
	if len(got) != 1 || got[0].Source != "/srv/cornus" {
		t.Fatalf("instanceDiskMounts = %+v, want only the absolute host-path disk device", got)
	}
}

// TestInstanceDiskMountsIsDeterministic: ExpandedDevices is a map, and the result
// feeds hostenv's path map, where entries of equal prefix length are resolved in
// arrival order. A map range here would be a mapping that differs between restarts
// of the same server — the defect pickIPv4 had, and just as hard to diagnose.
func TestInstanceDiskMountsIsDeterministic(t *testing.T) {
	devices := map[string]map[string]string{
		"aaa": {"type": "disk", "source": "/srv/a", "path": "/mnt/a"},
		"bbb": {"type": "disk", "source": "/srv/b", "path": "/mnt/b"},
		"ccc": {"type": "disk", "source": "/srv/c", "path": "/mnt/c"},
		"ddd": {"type": "disk", "source": "/srv/d", "path": "/mnt/d"},
		"eee": {"type": "disk", "source": "/srv/e", "path": "/mnt/e"},
	}
	want := "/mnt/a,/mnt/b,/mnt/c,/mnt/d,/mnt/e"
	for i := 0; i < 200; i++ {
		var got []string
		for _, m := range instanceDiskMounts(devices) {
			got = append(got, m.Destination)
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("iteration %d: order = %v, want %s", i, got, want)
		}
	}
}

// TestSelfInspectRejectsAnInstanceThatIsNotThere: the fake returns (nil, nil) for a
// missing instance, which must become an error rather than an empty SelfInspect —
// an empty one would be "confirmed" by confirmSelf's accept-when-nothing-to-check
// branch and would set SelfID to "".
func TestSelfInspectRejectsAnInstanceThatIsNotThere(t *testing.T) {
	f := &fakeConn{insts: map[string]*incusapi.Instance{}}
	if _, err := selfInspect(f, "nope"); err == nil {
		t.Fatal("selfInspect accepted a missing instance")
	}
}

// TestUnreachableHintSuppressedForASiblingInstance is the routing consequence of
// self-inspection, and the reason it is worth doing on a backend with no paths to
// translate.
//
// A cornus that IS an instance looks isolated by every test hostenv can apply — it
// is in a container with its own netns — but it sits on incusd's bridge alongside
// the workloads and can dial them. The hint would send an operator to configure
// remote mode or host networking for a dial that failed because nothing was
// listening yet.
func TestUnreachableHintSuppressedForASiblingInstance(t *testing.T) {
	isolated := &Backend{isolatedNetwork: true}
	if isolated.unreachableHint() == "" {
		t.Fatal("a containerized server beside incusd must still be told why it cannot route")
	}
	sibling := &Backend{isolatedNetwork: true, selfInstance: "cornus-srv"}
	if h := sibling.unreachableHint(); h != "" {
		t.Errorf("a cornus that IS an instance can route to its workloads; hint should be empty, got %q", h)
	}
}

// TestWithSelfInstanceIgnoresEmpty: "" is also what a failed lookup yields, so
// recording it would silently claim we are an instance named nothing.
func TestWithSelfInstanceIgnoresEmpty(t *testing.T) {
	var o options
	WithSelfInstance("")(&o)
	if o.selfInstance != "" {
		t.Errorf("selfInstance = %q, want empty", o.selfInstance)
	}
	WithSelfInstance("srv")(&o)
	if o.selfInstance != "srv" {
		t.Errorf("selfInstance = %q, want srv", o.selfInstance)
	}
}
