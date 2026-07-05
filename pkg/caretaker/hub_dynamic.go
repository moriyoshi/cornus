package caretaker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"

	"github.com/hashicorp/yamux"
	"golang.org/x/sync/errgroup"

	"cornus/pkg/hub"
	"cornus/pkg/logging"
)

// runDynamicReach is the caretaker side of dynamic import discovery. The
// registration already asked the hub to push catalog updates (Registration.Watch),
// so this reads hub.CatalogUpdate frames off the control stream and rebinds: a
// service newly in the catalog gets a reach listener at hub.SyntheticIP(name) on
// the configured ports; a service that vanished has its listener closed.
//
// DNS publication: each successful bind also publishes name -> synthetic IP into
// the process-wide dynamic DNS overlay (DynamicDNS), and each unbind withdraws
// it — so when the pod also runs the dns role, `dial(name)` resolves to the
// listener just bound, with no records pre-computed at deploy time. With no dns
// role in the process the overlay is simply never served. Static (deploy-time)
// records shadow dynamic ones of the same name.
//
// Rebind semantics: unbinding cancels only the listener's context, which closes
// the listening socket — already-accepted TCP relays are detached goroutines and
// drain naturally (they run until either side closes). UDP flows are the
// exception: closing the bound packet socket cuts live flows, which is acceptable
// because a service that left the catalog has no live provider anyway.
//
// Version tolerance: against an old server that never pushes catalog frames the
// Decode simply blocks for the connection's life and no dynamic import ever
// appears (static behavior). The read unblocks when the session closes (the
// connection holder closes it on ctx cancellation), which reads as ctx done here.
func runDynamicReach(ctx context.Context, ctl net.Conn, sess *yamux.Session, role *HubRole) error {
	spec := *role.ReachDynamic
	// Names this spoke must never dynamically import: its own hosted services and
	// the statically-configured reaches (their listeners are owned elsewhere).
	skip := map[string]bool{}
	for _, svc := range role.Register {
		skip[svc.Name] = true
	}
	for _, p := range role.Reach {
		skip[p.Name] = true
	}

	active := map[string]context.CancelFunc{} // service name -> unbind
	defer func() {
		for name, cancel := range active {
			cancel()
			dnsDynamic.Remove(name)
		}
	}()

	ctx = logging.WithAttrs(ctx, slog.String("component", "hub"))
	log := logging.FromContext(ctx)
	dec := json.NewDecoder(ctl)
	for {
		var upd hub.CatalogUpdate
		if err := dec.Decode(&upd); err != nil {
			if ctx.Err() != nil {
				return nil // connection teardown, not a watch failure
			}
			// The control stream died under a live context: the connection is no
			// longer usable for discovery, so fail fast (the sidecar restarts).
			return fmt.Errorf("hub: catalog watch: %w", err)
		}
		desired := map[string]bool{}
		for _, name := range upd.Services {
			if name != "" && !skip[name] {
				desired[name] = true
			}
		}
		// Unbind vanished imports: close the listener (drain — see the doc above)
		// and withdraw the dynamic DNS record so the name stops resolving.
		for name, cancel := range active {
			if !desired[name] {
				cancel()
				dnsDynamic.Remove(name)
				delete(active, name)
			}
		}
		// Bind newly-appeared imports. A bind failure (e.g. a port collision on
		// the synthetic IP) is logged and skipped, not fatal: unlike the static
		// reach set, the dynamic set was not explicitly promised by the operator.
		for name := range desired {
			if _, ok := active[name]; ok {
				continue
			}
			nctx, cancel := context.WithCancel(ctx)
			ng, ngctx := errgroup.WithContext(nctx)
			peer := HubPeer{Name: name, Listen: hub.SyntheticIP(name), Ports: spec.Ports, Protocol: spec.Protocol}
			if err := startReachListeners(ngctx, ng, sess, []HubPeer{peer}); err != nil {
				cancel()
				log.WarnContext(ctx, "dynamic reach bind failed", "service", name, "listen", peer.Listen, "error", err)
				continue
			}
			go func() { _ = ng.Wait() }() // reap the listener goroutines on unbind
			// The listener is live: publish the name into the dynamic DNS overlay
			// (atomic with the bind — the name never resolves to a dead address).
			dnsDynamic.Set(name, peer.Listen)
			active[name] = cancel
		}
	}
}
