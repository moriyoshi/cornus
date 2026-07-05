package server

import (
	"context"
	"net"
	"net/http"

	"cornus/pkg/wire"
)

// handleCaretakerUnified serves GET /.cornus/v1/caretaker/attach: the pod-scoped,
// always-on caretaker connection carrying every server-bound role on ONE yamux
// mux — mount streams ('M', each carrying its deploy-attach session then name),
// hub control ('C', service registration), and hub egress ('D'). It is the single
// server-bound caretaker endpoint: the connection is decoupled from any deploy-attach
// session (each 'M' stream carries its own session), so one connection serves a pod's
// mounts (from any session) and its hub membership. See ARCHITECTURE.md, the
// caretaker section ("One pod-scoped connection").
func (s *Server) handleCaretakerUnified(w http.ResponseWriter, r *http.Request) {
	mux, err := wire.Accept(w, r)
	if err != nil {
		return
	}
	defer mux.Close()

	// instance, when the caretaker declared one (Config.Instance — always set
	// by a dockerhost/containerdhost remote companion or a kubernetes pod
	// caretaker), registers this connection so ForwardPort and the exec
	// agent-relay can find it by app instance. Empty for older/plain
	// caretakers, which simply don't get PortForward/AgentRelay routing.
	instance := r.URL.Query().Get("instance")
	s.remoteCompanions.Put(instance, mux)
	defer s.remoteCompanions.Remove(instance, mux)

	hc := newHubConn(newSessionID())
	// On disconnect drop the spoke's registrations, then kick the catalog
	// notifier so watching spokes see the vanished services promptly.
	defer func() {
		s.hub.RemoveConn(hc.id)
		s.catalogWatch().changed()
	}()
	// The authenticated identity is authoritative for hub policy and wins over any
	// self-declared identity on the control stream. Prefer the identity the auth
	// middleware established (JWT sub, or the mTLS CommonName when bearer auth is on);
	// fall back to reading the verified client cert directly, which covers mTLS
	// terminated at the TLS layer without the API auth middleware engaged. When
	// neither is present (auth off, no cert) the control-stream declaration is used.
	id := Identity(r)
	if id == "" {
		id = verifiedIdentity(r)
	}
	if id != "" {
		hc.declare(id)
	}

	for {
		tag, stream, err := wire.AcceptTagged(mux)
		if err != nil {
			return
		}
		switch tag {
		case wire.TagControl:
			go s.hubControl(hc, mux, stream)
		case wire.TagMount:
			// r.Context() is passed for span parenting only (each mount stream's
			// cornus.mount.relay span links to this connection's otelhttp span);
			// stream lifetime is governed by the mux, not the context.
			go s.relayMountMuxed(r.Context(), stream)
		case wire.TagCredential:
			go s.relayCredentialMuxed(stream)
		case wire.TagEgress:
			go s.relayEgressMuxed(r.Context(), stream)
		case wire.TagAgentRelay:
			go s.relayAgentMuxed(instance, stream)
		case wire.TagData:
			go s.hubRelay(hc, stream)
		default:
			stream.Close()
		}
	}
}

// relayAgentMuxed bridges one caretaker AgentRelayRole connection (a process
// inside the app instance talking to the forwarded ssh-agent socket) to
// whichever `cornus exec --forward-agent` client channel currently holds the
// real local agent for this instance. If none is registered — no exec
// session with --forward-agent is currently active for this instance — the
// stream is closed immediately, matching real ssh-agent forwarding's failure
// mode when nothing is forwarding.
func (s *Server) relayAgentMuxed(instance string, stream net.Conn) {
	defer stream.Close()
	sess := s.execAgentChannels.Get(instance)
	if sess == nil {
		return
	}
	backing, err := sess.OpenStream()
	if err != nil {
		return
	}
	defer backing.Close()
	wire.Pipe(stream, backing)
}

// relayMountMuxed bridges a mount stream on the unified connection. Unlike the
// session-scoped path (session in the URL), the stream carries its deploy-attach
// session then name (two lines), so one pod-scoped connection can serve mounts from
// any session. The session id remains the capability (unguessable); AllowsMount
// gates the name.
//
// The local registry is checked FIRST — a session held by this process is bridged
// with zero store traffic, byte-identical to the single-replica behavior. Only on
// a miss (and only with a distributed hub store) is the session's owner looked up
// and the stream forwarded to that replica (relayMountRemote); the owner then
// enforces AllowsMount against the session it holds.
//
// When tracing is on, each bridged stream runs under its own cornus.mount.relay
// span (transport=local here; relayMountRemote emits the forwarded one), parented
// to the caretaker connection's attach span carried by ctx. ctx is used for span
// parenting only, never for cancellation.
func (s *Server) relayMountMuxed(ctx context.Context, stream net.Conn) {
	defer stream.Close()
	session, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	name, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	sess := s.mounts.get(session)
	if sess == nil {
		s.relayMountRemote(ctx, stream, session, name)
		return
	}
	conn, finish := s.traceMountRelay(ctx, session, name, "local", stream)
	if !sess.AllowsMount(name) {
		s.logMountReset(ctx, session, name, "mount name not declared by this deploy-attach session")
		finish(errMountNotAllowed)
		return
	}
	// A writable async mount speaks the block protocol on a 'b' backing; the 9P
	// modes ride the 'L' backing.
	writable := s.fileCache != nil && sess.MountWritableCacheable(name)
	open := wire.OpenBacking
	if writable {
		open = wire.OpenBlockBacking
	}
	backing, err := open(sess.Mux(), name)
	if err != nil {
		s.logMountReset(ctx, session, name, "opening the client 9P backing failed: "+err.Error())
		finish(err)
		return
	}
	defer backing.Close()
	kernelConn := s.meterMountConn(name, conn)
	switch {
	case writable:
		// Writable, cache-coherent mount: terminate it in the block proxy.
		wire.ServeBlockProxy(kernelConn, backing, s.fileCache, name, wire.BlockEnvOpts()...)
	case s.fileCache != nil && sess.MountCacheable(name):
		// Immutable read-only mount: terminate 9P here and serve reads from the
		// block cache instead of blindly piping frames to the pod.
		wire.ServeCachingProxy(kernelConn, backing, s.fileCache, name)
	default:
		wire.Pipe(kernelConn, backing)
	}
	finish(nil)
}

// relayCredentialMuxed bridges a credential stream on the unified connection. Like
// the mount relay, the stream carries its deploy-attach session then the
// credential name (two lines); the session id is the unguessable capability and
// AllowsCredential gates the name so a pod can only fetch the credentials its own
// deployment declared. The caller (which holds the source backend) answers the
// credential request/response over the bridged backing.
//
// Only the local registry is consulted: a session held by another replica is not
// yet forwarded (credential multi-replica forwarding is a follow-up); the
// single-replica case — every deployment's caretaker connects through the same
// server that holds its session — always finds it here.
func (s *Server) relayCredentialMuxed(stream net.Conn) {
	defer stream.Close()
	session, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	name, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	sess := s.mounts.get(session)
	if sess == nil {
		return
	}
	if !sess.AllowsCredential(name) {
		return
	}
	backing, err := wire.OpenCredBacking(sess.Mux(), name)
	if err != nil {
		return
	}
	defer backing.Close()
	wire.Pipe(stream, backing)
}
