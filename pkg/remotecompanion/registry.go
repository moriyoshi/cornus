// Package remotecompanion holds the per-instance companion-connection
// registry types shared between pkg/server (which accepts caretaker
// connections and `cornus exec --forward-agent` channels) and the
// dockerhost/containerdhost deploy backends (which need to look a caretaker
// connection up by app instance to reroute ForwardPort in remote mode). It is
// a neutral package deliberately kept free of any pkg/server or pkg/deploy
// import, so backends can depend on it without a cycle (pkg/server already
// imports pkg/deploy/*).
package remotecompanion

import (
	"fmt"
	"sync"

	"github.com/hashicorp/yamux"
)

// AgentScratchDir is the fixed directory a remote companion binds — as its
// OWN dedicated propagated volume/host-dir, independent of any --mount
// volumes — so AgentSocketPath is visible inside the app container even for
// an instance with no client-local mounts at all. Deliberately a distinct
// path from the per-mount scratch dirs (mounts.go's "/cornus/mounts/<i>"),
// so the two propagated binds never nest or collide.
const AgentScratchDir = "/cornus/agent"

// AgentSocketPath is the fixed, well-known unix socket path a remote
// companion's AgentRelayRole listens on (inside AgentScratchDir) and that
// `cornus exec --forward-agent` injects as SSH_AUTH_SOCK for the exec'd
// process — the two agree on this path with no runtime handshake needed.
const AgentSocketPath = AgentScratchDir + "/agent.sock"

// InstanceKey builds the canonical app instance identity ("name/replica") a
// dockerhost/containerdhost remote companion or a kubernetes pod caretaker
// declares as its Config.Instance, and that the server looks up by when
// rerouting ForwardPort or relaying an agent-forwarding connection.
func InstanceKey(name string, replica int) string {
	return fmt.Sprintf("%s/%d", name, replica)
}

// Registry maps an app instance identity to a live yamux session. Two
// independent registries exist on the server:
//
//   - one holds each instance's CARETAKER connection, so ForwardPort can open
//     a server-initiated stream toward it (see pkg/caretaker's
//     PortForwardRole);
//   - one holds each instance's currently-active `cornus exec
//     --forward-agent` CLIENT channel, so the server can relay a caretaker's
//     AgentRelayRole connection to the real local agent.
//
// Both are process-local (no cross-replica forwarding, mirroring the existing
// credential-relay scoping): only one session is tracked per instance at a
// time. A later registration for the same instance replaces the prior one; a
// stale deregistration (from a connection that already lost its slot to a
// newer one) is a no-op rather than clobbering the newer entry.
type Registry struct {
	mu         sync.Mutex
	byInstance map[string]*yamux.Session
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byInstance: map[string]*yamux.Session{}}
}

// Put registers sess under instance, replacing any prior registration. A
// no-op when instance is empty.
func (r *Registry) Put(instance string, sess *yamux.Session) {
	if instance == "" {
		return
	}
	r.mu.Lock()
	r.byInstance[instance] = sess
	r.mu.Unlock()
}

// Remove deregisters instance only if it currently maps to sess.
func (r *Registry) Remove(instance string, sess *yamux.Session) {
	if instance == "" {
		return
	}
	r.mu.Lock()
	if r.byInstance[instance] == sess {
		delete(r.byInstance, instance)
	}
	r.mu.Unlock()
}

// Get returns the currently registered session for instance, or nil.
func (r *Registry) Get(instance string) *yamux.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byInstance[instance]
}
