package dockerhost

// Routing to workloads from a server that is itself a container.
//
// Docker's inter-network isolation (the DOCKER-ISOLATION-STAGE-* chains) drops
// traffic between two bridge networks. The HOST can reach every one of them, so
// a cornus running as a host process sees a flat, fully routable space and this
// file is inert for it. A cornus running as a CONTAINER sees only the networks
// it is a member of — and a deployment that declares `networks:` puts its
// workload on a user-defined network the server is, by default, not on. Measured
// on Docker 29.2.1: container-on-bridge -> container-on-user-network is
// UNREACHABLE, while host -> the same address is reachable. That is the whole
// bug. It has no deploy-time symptom; the workload starts perfectly and the
// server simply cannot talk to it (port-forward, tunnel, ingress) and cannot be
// talked to (a companion caretaker dialing back, a workload exporting telemetry
// to an advertise URL naming the server's container).
//
// The fix is the same one docker-compose-in-docker and testcontainers use: put
// the server on the workload's network. Every Apply attaches this cornus's own
// container to the networks it is about to ensure, and Delete detaches it again
// as part of network GC.
//
// Why attaching is safe, measured rather than assumed (Docker 29.2.1):
//
//   - Joining a user-defined network from the default bridge does NOT move the
//     container's default route; the bridge keeps the gateway. Cornus's own
//     outbound connectivity (registry pulls, tunnel providers) is unaffected.
//   - Joining an `internal: true` network does not move it either — such a
//     network has no gateway to steal it with.
//   - The reverse, joining the default bridge from a user-defined network, DOES
//     move the default route. So this file never joins the default bridge: a
//     workload with no `networks:` lands there, and a server that is not already
//     on it stays off it rather than re-homing its own egress behind cornus's
//     back. ForwardPort reports that case as unreachable, with the hint.
//
// Endpoint configuration is deliberately empty (networkJoin, not
// networkConnect): the server is a guest on the workload's network, not a member
// of the deployment, so it must not publish the deployment's DNS aliases.
//
// Scope: co-located mode only. Remote mode (CORNUS_DOCKER_REMOTE) reaches
// workloads through a per-instance companion and its daemon need not even be
// this machine's, so joining a network on it would be meaningless.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// selfNetworkScope reports whether this backend should manage its own network
// memberships at all, returning the server's own container id when it should.
//
// All three conditions are load-bearing. isolatedNetwork is the symptom (a netns
// of our own); non-remote is the topology this path serves; and a CONFIRMED self
// id — hostenv resolves candidates from /proc and verifies each against this very
// daemon — is what keeps a wrong guess from attaching some unrelated container to
// the workload's network.
func (b *Backend) selfNetworkScope(ctx context.Context) (string, bool) {
	if !b.isolatedNetwork || b.remote {
		return "", false
	}
	id := b.selfContainerID(ctx)
	return id, id != ""
}

// joinWorkloadNetworks attaches this cornus's own container to each of a
// deployment's user-defined networks, so the server can route to the workload it
// is about to start (and the workload back to it).
//
// Best-effort by design: a deploy whose workload is perfectly healthy must not
// fail because the SERVER could not give itself a route to it. The deployment is
// the user's request; server reachability is a capability layered on top, and it
// already reports its own absence at the point of use — ForwardPort's
// unreachableHint names both remedies. So a failure here is a warning that says
// what will not work, not an error that discards a working deploy.
func (b *Backend) joinWorkloadNetworks(ctx context.Context, deployment string, nets []api.NetworkAttachment) {
	if len(nets) == 0 {
		return
	}
	self, ok := b.selfNetworkScope(ctx)
	if !ok {
		return
	}
	log := logging.FromContext(ctx, slog.Group("dockerhost", "deployment", deployment))
	for _, n := range nets {
		if n.Name == "" {
			continue
		}
		if err := b.api.networkJoin(ctx, n.Name, self); err != nil {
			log.WarnContext(ctx, "could not attach this cornus server's own container to the workload's network; port-forward, tunnels and caretaker dial-backs for this deployment will not reach it",
				"network", n.Name, "container", self, "error", err,
				"remedy", "run the server container with --network host, or set CORNUS_DOCKER_REMOTE=1 to reach workloads through a per-instance companion")
			continue
		}
		log.DebugContext(ctx, "attached this cornus server's own container to the workload's network",
			"network", n.Name, "container", self)
	}
}

// addressPoolHint explains cornus's own contribution to an exhausted network
// address pool, or "" when it made none.
//
// Attaching the server to a workload's network consumes one address from that
// network's IPAM pool, ahead of the replicas — measured: a `/29` subnet (six
// usable addresses) fits five replicas instead of six once cornus is on it, and
// the sixth fails at container START with the daemon's "no available IPv4
// addresses on this network's address pools". That message is loud but it names
// only the network, so an operator counting their own replicas against their own
// subnet has no way to discover the extra tenant. This says it.
//
// Deliberately narrow: it fires only on the daemon's address-pool wording, and
// only when this cornus is in fact attached to something, so it cannot editorialize
// on an unrelated start failure.
func (b *Backend) addressPoolHint(ctx context.Context, err error) string {
	if err == nil || !strings.Contains(err.Error(), "no available IPv4 addresses") {
		return ""
	}
	if _, ok := b.selfNetworkScope(ctx); !ok {
		return ""
	}
	return " (this cornus runs in a container and attaches itself to the networks it deploys onto, so it holds one address from this network's pool;" +
		" widen the subnet or ip_range by one, or run the server with --network host so it needs no endpoint of its own)"
}

// reachableNetworks returns the names of the networks this cornus's own
// container is attached to, or nil when it shares the host's network view.
//
// nil is the meaningful zero: it tells containerIP that every network is
// routable, which is the truth for a server on the host and for one in the
// host's netns. A containerized server that cannot resolve its own id also
// yields nil — it has no better information, and preserving the historical
// behaviour there beats narrowing the choice on a guess.
func (b *Backend) reachableNetworks(ctx context.Context) map[string]bool {
	self, ok := b.selfNetworkScope(ctx)
	if !ok {
		return nil
	}
	nets, err := b.api.containerNetworks(ctx, self)
	if err != nil || len(nets) == 0 {
		return nil
	}
	out := make(map[string]bool, len(nets))
	for n := range nets {
		out[n] = true
	}
	return out
}

// defaultBridge is Docker's default bridge network. It is named here because it
// is the one network cornus never attaches ITSELF to: measured on Docker 29.2.1,
// connecting to it moves a container's default route, so an automatic join would
// silently re-home the server's own egress. A workload that lives only there,
// seen from a server that is not on it, stays an operator-visible error with a
// hint rather than a surprise.
const defaultBridge = "bridge"

// instanceIP resolves the address ForwardPort should dial for a workload
// container, attaching this cornus to the container's networks if that is what
// it takes to have a route at all.
//
// The retry is not belt-and-braces; it is the fix for a specific, reproduced
// failure. Apply's join creates a real Docker endpoint on the SERVER's
// container, and endpoints belong to a container, not to an image or a name — so
// they survive `docker restart` and do NOT survive `docker rm` + `docker run`,
// which is exactly how a cornus server is upgraded. After an upgrade the
// workloads keep running, unchanged and healthy, while the new server container
// has no route to any of them; and because the old error blamed the network
// namespace, it pointed at `--network host` when the actual remedy was
// "re-apply, or wait for something to re-join". Joining on demand also picks up
// deployments created before this mechanism existed.
// A host-process server takes a different narrowing: it can route to every
// bridge network, including `internal` ones, but not to a macvlan/ipvlan one
// (see hostisolation.go). The two are mutually exclusive by construction — a
// server is either in a container of its own or it is not — so the second is
// consulted only when the first has no opinion.
func (b *Backend) instanceIP(ctx context.Context, name, id string) (string, error) {
	nets, bridgeIP, err := b.api.containerAddresses(ctx, id)
	if err != nil {
		return "", err
	}
	reachable := b.reachableNetworks(ctx)
	if reachable == nil {
		routable, isolated := b.hostRoutableNetworks(ctx, nets)
		if isolated && len(routable) == 0 {
			return "", fmt.Errorf("container %s is only on host-isolated network(s): a macvlan/ipvlan member cannot be reached from its own host, "+
				"so this server has no route to it (publish a port, put the workload on a bridge network as well, or reach it from off-host)", id)
		}
		reachable = routable
	}
	if ip := selectIP(nets, bridgeIP, reachable); ip != "" {
		return ip, nil
	}
	if !b.rejoinNetworksOf(ctx, name, id, nets, reachable) {
		return "", noRouteError(id)
	}
	if ip := selectIP(nets, bridgeIP, b.reachableNetworks(ctx)); ip != "" {
		return ip, nil
	}
	return "", noRouteError(id)
}

// rejoinNetworksOf attaches this cornus to the user-defined networks a workload
// container is already on, reporting whether anything was joined.
//
// It takes the networks off the CONTAINER rather than off a spec, because the
// caller may be serving a deployment this process never applied — and takes them
// from the inspect instanceIP already did, so the retry costs no extra daemon
// round-trip on the way in. A network with no ADDRESS on it is not a routing
// problem: it is a container that is not running (a stopped one still lists
// every network it was created on, with empty addresses), so nothing is joined
// and the original verdict stands.
func (b *Backend) rejoinNetworksOf(ctx context.Context, name, id string, nets map[string]string, reachable map[string]bool) bool {
	self, ok := b.selfNetworkScope(ctx)
	if !ok {
		return false
	}
	log := logging.FromContext(ctx, slog.Group("dockerhost", "deployment", name))
	joined := false
	for n, addr := range nets {
		if n == "" || addr == "" || n == defaultBridge || reachable[n] {
			continue
		}
		if err := b.api.networkJoin(ctx, n, self); err != nil {
			log.WarnContext(ctx, "could not attach this cornus server's own container to a running workload's network",
				"network", n, "container", self, "error", err)
			continue
		}
		joined = true
		log.InfoContext(ctx, "attached this cornus server's own container to a running workload's network (a server that was replaced does not inherit the endpoints of the one it replaced)",
			"network", n, "container", self)
	}
	return joined
}
