package dockerhost

// Self-preservation: never stop or remove the container this cornus is itself
// running in.
//
// In server-in-a-container mode cornus drives the very daemon that runs it, so
// every enumeration this backend performs — `cornus.app=<name>` for the
// lifecycle verbs and the recreate step, `cornus.managed=true` for List, which
// is what the compose orphan sweep filters — is in principle capable of
// returning cornus's own container.
//
// How reachable that actually is, honestly: a cornus container started by an
// operator (`docker run ... cornus serve`) carries none of cornus's management
// labels, so it matches no filter here and was never at risk. The reachable
// shape is narrower and quite specific: a cornus that was itself DEPLOYED by
// cornus onto the same daemon. Then it carries cornus.managed=true and
// cornus.app=<its own service>, and an `apply`/`delete` of that name — or a
// `compose up --remove-orphans` after that service was renamed out of the file
// — enumerates it like any other workload and tears it down mid-request. So
// this guard is defence in depth for a self-hosting topology, not a fix for a
// hole every containerized server was standing over.
//
// The identity signal is the one the in-container mode already established for
// host-path translation (pkg/hostenv): the candidate container ids mined from
// /proc (runtime-managed bind mounts in mountinfo, the cgroup path, HOSTNAME),
// each CONFIRMED by inspecting it on this very daemon before it is trusted.
// Reusing it matters — a self-id guessed wrong is worse than none, since it
// would make some unrelated workload permanently undeletable.
//
// Limits worth stating plainly:
//   - the guard is only as good as the self-id. A plain cgroup v2 container with
//     no runtime-managed binds and a custom hostname names itself nowhere in
//     /proc, so hostenv resolves nothing and the guard is inert. That is the same
//     population for which host-path translation already requires an explicit
//     CORNUS_HOST_PATH_MAP, and it fails safe in the sense that it is exactly the
//     pre-existing behaviour.
//   - it protects the container cornus runs in, not the daemon's other
//     infrastructure. A sibling container that cornus depends on is not covered.

import (
	"context"
	"log/slog"
	"strings"

	"cornus/pkg/hostenv"
	"cornus/pkg/logging"
)

// WithSelfContainerID pins the id of the container this cornus process runs in,
// so the destructive paths can refuse to act on it. Callers that already ran the
// server preflight (pkg/hostenv's Detect, whose Env.SelfID is the confirmed id)
// should pass it here rather than making the backend repeat the detection.
//
// An empty id is ignored rather than recorded as "known to be on the host",
// because "" is also what Detect yields when it merely could not tell — pinning
// it would silently disarm the guard. Pass nothing to let the backend resolve
// its own identity lazily (selfContainerID).
func WithSelfContainerID(id string) Option {
	return func(b *Backend) {
		if id != "" {
			b.selfID, b.selfIDKnown = id, true
		}
	}
}

// selfContainerID returns this process's own container id on the daemon this
// backend drives, or "" when cornus is not containerized (or cannot tell).
//
// It runs hostenv's detection with this backend's OWN engine client as the
// inspector, so "the daemon" can never mean a different daemon than the one
// about to be mutated. Detection is skipped entirely for a process that does not
// look containerized, so a server on the host pays one /proc read and no daemon
// round-trip.
//
// A resolved answer is cached for the backend's lifetime (a container's id does
// not change). An UNRESOLVED one deliberately is not: a socket hiccup during the
// first Delete must not disarm the guard for the rest of the process's life. The
// retry costs two small /proc reads on a path that is already doing container
// teardown.
func (b *Backend) selfContainerID(ctx context.Context) string {
	b.selfMu.Lock()
	defer b.selfMu.Unlock()
	if b.selfIDKnown {
		return b.selfID
	}
	// Name the runtime this Backend actually drives. hostenv does not branch on
	// it today (Detect only carries it into Env.Runtime), so this is currently
	// cosmetic — but a hardcoded "docker" on a podman backend is a fact waiting
	// to become a bug the moment detection gains a runtime-dependent path, and it
	// misreports the runtime to anything that reads Env.Runtime.
	runtime := hostenv.RuntimeDocker
	if b.flavor == FlavorPodman {
		runtime = hostenv.RuntimePodman
	}
	env, _, err := hostenv.Detect(ctx, hostenv.Options{
		Runtime: runtime,
		Inspect: b.api.selfInspect,
	})
	if err != nil || env.Err != nil {
		return ""
	}
	b.selfID, b.selfIDKnown = env.SelfID, true
	return b.selfID
}

// sameContainer reports whether two container ids name the same container.
//
// Ids are compared with prefix tolerance in both directions because Docker hands
// out an abbreviated 12-hex form (it is a container's default HOSTNAME, and the
// API accepts it wherever a full id is accepted), and an operator-supplied
// WithSelfContainerID may well carry that spelling while /containers/json always
// reports the full 64. Twelve is the floor: it is the shortest form Docker
// itself produces, and accepting less would let a two-character coincidence
// declare an unrelated workload undeletable.
func sameContainer(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 12 && strings.HasPrefix(b, a)
}

// withoutSelf drops this cornus's own container from a set about to be stopped
// or removed, returning the survivors and the ids withheld. verb and name only
// shape the warning.
//
// It warns rather than staying silent: a deployment that quietly comes back one
// replica short is its own kind of bug report, and the operator needs to know
// the workload and the server have been deployed on top of each other.
func (b *Backend) withoutSelf(ctx context.Context, verb, name string, cs []containerSummary) (kept []containerSummary, withheld []string) {
	self := b.selfContainerID(ctx)
	if self == "" {
		return cs, nil
	}
	kept = make([]containerSummary, 0, len(cs))
	for _, c := range cs {
		if !sameContainer(c.ID, self) {
			kept = append(kept, c)
			continue
		}
		withheld = append(withheld, c.ID)
		logging.FromContext(ctx, slog.Group("dockerhost", "deployment", name)).
			WarnContext(ctx, "refusing to "+verb+" this cornus server's own container; skipping it",
				"container", c.ID)
	}
	return kept, withheld
}
