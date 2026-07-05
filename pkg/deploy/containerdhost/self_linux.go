//go:build linux

package containerdhost

// Self-preservation: never stop or remove the container this cornus is itself
// running in.
//
// Same hazard as dockerhost's self.go, and the same honest scoping: a cornus
// container an operator started by hand carries none of cornus's labels and is
// in no enumeration here. The reachable shape is a cornus that was itself
// DEPLOYED by cornus onto the containerd it now drives — it then carries
// cornus.managed / cornus.app like any workload, and an apply, a delete, or a
// compose orphan sweep of that name would tear the server down mid-request.
//
// The self-identity signal is weaker here than on Docker, and it is worth being
// precise about why. hostenv resolves a confirmed container id only for Docker,
// because only the Engine API can be asked "what are THIS container's mounts?";
// its /proc miners look for the 64-hex ids Docker and CRI use, which never match
// cornus's own container ids. So this backend derives its identity from the one
// containerd-specific fact that is both already true and self-anchoring:
// containerd's default OCI spec sets a container's CgroupsPath to
// "/<namespace>/<id>" (containerd/oci.populateDefaultUnixSpec), and cornus
// overrides neither. A cornus running as a container in cornus's OWN containerd
// namespace therefore reads its own container id straight out of
// /proc/self/cgroup, anchored by the namespace so it cannot name anything this
// backend does not manage.
//
// Limits, stated rather than papered over:
//   - the signal is inert whenever the cgroup path is not containerd's default
//     spelling: a container created with a cgroup namespace (which sees a bare
//     "0::/"), a runtime configured with the systemd cgroup driver (which spells
//     it "<slice>:<prefix>:<name>"), or a cornus in some other namespace. In all
//     of those the guard simply does not fire, which is the pre-existing
//     behaviour — it never makes anything worse.
//   - it protects the container cornus runs in, nothing else on the host.
//
// WithSelfContainerID exists so a caller that knows better can say so outright
// and skip the inference entirely.

import (
	"context"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"

	ctd "github.com/containerd/containerd"

	"cornus/pkg/logging"
)

// selfIdentity memoizes the resolved self container id. Cached only once
// settled: a container's id does not change, but an unresolved read must stay
// retryable rather than disarming the guard for the process's lifetime.
type selfIdentity struct {
	mu    sync.Mutex
	id    string
	known bool
}

// selfContainerID returns this process's own container id in the namespace this
// backend manages, or "" when cornus is not running as a container there (or
// cannot tell). See the package note above for the derivation and its limits.
func (b *Backend) selfContainerID() string {
	b.self.mu.Lock()
	defer b.self.mu.Unlock()
	if b.self.known {
		return b.self.id
	}
	content, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		// No procfs (or no permission): unresolved, and retryable.
		return ""
	}
	b.self.id, b.self.known = selfIDFromCgroup(string(content), b.namespace), true
	return b.self.id
}

// selfIDFromCgroup mines a containerd container id out of /proc/self/cgroup
// content, accepting only the "/<namespace>/<id>" shape containerd's default
// spec produces for the namespace this backend manages.
//
// The namespace anchor is what makes this safe rather than a guess: any other
// cgroup layout on the host (systemd slices, Kubernetes pod paths, the host's
// own services) fails the two-segment/namespace test and yields nothing, so the
// function either names a container this backend could actually have created or
// names none at all.
func selfIDFromCgroup(content, namespace string) string {
	if namespace == "" {
		return ""
	}
	for _, line := range strings.Split(content, "\n") {
		// A cgroup line is "hierarchy:controllers:path"; only the path can carry
		// an id.
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		p := path.Clean("/" + strings.TrimPrefix(parts[2], "/"))
		ns, id := path.Split(p)
		if strings.Trim(ns, "/") != namespace || id == "" {
			continue
		}
		return id
	}
	return ""
}

// withoutSelf drops this cornus's own container from a set about to be stopped
// or removed, returning the survivors and the ids withheld. verb and name only
// shape the warning, which is emitted rather than swallowed so an operator who
// has stacked the server and a workload on the same name learns it from the
// first attempt rather than from a deployment that is quietly one replica short.
func (b *Backend) withoutSelf(ctx context.Context, verb, name string, cs []ctd.Container) (kept []ctd.Container, withheld []string) {
	self := b.selfContainerID()
	if self == "" {
		return cs, nil
	}
	kept = make([]ctd.Container, 0, len(cs))
	for _, c := range cs {
		if c.ID() != self {
			kept = append(kept, c)
			continue
		}
		withheld = append(withheld, c.ID())
		logging.FromContext(ctx, slog.Group("containerd", "deployment", name)).
			WarnContext(ctx, "refusing to "+verb+" this cornus server's own container; skipping it",
				"instance", c.ID())
	}
	return kept, withheld
}
