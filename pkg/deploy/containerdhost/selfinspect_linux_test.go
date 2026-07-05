//go:build linux

package containerdhost

import (
	"context"
	"errors"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/hostenv"
)

// The specs below are the shapes MEASURED from containerd 2.2.6 with
// `ctr containers create` (recorded in JOURNAL.md), not shapes invented to match
// the code:
//
//   - `--net-host` produces linux.namespaces == [pid, ipc, uts, mount], with no
//     network entry at all; omitting it adds {"type": "network"}.
//   - spec.Hostname is empty; the container inherits the host's hostname.
//   - the default spec binds /etc/hosts and /etc/resolv.conf from the host in
//     addition to whatever the operator asked for.

// netHostSpec is a container created with --net-host plus one operator bind,
// including the two plumbing binds containerd adds on its own.
func netHostSpec() *specs.Spec {
	return &specs.Spec{
		Hostname: "",
		Mounts: []specs.Mount{
			{Type: "proc", Source: "proc", Destination: "/proc"},
			{Type: "sysfs", Source: "sysfs", Destination: "/sys"},
			{Type: "tmpfs", Source: "tmpfs", Destination: "/dev/shm"},
			{Type: "bind", Source: "/srv/cornus", Destination: "/var/lib/cornus"},
			{Type: "bind", Source: "/etc/hosts", Destination: "/etc/hosts"},
			{Type: "bind", Source: "/etc/resolv.conf", Destination: "/etc/resolv.conf"},
		},
		Linux: &specs.Linux{Namespaces: []specs.LinuxNamespace{
			{Type: specs.PIDNamespace},
			{Type: specs.IPCNamespace},
			{Type: specs.UTSNamespace},
			{Type: specs.MountNamespace},
		}},
	}
}

// fakeSelfInspect is a containerd stand-in: a namespace -> id -> spec table.
type fakeSelfInspect struct {
	specs      map[string]map[string]*specs.Spec
	namespaces []string
	loaded     []string // every (ns, id) LoadSpec was asked for, in order
	closed     int
}

func (f *fakeSelfInspect) Namespaces(context.Context) ([]string, error) {
	return f.namespaces, nil
}

func (f *fakeSelfInspect) LoadSpec(_ context.Context, ns, id string) (*specs.Spec, error) {
	f.loaded = append(f.loaded, ns+"/"+id)
	if s, ok := f.specs[ns][id]; ok {
		return s, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeSelfInspect) Close() error { f.closed++; return nil }

func (f *fakeSelfInspect) dialer() func() (selfInspectClient, error) {
	return func() (selfInspectClient, error) { return f, nil }
}

// TestSelfInspectReportsHostNetworkAndTheOperatorsBinds is the whole point of
// self-inspection: from the OCI spec alone, the server must learn that it shares
// the host's netns and that its data dir is /srv/cornus over there.
func TestSelfInspectReportsHostNetworkAndTheOperatorsBinds(t *testing.T) {
	f := &fakeSelfInspect{specs: map[string]map[string]*specs.Spec{
		"default": {"cornus-server": netHostSpec()},
	}}
	insp := selfInspector(f.dialer(), "cornus", map[string]string{"cornus-server": "default"})

	self, err := insp(context.Background(), "cornus-server")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if self.NetworkMode != "host" {
		t.Errorf("NetworkMode = %q, want \"host\" (a spec with no network namespace entry shares the host's)", self.NetworkMode)
	}
	if len(self.Mounts) != 1 {
		t.Fatalf("Mounts = %+v, want exactly the operator's bind", self.Mounts)
	}
	if self.Mounts[0].Source != "/srv/cornus" || self.Mounts[0].Destination != "/var/lib/cornus" {
		t.Errorf("Mounts[0] = %+v, want /srv/cornus -> /var/lib/cornus", self.Mounts[0])
	}
	if f.closed == 0 {
		t.Error("the containerd connection must be closed again; self-inspection is a startup question, not a lifetime one")
	}
}

// TestSelfInspectReportsAnIsolatedNetworkNamespace is the branch that becomes
// FATAL in pkg/hostcheck, so it must not be reachable by accident. A network
// namespace entry — whether it asks for a new netns (empty Path) or joins some
// other one (a Path) — is never the host's.
func TestSelfInspectReportsAnIsolatedNetworkNamespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		ns   specs.LinuxNamespace
	}{
		{"new netns", specs.LinuxNamespace{Type: specs.NetworkNamespace}},
		{"joins another netns", specs.LinuxNamespace{Type: specs.NetworkNamespace, Path: "/run/netns/other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := netHostSpec()
			spec.Linux.Namespaces = append(spec.Linux.Namespaces, tc.ns)
			f := &fakeSelfInspect{specs: map[string]map[string]*specs.Spec{"default": {"c": spec}}}
			insp := selfInspector(f.dialer(), "cornus", map[string]string{"c": "default"})

			self, err := insp(context.Background(), "c")
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			if self.NetworkMode == "host" {
				t.Errorf("NetworkMode = %q: a container with its own network namespace must not report host networking", self.NetworkMode)
			}
		})
	}
}

// TestReportableMountsDropsRuntimePlumbing guards hostenv.confirmSelf.
//
// confirmSelf accepts a candidate id as soon as ONE reported destination is a
// mount point in our own table, so that a wrong id cannot yield a confident path
// map. /etc/hosts, /etc/hostname and /etc/resolv.conf are mount points in EVERY
// container (measured: 1 hit for /etc/hosts in a ctr container's mountinfo), so
// reporting them would confirm any candidate at all and turn that guard off.
func TestReportableMountsDropsRuntimePlumbing(t *testing.T) {
	got := reportableMounts([]specs.Mount{
		{Type: "bind", Source: "/etc/hosts", Destination: "/etc/hosts"},
		{Type: "bind", Source: "/etc/hostname", Destination: "/etc/hostname"},
		{Type: "bind", Source: "/var/lib/docker/containers/x/resolv.conf", Destination: "/etc/resolv.conf"},
		{Type: "proc", Source: "proc", Destination: "/proc"},
		{Type: "tmpfs", Source: "tmpfs", Destination: "/tmp"},
		{Type: "bind", Source: "relative/path", Destination: "/opt/x"},
		{Type: "rbind", Source: "/srv/data", Destination: "/data"},
	})
	if len(got) != 1 {
		t.Fatalf("reportableMounts = %+v, want only the /srv/data rbind", got)
	}
	if got[0].Destination != "/data" {
		t.Errorf("kept %+v, want /srv/data -> /data", got[0])
	}
}

// TestNamespaceOrderPrefersTheCgroupNamespaceAndIsDeterministic pins both halves
// of the lookup order.
//
// Determinism is the part that matters: ranging the daemon's namespace map here
// would make self-inspection resolve differently between restarts of the same
// server — the defect incushost's pickIPv4 had, where a lookup that works about
// half the time is harder to diagnose than one that never works.
func TestNamespaceOrderPrefersTheCgroupNamespaceAndIsDeterministic(t *testing.T) {
	f := &fakeSelfInspect{namespaces: []string{"zeta", "alpha", "moby", "default", "cornus"}}
	want := []string{"default", "cornus", "moby", "alpha", "zeta"}
	for i := 0; i < 50; i++ {
		got := namespaceOrder(context.Background(), f, "cornus", "default")
		if len(got) != len(want) {
			t.Fatalf("iteration %d: order = %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iteration %d: order = %v, want %v", i, got, want)
			}
		}
	}
}

// TestSelfInspectFindsUsInADaemonNamespaceTheCgroupDidNotName covers the server
// container created by docker on a containerd host: its cgroup is "0::/" (docker
// gives a private cgroup namespace), so nothing names a namespace and the search
// has to find it.
func TestSelfInspectFindsUsInADaemonNamespaceTheCgroupDidNotName(t *testing.T) {
	f := &fakeSelfInspect{
		specs:      map[string]map[string]*specs.Spec{"moby": {"deadbeef": netHostSpec()}},
		namespaces: []string{"moby", "cornus"},
	}
	insp := selfInspector(f.dialer(), "cornus", nil)

	self, err := insp(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if self.ID != "deadbeef" {
		t.Errorf("ID = %q, want deadbeef", self.ID)
	}
	// cornus's own namespace must be tried before moby: a container in the
	// namespace cornus manages is the more specific claim on our identity.
	if len(f.loaded) < 2 || f.loaded[0] != "cornus/deadbeef" || f.loaded[1] != "moby/deadbeef" {
		t.Errorf("lookup order = %v, want cornus before moby", f.loaded)
	}
}

// TestSelfInspectSurfacesTheDaemonsErrorNotAGenericMiss keeps the preflight able
// to say "permission denied on the socket" instead of "not found", which is the
// difference between an operator fixing a bind and an operator guessing.
func TestSelfInspectSelfIDIsSetFromTheCandidate(t *testing.T) {
	f := &fakeSelfInspect{specs: map[string]map[string]*specs.Spec{"default": {"srv": netHostSpec()}}}
	insp := selfInspector(f.dialer(), "cornus", map[string]string{"srv": "default"})
	self, err := insp(context.Background(), "srv")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// The OCI spec carries no container id, so the Inspector must report the
	// candidate it was asked about; otherwise Env.SelfID comes back empty and the
	// backends that read it (incus's routing hint, containerd's self-preservation)
	// see "not containerized".
	if self.ID != "srv" {
		t.Errorf("ID = %q, want srv", self.ID)
	}
}

// TestSelfCgroupRefsReadsTheContainerdShape pins the measured cgroup spelling and
// the namespace-agnosticism self-inspection depends on.
func TestSelfCgroupRefsReadsTheContainerdShape(t *testing.T) {
	// Measured verbatim from a `ctr run` container.
	refs := selfCgroupRefs("0::/default/spikeself\n")
	if len(refs) != 1 || refs[0].namespace != "default" || refs[0].id != "spikeself" {
		t.Fatalf("refs = %+v, want one {default, spikeself}", refs)
	}
	// A cornus SERVER container is created outside cornus's namespace, which is
	// exactly why this must not anchor on it.
	if id := selfIDFromCgroup("0::/default/spikeself\n", "cornus"); id != "" {
		t.Errorf("selfIDFromCgroup anchored on cornus returned %q; the namespace anchor must still hold for the self-preservation guard", id)
	}
	// A private cgroup namespace hides everything; no refs, no candidates.
	if refs := selfCgroupRefs("0::/\n"); len(refs) != 0 {
		t.Errorf("refs = %+v, want none for a container with its own cgroup namespace", refs)
	}
}

// TestSelfIDCandidatesFeedHostenv documents the contract with pkg/hostenv: the
// ids this package mines are the ONLY route to a containerd container's identity,
// because hostenv's own miners recognize 64-hex ids and the docker/CRI cgroup
// spellings and a containerd id is an arbitrary name.
func TestSelfIDCandidatesFeedHostenv(t *testing.T) {
	var _ hostenv.Inspector = selfInspector(nil, "cornus", nil)
	// Not asserting on the live /proc/self/cgroup content — this test process is
	// not necessarily containerized. The parse itself is covered above; this only
	// pins that the function is total (never panics, empty when there is nothing).
	_ = SelfIDCandidates()
}
