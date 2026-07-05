package dockerhost

// Routing to workloads from a server running as an ordinary HOST PROCESS — the
// default topology, and the counterpart of selfnet.go's containerized one.
//
// The comment `pickNetworkIP` carried for a long time said the default bridge is
// the network "which the server host can route to", with the unstated premise
// that the host can route to all of them. For bridge networks that premise
// holds, including ones marked `internal: true` — measured on Docker 29.2.1, a
// host->internal-network dial SUCCEEDS, because `internal` blocks the network's
// own egress and inter-network traffic, not the host's access to it. So no
// special case is needed there, and one would have been wrong.
//
// It does NOT hold for the L2 drivers. A macvlan (and an ipvlan in l2 mode)
// child interface cannot talk to its own parent, by kernel design — the host is
// specifically the one peer such a container cannot reach and that cannot reach
// it. Measured on the same host: a `nginx:alpine` on a macvlan network is
// UNREACHABLE from the host on port 80, while the identical container on a
// bridge network is reachable. cornus passes `driver:` straight through to
// Docker (only the three kubernetes pseudo-drivers are filtered out in
// networkEnsure), so a compose file may perfectly legitimately ask for one.
//
// Two consequences, and this file addresses both:
//
//   - A workload on a macvlan network AND a bridge network has two addresses,
//     one dialable and one not, and pickNetworkIP's tie-break is the network
//     NAME. Which address ForwardPort chose was therefore decided by
//     alphabetical order — deterministic, and arbitrarily right or wrong.
//   - A workload on ONLY host-isolated networks cannot be reached at all, and
//     used to fail as a bare dial timeout minutes later, with nothing naming the
//     cause.
//
// The driver lookup is cached for the backend's lifetime because a network's
// driver cannot change: a network is created with one and destroyed with it.
// That matters here — ForwardPort runs once per CONNECTION, so an uncached
// inspect per network would ride every connection of every forward.

import (
	"context"
)

// hostIsolatedDriver are the Docker network drivers whose members the host
// cannot reach, however the routing table looks. Both are the same kernel
// behaviour: a macvlan/ipvlan child is invisible to its parent interface.
//
// ipvlan is included on its l2 mode, which is the default and the only one
// Docker configures without an explicit `ipvlan_mode` driver option. An l3
// ipvlan is routable from the host, so this is conservative in the direction
// that costs a preference rather than a capability — being demoted only moves an
// address DOWN the choice order, and a workload with no other network is still
// dialed exactly as before.
var hostIsolatedDriver = map[string]bool{"macvlan": true, "ipvlan": true}

// networkDriverCached returns a network's driver, remembering it. An
// unresolvable network yields "", which is not in hostIsolatedDriver and so
// reads as "routable" — the historical assumption, kept for anything this cannot
// answer for.
func (b *Backend) networkDriverCached(ctx context.Context, name string) string {
	b.driverMu.Lock()
	if drv, ok := b.driverCache[name]; ok {
		b.driverMu.Unlock()
		return drv
	}
	b.driverMu.Unlock()

	drv, err := b.api.networkDriver(ctx, name)
	if err != nil {
		return "" // transient: do not cache a failure as a fact
	}
	b.driverMu.Lock()
	defer b.driverMu.Unlock()
	if b.driverCache == nil {
		b.driverCache = map[string]string{}
	}
	b.driverCache[name] = drv
	return drv
}

// hostRoutableNetworks narrows a workload's networks to the ones a host-process
// server can actually dial, and reports whether anything was excluded.
//
// A nil set means "no opinion" and is the common answer: it is returned whenever
// nothing is host-isolated, so the overwhelmingly normal all-bridge deployment
// keeps byte-for-byte the previous address choice. isolated is reported
// separately from the set being empty so the caller can tell "everything this
// workload has is unreachable" — a diagnosable state — from "nothing to say".
//
// nets is the caller's already-fetched name -> address map, so this adds no
// container inspect of its own; the driver lookups behind it are cached.
func (b *Backend) hostRoutableNetworks(ctx context.Context, nets map[string]string) (routable map[string]bool, isolated bool) {
	if len(nets) == 0 {
		return nil, false
	}
	routable = make(map[string]bool, len(nets))
	for n, addr := range nets {
		if addr == "" {
			continue // an unaddressed endpoint is no route either way
		}
		if hostIsolatedDriver[b.networkDriverCached(ctx, n)] {
			isolated = true
			continue
		}
		routable[n] = true
	}
	if !isolated {
		return nil, false
	}
	return routable, true
}
