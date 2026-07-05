// Package hub is cornus's workload-to-workload overlay: the cornus server is
// a star hub that spokes (pod caretakers) dial, register the services they host,
// and open data streams to reach other registered services.
//
// A registered service is reached one of two ways:
//   - dial-direct (Phase 1): the hub dials a hub-reachable address and splices;
//   - delivery (Phase 2): the hub opens an ingress stream to the hosting spoke,
//     which dials its own local target and splices — so the target need not be
//     reachable from the hub (a NAT'd or cross-cluster spoke).
//
// See .agents/docs/ARCHITECTURE.md ("Workload-to-workload hub").
package hub

import (
	"sort"
	"sync"

	"github.com/hashicorp/yamux"
)

// Target is how the hub reaches a registered service: dial Addr directly, or, when
// Addr is empty, deliver an ingress stream over Mux to the hosting spoke. Protocol
// is "tcp" (default, empty) or "udp"; it matters only for a dial-direct target, so
// the hub knows whether to open a UDP socket and datagram-bridge (delivery is
// byte-agnostic — the already-framed datagrams pass through unchanged).
type Target struct {
	Addr     string
	Mux      *yamux.Session
	Protocol string

	// ForwardAddr and ForwardName are set only by a distributed Store (RedisStore)
	// for a REMOTE delivery: the service is a delivery hosted by a spoke connected to
	// ANOTHER replica, whose process holds the *yamux.Session. ForwardAddr is that
	// owner replica's inter-replica base URL (e.g. "ws://podIP:5000"); the relay must
	// dial ForwardAddr + "/.cornus/v1/hub/forward" and hand off the stream under ForwardName.
	// Both are empty for the in-memory Registry (single-replica) path.
	ForwardAddr string
	ForwardName string
}

// Store is the hub's service registry, abstracted so the in-memory Registry can be
// swapped for a distributed backend (the seam for a multi-replica hub). Note a
// delivery target carries a live *yamux.Session to the hosting spoke, which is
// process-local: a multi-replica hub must route a relay to the process holding that
// connection. The distributed Stores (RedisStore here, kubehub.KubeStore) do this by
// returning a ForwardAddr disposition that server.hubRelay forwards to the owning
// replica's /.cornus/v1/hub/forward — see ARCHITECTURE.md ("Multi-replica hub").
type Store interface {
	Register(connID, name, addr, protocol string)
	RegisterDeliver(connID, name string, mux *yamux.Session)
	Lookup(name string) (Target, bool)
	RemoveConn(connID string)
	Catalog() []string
}

// Registry maps a service name to the targets spokes have registered for it,
// scoped by the registering connection so a spoke's entries vanish when it
// disconnects. Multiple spokes may register one name (replicas); Lookup rotates
// across them. It is the default in-memory Store.
type Registry struct {
	mu   sync.Mutex
	svc  map[string][]entry
	next map[string]int
}

type entry struct {
	connID string
	tgt    Target
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{svc: map[string][]entry{}, next: map[string]int{}}
}

// Register adds a dial-direct target (the hub dials addr) for name under connID.
// protocol is "tcp" (default, empty) or "udp".
func (r *Registry) Register(connID, name, addr, protocol string) {
	r.add(connID, name, Target{Addr: addr, Protocol: protocol})
}

// RegisterDeliver adds a delivery target for name under connID: the hub reaches
// the service by opening an ingress stream over mux to the hosting spoke.
func (r *Registry) RegisterDeliver(connID, name string, mux *yamux.Session) {
	r.add(connID, name, Target{Mux: mux})
}

func (r *Registry) add(connID, name string, tgt Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.svc[name] = append(r.svc[name], entry{connID: connID, tgt: tgt})
}

// Lookup returns one registered target for name, rotating round-robin across the
// spokes (replicas) that registered it. ok is false when no spoke hosts name.
func (r *Registry) Lookup(name string) (Target, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	es := r.svc[name]
	if len(es) == 0 {
		return Target{}, false
	}
	i := r.next[name] % len(es)
	r.next[name] = i + 1
	return es[i].tgt, true
}

// Catalog returns the sorted set of service names currently registered (at least
// one live provider). It is the overlay's live directory.
func (r *Registry) Catalog() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.svc))
	for name := range r.svc {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RemoveConn drops every entry a connection registered (called when the spoke's
// hub connection drops).
func (r *Registry) RemoveConn(connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, es := range r.svc {
		var kept []entry
		for _, e := range es {
			if e.connID != connID {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(r.svc, name)
			delete(r.next, name)
		} else {
			r.svc[name] = kept
		}
	}
}
