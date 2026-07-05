package dockerhost

// Rootless podman: when port-forward cannot work, when it can, and how the
// difference is reported.
//
// A rootless podman container lives in a network namespace of podman's own,
// behind pasta or slirp4netns. Its address is real INSIDE that namespace and
// meaningless outside it. That is what rootless means; it is not a
// misconfiguration to fix.
//
// The distinction that matters is WHERE THIS PROCESS SITS, not whether the
// daemon is rootless. Measured on Podman 5.8.2 with netavark, against one
// workload at 10.89.0.2:
//
//	from the rootless host netns      -> TimeoutError
//	from a container on that network  -> reachable
//
// So there are two working topologies and one broken one:
//
//   - cornus on the HOST, rootless podman: no route. ForwardPort would dial and
//     time out, and a timeout is the worst way to say this — it reads as "the
//     workload is down" and sends the operator to their application. Hence a
//     PRECONDITION that refuses before dialing, naming both remedies.
//   - cornus IN A CONTAINER on that same podman: it joins each workload's
//     network at Apply time (selfNetworkScope / joinWorkloadNetworks) and dials
//     directly. Nothing extra to configure. This is the topology the refusal
//     must NOT fire in.
//   - CORNUS_PODMAN_REMOTE=1: a per-instance companion shares the workload's
//     namespace and dials back out. Works from anywhere, costs an agent image
//     and an advertise URL.
//
// Remote mode stays an explicit opt-in, matching the other three host backends.
// An earlier design defaulted it ON under rootless; that inverted the project's
// convention AND assumed the broken topology was the only one. Opt-in plus a
// loud, accurate failure is the better trade.

import (
	"context"
	"sync"

	"cornus/pkg/deploy"
)

// rootlessState caches the daemon's answer to "are you rootless".
//
// Cached because ForwardPort runs once per CONNECTION, and an /info round trip
// per connection would ride every forward. Safe to cache for the process's
// lifetime: a daemon does not change its rootlessness while running, and a
// different daemon means a different endpoint and a different Backend.
type rootlessState struct {
	once   sync.Once
	value  bool
	err    error
	probed bool
}

// rootless reports whether the podman daemon behind this Backend runs rootless.
//
// Answers false for any non-podman flavor without asking anything: the question
// is meaningful only for podman, and a Docker engine has no /libpod/info to ask.
//
// A probe FAILURE answers false rather than propagating. That direction is
// deliberate: an unreachable daemon is about to produce its own, better error
// from the actual operation, and turning a transient info failure into a
// port-forward refusal would replace a real diagnosis with a guess.
func (b *Backend) rootless(ctx context.Context) bool {
	if b.flavor != FlavorPodman {
		return false
	}
	eng, ok := b.api.(*podmanEngine)
	if !ok {
		return false
	}
	b.rootlessOnce.once.Do(func() {
		v, err := eng.rootlessInfo(ctx)
		b.rootlessOnce.value, b.rootlessOnce.err, b.rootlessOnce.probed = v, err, true
	})
	return b.rootlessOnce.value
}

// rootlessForwardPortRefusal returns the error ForwardPort must fail with on a
// rootless daemon that this process genuinely cannot reach, or nil when the
// forward may proceed.
//
// "Rootless" alone is NOT the condition, and an earlier version of this that
// refused on rootless && !remote was wrong: it turned away forwards that work.
//
// What rootless actually costs is a route from the HOST's network namespace.
// Podman's bridges live in a namespace of their own, so a host-run cornus cannot
// dial 10.89.x.y — measured on Podman 5.8.2, netavark:
//
//	from the rootless host netns      -> TimeoutError
//	from a container on that network  -> reachable
//
// The second line is the whole point, and it holds MORE broadly than expected.
// Rootless podman keeps all of its bridges inside one rootless network
// namespace, so any container on that daemon can route to any other container's
// address — measured with cornus on network "testnet" reaching a workload on
// "appnet" with no shared network between them. Being a container on this podman
// is therefore sufficient on its own; unlike Docker, where inter-network
// isolation is enforced and selfnet.go must attach the server to the workload's
// network to gain a route.
//
// So the condition is co-residency, and selfNetworkScope is the existing
// predicate for it: a network namespace of our own, not already remote, and a
// CONFIRMED id on THIS daemon. The confirmation is what makes it mean "a
// container this daemon holds" rather than merely "containerized" — a process in
// some unrelated container is exactly as unable to reach the workload as a host
// process is. (It is also what keeps joinWorkloadNetworks from attaching a
// stranger to the workload's network on the backends that do need to join.)
//
// This mirrors the exception the incus backend already carves out for a cornus
// running as an instance on the daemon it drives.
func (b *Backend) rootlessForwardPortRefusal(ctx context.Context, name string) error {
	if b.remote || !b.rootless(ctx) {
		return nil
	}
	if _, coResident := b.selfNetworkScope(ctx); coResident {
		return nil // in the workload's namespace; the direct dial is the right path
	}
	return b.errf("cannot reach %q: this podman daemon is rootless, so a workload's network "+
		"namespace (pasta/slirp4netns) is not routable from this host — no dial can succeed. "+
		"Either set CORNUS_PODMAN_REMOTE=1 to reach workloads through a per-instance companion "+
		"that shares their namespace (that mode also needs CORNUS_AGENT_IMAGE and "+
		"CORNUS_ADVERTISE_URL), or run this cornus as a container on this same podman, on a "+
		"podman network rather than --network host: it then joins each workload's network and "+
		"dials it directly",
		name)
}

// podmanRootlessHint is the clause unreachableHint adds for a rootless daemon
// this process could not reach. It names both remedies, in the order an operator
// is most likely to want them.
const podmanRootlessHint = " (this podman daemon is rootless, so a workload's network namespace is not" +
	" routable from the host; run this cornus as a container on this same podman — on a podman network," +
	" not --network host — so it joins the workload's network, or set CORNUS_PODMAN_REMOTE=1 to reach it" +
	" through a per-instance companion instead)"

var _ deploy.CrossNamespaceMounter = (*Backend)(nil)

// MountsCrossNamespace implements deploy.CrossNamespaceMounter.
//
// Rootless podman runs its containers in a mount namespace held by its pause
// process, so a mount this server makes reaches them only by propagation. Rootful
// docker and rootful podman share this server's namespace and need none.
func (b *Backend) MountsCrossNamespace(ctx context.Context) bool { return b.rootless(ctx) }
