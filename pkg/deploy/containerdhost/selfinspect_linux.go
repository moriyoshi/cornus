//go:build linux

package containerdhost

// Self-inspection: what are THIS container's mounts, and does it share the host's
// network namespace?
//
// Answering those two questions is what makes a containerized server on a
// containerd host self-configuring, the way one on a docker host already is
// (pkg/deploy/dockerhost/selfinspect.go). Without it, hostenv has no evidence
// that cornus's paths and containerd's diverge, so it hands back the IDENTITY
// mapper — whose ToHost never fails. Every path the backend then gives containerd
// is the container's own spelling, and containerd creates each one fresh and
// empty on the host: volumes with no data, no managed /etc/hosts, and a log shim
// that is never staged, so `cornus logs` returns nothing for a healthy container.
// All of it silent, and the preflight's one fatal check (dataDirCheck) sits behind
// Translating, so it cannot fire in exactly that configuration.
//
// The reachable topology is a server container created by the SAME containerd it
// manages — the native `ctr`/`nerdctl`/CRI story, which is the whole point of
// having a containerd backend. A server started by DOCKER on a containerd host
// lives in docker's own containerd (namespace "moby", a different socket), so
// LoadContainer here will not find it; that degrades to the pre-existing
// behaviour (Env.Err set, identity mapper, the unverified-paths warning naming
// CORNUS_HOST_PATH_MAP), which is correct rather than merely tolerable.
//
// Measured against containerd 2.2.6 (see .agents/docs/JOURNAL.md):
//
//   - the default spec's cgroupsPath is "/<namespace>/<id>" and no cgroup
//     namespace is created, so a process inside reads "0::/default/<id>";
//   - spec.Hostname is EMPTY — a containerd container inherits the host's
//     hostname, so unlike docker the hostname is neither evidence nor a route to
//     the id. The cgroup is the only source, which is why SelfIDCandidates exists;
//   - `--net-host` omits the network namespace entry entirely; without it an
//     entry of type "network" is present. That is the host-netns signal;
//   - the default spec binds /etc/hosts and /etc/resolv.conf from the host, which
//     is why reportableMounts filters them out (see its doc).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/hostenv"
)

// selfInspectClient is the containerd surface self-inspection needs. It exists so
// the mapping below is unit-testable against a fake: the rules that matter here
// (which mounts count as evidence, what absence of a network namespace means) are
// pure data transformations, and gating them behind a live daemon would leave
// them untested in the default `go test ./...` path.
type selfInspectClient interface {
	// Namespaces lists the daemon's namespaces.
	Namespaces(ctx context.Context) ([]string, error)
	// LoadSpec returns the OCI spec of container id in namespace ns.
	LoadSpec(ctx context.Context, ns, id string) (*specs.Spec, error)
	Close() error
}

// SelfIDCandidates returns this process's own container id as containerd spells
// it, for hostenv to try before its own runtime-neutral candidates.
//
// hostenv's miners cannot find it: they match 64-hex ids and the docker/CRI
// cgroup spellings (pkg/hostenv/selfid.go), and a containerd container id is an
// arbitrary name — `ctr run ... myserver` yields "/default/myserver". So the
// candidate has to be supplied from here, where the shape is known.
func SelfIDCandidates() []string {
	content, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil
	}
	refs := selfCgroupRefs(string(content))
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.id)
	}
	return ids
}

// SelfInspector returns a hostenv.Inspector backed by the containerd daemon this
// cornus drives, so a containerized server can learn its own mounts and network
// mode and derive the host paths it must hand back to that same daemon.
//
// It resolves the socket through ResolveAddress, the same precedence New uses, so
// self-inspection and the deploy path cannot end up talking to different daemons.
//
// The returned Inspector dials per call and closes again. hostenv calls it only
// when the process looks containerized, and then at most once per candidate id, so
// this trades a few short-lived connections for not holding a grpc conn open for
// the process's lifetime to serve a startup question.
func SelfInspector(cfg Config) (hostenv.Inspector, error) {
	address, namespace := ResolveAddress(cfg.Address), ResolveNamespace(cfg.Namespace)
	dial := func() (selfInspectClient, error) {
		c, err := ctd.New(address)
		if err != nil {
			return nil, fmt.Errorf("containerd: connect %s: %w", address, err)
		}
		return realSelfInspectClient{c}, nil
	}
	return selfInspector(dial, namespace, selfNamespaceByID()), nil
}

// selfNamespaceByID pairs each id our own cgroup path names with the namespace it
// named it in, so the lookup can go straight to that namespace instead of
// searching. The cgroup path carries both halves; throwing the namespace away and
// then hunting for it would be strictly worse.
func selfNamespaceByID() map[string]string {
	content, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil
	}
	nsOf := make(map[string]string)
	for _, ref := range selfCgroupRefs(string(content)) {
		// First ref wins: selfCgroupRefs preserves /proc/self/cgroup's order, and
		// a later duplicate id in some other namespace is the less specific claim.
		if _, ok := nsOf[ref.id]; !ok {
			nsOf[ref.id] = ref.namespace
		}
	}
	return nsOf
}

// selfInspector is SelfInspector's testable core: everything but the dialer.
func selfInspector(dial func() (selfInspectClient, error), namespace string, nsOf map[string]string) hostenv.Inspector {
	return func(ctx context.Context, id string) (hostenv.SelfInspect, error) {
		client, err := dial()
		if err != nil {
			return hostenv.SelfInspect{}, err
		}
		defer client.Close()

		var lastErr error
		for _, ns := range namespaceOrder(ctx, client, namespace, nsOf[id]) {
			spec, err := client.LoadSpec(ctx, ns, id)
			if err != nil {
				lastErr = err
				continue
			}
			return specToSelfInspect(id, spec), nil
		}
		if lastErr != nil {
			return hostenv.SelfInspect{}, lastErr
		}
		return hostenv.SelfInspect{}, fmt.Errorf("containerd: container %s not found in any namespace", id)
	}
}

// namespaceOrder is the deterministic order of containerd namespaces to look for
// our own container in.
//
// Deterministic on purpose. Ranging a map here would resolve self-inspection
// differently between restarts of the same server, which is the same defect
// incushost's pickIPv4 had: a lookup that works about half the time is harder to
// diagnose than one that never does.
//
// Order: the namespace our own cgroup path named (exact), then the one cornus
// deploys into, then the two conventional ones a server container is likely to
// have been created in, then whatever else the daemon holds, sorted.
func namespaceOrder(ctx context.Context, client selfInspectClient, cornusNS, cgroupNS string) []string {
	var order []string
	seen := map[string]bool{}
	add := func(ns string) {
		if ns == "" || seen[ns] {
			return
		}
		seen[ns] = true
		order = append(order, ns)
	}
	add(cgroupNS)
	add(cornusNS)
	add("moby")    // docker's
	add("default") // ctr's and nerdctl's
	rest, err := client.Namespaces(ctx)
	if err == nil {
		sort.Strings(rest)
		for _, ns := range rest {
			add(ns)
		}
	}
	return order
}

// specToSelfInspect maps an OCI spec onto the subset hostenv uses.
func specToSelfInspect(id string, spec *specs.Spec) hostenv.SelfInspect {
	self := hostenv.SelfInspect{ID: id, Hostname: spec.Hostname}
	if sharesHostNetwork(spec) {
		self.NetworkMode = "host"
	}
	self.Mounts = reportableMounts(spec.Mounts)
	return self
}

// sharesHostNetwork reports whether the spec leaves this container in the host's
// network namespace.
//
// OCI spells that as the ABSENCE of a network entry in linux.namespaces: a
// namespace listed with an empty Path means "make a new one", and one with a Path
// means "join that one" — which is some other netns, not the host's. So only
// absence may read as host, and every present entry must read as isolated.
func sharesHostNetwork(spec *specs.Spec) bool {
	if spec.Linux == nil {
		// No linux section at all: nothing claims a network namespace.
		return true
	}
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			return false
		}
	}
	return true
}

// runtimePlumbingMounts are the per-container files a runtime binds into every
// container it creates. They must not be reported to hostenv.
//
// Not tidiness — correctness. hostenv.confirmSelf accepts a candidate id as soon
// as ONE reported mount destination is a mount point in our own table, precisely
// so a wrong id cannot produce a confident path map. Every one of these is a
// mount point in every container, so reporting them would confirm ANY candidate
// and defeat that guard. They are also worthless as map entries: each is a
// per-container file, so mapping the container's /etc/hosts to the host's says
// nothing true about any other path.
var runtimePlumbingMounts = map[string]bool{
	"/etc/hosts":       true,
	"/etc/hostname":    true,
	"/etc/resolv.conf": true,
}

// reportableMounts narrows a spec's mounts to the ones that are evidence of
// identity and useful as path-map entries: real binds, from an absolute host
// source, that the runtime did not add on its own behalf.
func reportableMounts(mounts []specs.Mount) []hostenv.MountPoint {
	var out []hostenv.MountPoint
	for _, m := range mounts {
		if m.Type != "bind" && m.Type != "rbind" {
			// proc, sysfs, tmpfs, devpts, mqueue: no host path to correspond to.
			continue
		}
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Destination) {
			continue
		}
		if runtimePlumbingMounts[filepath.Clean(m.Destination)] {
			continue
		}
		out = append(out, hostenv.MountPoint{Source: m.Source, Destination: m.Destination})
	}
	return out
}

// realSelfInspectClient adapts a containerd client to selfInspectClient.
type realSelfInspectClient struct{ c *ctd.Client }

func (r realSelfInspectClient) Namespaces(ctx context.Context) ([]string, error) {
	return r.c.NamespaceService().List(ctx)
}

func (r realSelfInspectClient) LoadSpec(ctx context.Context, ns, id string) (*specs.Spec, error) {
	container, err := r.c.LoadContainer(namespaces.WithNamespace(ctx, ns), id)
	if err != nil {
		return nil, err
	}
	return container.Spec(namespaces.WithNamespace(ctx, ns))
}

func (r realSelfInspectClient) Close() error { return r.c.Close() }
