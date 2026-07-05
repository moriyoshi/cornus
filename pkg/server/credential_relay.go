package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// Relay outcomes for a caretaker credential stream that could not be bridged.
// Like the mount relay's, they exist for the server's OWN log only: the relay
// deliberately tells the caretaker nothing beyond closing the stream (capability
// hygiene — see relayCredentialMuxed).
var (
	// errCredUnknownSession is the single-replica miss: no peer can hold a session
	// this process does not, so the session is genuinely gone.
	errCredUnknownSession = errors.New("unknown session on single-replica server (stale pod session id after a server restart or a deploy-attach reconnect)")
	// errCredNoOwner is the distributed miss: no live replica advertises the
	// session's routing record (the owner died, or the CLI disconnected).
	errCredNoOwner = errors.New("no replica currently owns this credential session (routing record missing)")
)

// logCredentialReset records, in the server's OWN log, that a caretaker credential
// stream was closed without being bridged to the caller's credential source. The
// relay tells the caretaker nothing, so without this an operator sees the pod-side
// fetch failure with no matching server-side reason. Only the session-id digest is
// logged (never the raw capability), matching logMountReset.
func (s *Server) logCredentialReset(ctx context.Context, session, name, reason string) {
	logging.FromContext(ctx).WarnContext(ctx, "credential relay: reset stream (no live backing for this deploy-attach session)",
		"reason", reason,
		"cred.session", sessionDigest(session),
		"cred.name", name)
}

// --- cross-replica credential forwarding ---------------------------------------

// relayCredentialRemote bridges a caretaker credential stream whose deploy-attach
// session is NOT held by this process. It is the credential twin of
// relayMountRemote and rides the SAME per-session routing record
// (registerMountSession / mountServiceName): one deploy-attach session owns both a
// pod's mounts and its credentials, so a second record would only be a second copy
// of the same fact. With a distributed hub store the owning replica may hold the
// session: look the record up, dial the owner's /.cornus/v1/cred/forward (same
// credential and TLS trust as the hub's inter-replica forward), hand it the session
// and name lines, and splice. On a single replica (in-memory store) or when no live
// owner exists it returns an error and the caretaker sees the stream close —
// exactly the prior unknown-session behavior.
//
// The trace context IS propagated across the forward hop: dialForward injects a
// W3C traceparent header from ctx, so the owner replica's /.cornus/v1/cred/forward
// otelhttp span links to the caretaker connection's span on this side. ctx bounds
// the dial only; it never cancels the spliced stream.
//
// It returns an error rather than logging, so the caretaker-facing boundary
// (relayCredentialMuxed) logs each dropped stream exactly once — the same shape as
// relayEgress.
func (s *Server) relayCredentialRemote(ctx context.Context, stream net.Conn, session, name string) error {
	if !s.hubDistributed() {
		return errCredUnknownSession
	}
	tgt, ok := s.hub.Lookup(mountServiceName(session))
	if !ok || tgt.ForwardAddr == "" {
		return errCredNoOwner
	}
	fwd, err := s.dialForward(ctx, tgt.ForwardAddr, "/.cornus/v1/cred/forward", session, name)
	if err != nil {
		return fmt.Errorf("forward to owning replica failed: %w", err)
	}
	defer fwd.Close()
	wire.Pipe(stream, fwd)
	return nil
}

// handleCredentialForward serves GET /.cornus/v1/cred/forward: the inter-replica
// credential hop. A peer replica dials here when a caretaker's credential stream
// landed on it but THIS replica holds the deploy-attach session (the caller's live
// credential sources). It reads the session and name lines, resolves the session
// LOCALLY only, enforces the per-session credential allow-list, opens the credential
// backing to the caller, and splices. It never re-forwards — a session not held here
// just closes the stream, so a stale routing record can never cause a forward loop
// between replicas.
//
// Authorization lives HERE, at the owner, not at the forwarding replica: the peer
// carries no session state and cannot vouch for the name, so the name is re-checked
// against the live session with AllowsCredential exactly as the local path does
// (relayCredentialMuxed). A forged or replayed forward request therefore gains
// nothing — it must still name a session this process holds (the session id is the
// unguessable capability) AND a credential that session declared.
//
// Trust model: identical to /.cornus/v1/mount/forward. The endpoint is under
// the exact peer-forward allowlist, so the auth middleware accepts a peer-scoped
// credential here (a full credential remains a superset). The caretaker-scoped
// token is rejected.
func (s *Server) handleCredentialForward(w http.ResponseWriter, r *http.Request) {
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
	if sess == nil || !sess.AllowsCredential(name) {
		return // not held here (or name not allowed) — never re-forward (loop guard)
	}
	backing, err := wire.OpenCredBacking(sess.Mux(), name)
	if err != nil {
		return
	}
	defer backing.Close()
	wire.Pipe(conn, backing)
}
