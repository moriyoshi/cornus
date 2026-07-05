package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"

	"cornus/pkg/deploywire"
	"cornus/pkg/hub"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// logMountReset records, in the server's OWN log, that a caretaker mount stream
// was closed without being bridged to a backing. The relay deliberately tells
// the caretaker nothing (capability hygiene — see relayMountMuxed), so without
// this an operator sees the pod-side "connection reset by peer" with no matching
// server-side reason. The most common cause is a stale baked-in session id: the
// workload pod carries the mount session id from deploy time, but this process's
// in-memory registry no longer holds it (the server restarted, or the owning
// deploy-attach connection ended and was replaced under a fresh id). Only the
// session-id digest is logged (never the raw capability), matching traceMountRelay.
func (s *Server) logMountReset(ctx context.Context, session, name, reason string) {
	logging.FromContext(ctx).WarnContext(ctx, "mount relay: reset stream (no live backing for this deploy-attach session)",
		"reason", reason,
		"mount.session", sessionDigest(session),
		"mount.name", name)
}

// mountRegistry maps a deploy-attach session id to its live session, so a pod's
// caretaker (a separate connection, possibly from another node) can be bridged to
// the caller's 9P export. It is in-memory in one server process: the live
// *deploywire.ServerSession is process-local, exactly like a hub delivery's spoke
// mux. Multi-replica reach comes from the same mechanism the hub uses — a
// distributed hub.Store carries a per-session routing record (registerMountSession)
// and a replica that does NOT hold the session forwards the caretaker's stream to
// the owner's /.cornus/v1/mount/forward (relayMountRemote), mirroring /.cornus/v1/hub/forward.
// The bridge itself lives on the unified caretaker connection (caretaker_attach.go).
type mountRegistry struct {
	mu sync.Mutex
	m  map[string]*deploywire.ServerSession
}

func newMountRegistry() *mountRegistry {
	return &mountRegistry{m: map[string]*deploywire.ServerSession{}}
}

func (r *mountRegistry) put(id string, s *deploywire.ServerSession) {
	r.mu.Lock()
	r.m[id] = s
	r.mu.Unlock()
}

func (r *mountRegistry) get(id string) *deploywire.ServerSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

// has reports whether a live session currently holds id. Used to decide whether a
// re-apply may reuse the id already baked into the running pod (mountSessionID):
// only when no live session holds it, so reuse can never clobber a still-connected
// session's registry entry.
func (r *mountRegistry) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[id]
	return ok
}

func (r *mountRegistry) del(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

// newSessionID returns a random hex id for a deploy-attach session (also used as a
// hub connection id).
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- cross-replica mount forwarding -------------------------------------------

// mountServicePrefix namespaces the mount-session routing records this server
// writes into the (distributed) hub store. The prefix keeps them disjoint from
// spoke-registered service names and lets the catalog surfaces filter them out
// (filterMountCatalog) — they are internal routing records, not services.
const mountServicePrefix = "cornus.mount/"

// mountServiceName derives the store key for a deploy-attach session id. The
// session id is an unguessable capability, so only its digest goes into the shared
// store (Redis entries and HubEndpoint CRs are readable more broadly than this
// process); a replica resolving a caretaker's request derives the same digest from
// the session id the caretaker presented.
func mountServiceName(session string) string {
	sum := sha256.Sum256([]byte(session))
	return mountServicePrefix + hex.EncodeToString(sum[:16])
}

// sessionDigest is the log-safe, non-capability identifier for a deploy-attach
// session id: the same digest logMountReset and traceMountRelay use, so a session
// registration / teardown log line can be correlated with the caretaker-facing
// mount-reset WARN for the same session.
func sessionDigest(id string) string {
	return strings.TrimPrefix(mountServiceName(id), mountServicePrefix)
}

// hubDistributed reports whether the hub store is a distributed backend (Redis /
// kube). The in-memory Registry means single-replica: no peer can ever hold a
// session this process does not, so mount records are neither written nor looked
// up — the single-replica path stays byte-identical with zero store traffic.
func (s *Server) hubDistributed() bool {
	_, local := s.hub.(*hub.Registry)
	return !local
}

// registerMountSession advertises, in the distributed hub store, that THIS replica
// holds the deploy-attach session: a delivery-mode record under the session's
// derived name, carrying this replica's forward base URL (the same owner/
// forwardAddr plumbing hub deliveries use, so liveness TTLs / Leases and the
// kube ownerRef crash-GC apply unchanged). The mux is deliberately nil — the
// deploy-attach session's mux speaks the deploywire backing protocol, not the hub
// delivery protocol, so a hub relay resolving this record locally must fail
// closed rather than open a hub ingress stream on it. A peer replica's Lookup
// resolves it to a ForwardAddr disposition, which is all the mount relay needs.
func (s *Server) registerMountSession(id string) {
	if !s.hubDistributed() {
		return
	}
	name := mountServiceName(id)
	s.hub.RegisterDeliver(name, name, nil)
}

// unregisterMountSession removes the session's routing record (CLI disconnect /
// deploy-attach teardown). A replica crash needs no explicit cleanup: the record
// dies with the owner's liveness key (Redis TTL) or Lease/Pod ownerRef (kube).
func (s *Server) unregisterMountSession(id string) {
	if !s.hubDistributed() {
		return
	}
	s.hub.RemoveConn(mountServiceName(id))
}

// relayMountRemote bridges a caretaker mount stream whose deploy-attach session is
// NOT held by this process. With a distributed hub store the owning replica may
// hold it: look the session's routing record up, dial the owner's
// /.cornus/v1/mount/forward (same credential and TLS trust as the hub's inter-replica
// forward), hand it the session and name lines, and splice. On a single replica
// (in-memory store) or when no live owner exists this returns immediately and the
// caretaker sees the stream close — exactly the prior unknown-session behavior.
// Metering happens here, at the caretaker-facing edge (the forward handler on the
// owner does not meter), so each mount stream is counted once cluster-wide. The
// per-stream cornus.mount.relay span (transport=forwarded) follows the same rule:
// it is emitted here only, parented to the caretaker connection's attach span via
// ctx (ctx is for span parenting only, never stream cancellation). The trace
// context IS propagated across the forward hop: dialForward injects a W3C
// traceparent header from ctx, so the owner replica's /.cornus/v1/mount/forward
// otelhttp span links to this relay span.
func (s *Server) relayMountRemote(ctx context.Context, stream net.Conn, session, name string) {
	if !s.hubDistributed() {
		// Single-replica (in-memory hub): no peer can hold a session this process
		// does not, so the session is genuinely gone. This is the common real-world
		// reset — surface it (the trace-span path never runs on a non-distributed hub).
		s.logMountReset(ctx, session, name, "unknown session on single-replica server (stale pod session id after a server restart or a deploy-attach reconnect)")
		return
	}
	conn, finish := s.traceMountRelay(ctx, session, name, "forwarded", stream)
	tgt, ok := s.hub.Lookup(mountServiceName(session))
	if !ok || tgt.ForwardAddr == "" {
		s.logMountReset(ctx, session, name, "no replica currently owns this mount session (routing record missing)")
		finish(errMountNoOwner)
		return
	}
	fwd, err := s.dialForward(ctx, tgt.ForwardAddr, "/.cornus/v1/mount/forward", session, name)
	if err != nil {
		s.logMountReset(ctx, session, name, "forward to owning replica failed: "+err.Error())
		finish(err)
		return
	}
	defer fwd.Close()
	wire.Pipe(s.meterMountConn(name, conn), fwd)
	finish(nil)
}

// handleMountForward serves GET /.cornus/v1/mount/forward: the inter-replica mount hop.
// A peer replica dials here when a caretaker's mount stream landed on it but THIS
// replica holds the deploy-attach session (the caller's live 9P export). It reads
// the session and name lines, resolves the session LOCALLY only, enforces the
// per-session mount allow-list, opens the backing stream to the caller, and
// splices. It never re-forwards — a session not held here just closes the stream,
// so a stale routing record can never cause a forward loop between replicas.
//
// Trust model: identical to /.cornus/v1/hub/forward. The endpoint is under /.cornus/v1/, so the
// auth middleware already requires a FULL credential (the caretaker-scoped token
// is rejected — the path is not /.cornus/v1/caretaker/attach); the session id remains the
// per-mount capability, checked here against the live session it names.
func (s *Server) handleMountForward(w http.ResponseWriter, r *http.Request) {
	conn, err := wire.AcceptConn(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	session, err := wire.ReadLine(conn)
	if err != nil {
		return
	}
	name, err := wire.ReadLine(conn)
	if err != nil {
		return
	}
	sess := s.mounts.get(session)
	if sess == nil || !sess.AllowsMount(name) {
		return // not held here (or name not allowed) — never re-forward (loop guard)
	}
	writable := s.fileCache != nil && sess.MountWritableCacheable(name)
	open := wire.OpenBacking
	if writable {
		open = wire.OpenBlockBacking
	}
	backing, err := open(sess.Mux(), name)
	if err != nil {
		return
	}
	defer backing.Close()
	switch {
	case writable:
		wire.ServeBlockProxy(conn, backing, s.fileCache, name, wire.BlockEnvOpts()...)
	case s.fileCache != nil && sess.MountCacheable(name):
		wire.ServeCachingProxy(conn, backing, s.fileCache, name)
	default:
		wire.Pipe(conn, backing)
	}
}

// filterMountCatalog drops reserved mount-session routing records from a catalog
// snapshot. They must never reach catalog surfaces: watching spokes (the
// caretaker's dynamic hub role) bind listeners for every catalog name, and the
// records are internal routing state, not reachable services.
func filterMountCatalog(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !strings.HasPrefix(n, mountServicePrefix) {
			out = append(out, n)
		}
	}
	return out
}

// mountFilteredStore is the catalog view of the hub store: it delegates everything
// and hides mount-session records from Catalog. The catalog notifier reads through
// it so watch pushes stay clean (see catalogWatch); /.cornus/v1/hub/catalog filters the
// same way.
type mountFilteredStore struct{ hub.Store }

func (m mountFilteredStore) Catalog() []string { return filterMountCatalog(m.Store.Catalog()) }
