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
//
// The three MUTATING methods return an error, because on a distributed Store they
// write to a shared backend that can fail — and a silently dropped write is not a
// local inconvenience: a Register that never lands leaves the provider invisible to
// every peer replica, and a RemoveConn that never lands leaves peers routing to a
// spoke that is gone (the record does NOT expire on its own while this replica keeps
// heartbeating). The in-memory Registry cannot fail and always returns nil, so the
// single-replica path is unaffected.
//
// A returned error does NOT mean the mutation was lost for good: the distributed
// Stores keep the local view authoritative and re-drive the failed shared-store write
// from their heartbeat loop until it lands (see RedisStore.reconcile). The error
// says "not visible cluster-wide yet", and StoreHealth (see HealthReporter) reports
// whether that condition persists.
type Store interface {
	Register(connID, name, addr, protocol string) error
	RegisterDeliver(connID, name string, mux *yamux.Session) error
	Lookup(name string) (Target, bool)
	RemoveConn(connID string) error
	Catalog() []string
	PublishPeerKey(replicaID string, publicKeyPEM []byte) error
	PeerKey(replicaID string) ([]byte, bool, error)
}

// HealthReporter is the optional Store capability that reports whether the store's
// shared state is currently trustworthy: nil when this replica's last liveness
// heartbeat landed and no provider write is still outstanding, non-nil describing the
// degradation otherwise. Only the distributed Stores implement it (the in-memory
// Registry has no shared state to lose); callers must type-assert.
//
// It exists because the heartbeat is the one failure whose symptom is delayed and
// misleading — providers vanish from peers a liveness TTL after the write that
// actually failed — so the server exposes it as a live signal (a metric gauge and
// the /readyz body) rather than only a log line at the moment of failure.
type HealthReporter interface {
	StoreHealth() error
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
// protocol is "tcp" (default, empty) or "udp". The error is always nil: an in-memory
// map write cannot fail. It exists only to satisfy Store, whose distributed
// implementations can.
func (r *Registry) Register(connID, name, addr, protocol string) error {
	r.add(connID, name, Target{Addr: addr, Protocol: protocol})
	return nil
}

// RegisterDeliver adds a delivery target for name under connID: the hub reaches
// the service by opening an ingress stream over mux to the hosting spoke. Always
// returns nil (see Register).
func (r *Registry) RegisterDeliver(connID, name string, mux *yamux.Session) error {
	r.add(connID, name, Target{Mux: mux})
	return nil
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
// hub connection drops). Always returns nil (see Register).
func (r *Registry) RemoveConn(connID string) error {
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
	return nil
}

// PublishPeerKey is a no-op for the process-local registry: it can never return
// a target owned by another replica, so no peer-forward dial can occur.
func (r *Registry) PublishPeerKey(string, []byte) error { return nil }

// PeerKey always misses for the process-local registry (see PublishPeerKey).
func (r *Registry) PeerKey(string) ([]byte, bool, error) { return nil, false, nil }
