package server

import (
	"context"
	"net"
	"net/http"

	"cornus/pkg/logging"
	"cornus/pkg/supervisor"
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
	//
	// The withdrawal is not blanket best-effort. On a distributed store a delete
	// that never lands leaves peers routing to a spoke that is gone: this replica
	// keeps heartbeating, so nothing else retires the record. The store queues the
	// failed delete and re-drives it from its heartbeat, so the right handling here
	// is to say so rather than to retry inline — this runs on connection teardown
	// (often server shutdown), where blocking on a struggling store would delay
	// every other teardown behind it. WARN, not ERROR: traffic to the stale record
	// fails fast at the owning replica (its mux is gone) rather than being
	// mis-served, and the condition is self-healing.
	defer func() {
		if err := s.hub.RemoveConn(hc.id); err != nil {
			logging.FromContext(r.Context()).WarnContext(r.Context(),
				"hub: spoke disconnected but its services are still advertised in the shared registry; peers may route to it until the withdrawal is retried",
				"error", err)
		}
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

	// Each accepted sub-stream runs as a panic-isolated child of this connection's
	// supervisor rather than a bare goroutine: a panic in any stream handler
	// (hubControl reading registrations, hubRelay splicing a relay, a mount/
	// credential/egress/agent relay) is recovered and logged instead of crashing
	// the whole server process. The policy is RemoveOnExit, not Restart, because
	// every server-bound stream is consumed exactly once and is gone after its
	// handler returns — there is nothing to restart (this is the server-side mirror
	// of the caretaker's per-connection roles, which DO Restart because they
	// re-dial their end). Each child self-removes on exit; nothing waits on the
	// tree here, so connection teardown timing is byte-identical to the previous
	// bare-goroutine dispatch — supervision adds isolation, not new ordering. The
	// tree is rooted at r.Context(), the connection's context, so it drops with the
	// connection.
	connSup := supervisor.New(r.Context(), nil)
	for {
		tag, stream, err := wire.AcceptTagged(mux)
		if err != nil {
			return
		}
		switch tag {
		case wire.TagControl:
			connSup.AddSystem("hub-control", supervisor.ServiceFunc(func(context.Context) error {
				// r.Context() is passed for log/trace correlation only; the stream's
				// lifetime is the mux's, exactly like the relays below.
				s.hubControl(r.Context(), hc, mux, stream)
				return nil
			}), supervisor.RemoveOnExit)
		case wire.TagMount:
			// r.Context() is passed for span parenting only (each mount stream's
			// cornus.mount.relay span links to this connection's otelhttp span);
			// stream lifetime is governed by the mux, not the context.
			connSup.AddSystem("mount", supervisor.ServiceFunc(func(context.Context) error {
				s.relayMountMuxed(r.Context(), stream)
				return nil
			}), supervisor.RemoveOnExit)
		case wire.TagCredential:
			// r.Context() carries the connection's trace context (and bounds an
			// inter-replica forward dial); the stream's lifetime is the mux's.
			connSup.AddSystem("credential", supervisor.ServiceFunc(func(context.Context) error {
				s.relayCredentialMuxed(r.Context(), stream)
				return nil
			}), supervisor.RemoveOnExit)
		case wire.TagEgress:
			connSup.AddSystem("egress", supervisor.ServiceFunc(func(context.Context) error {
				s.relayEgressMuxed(r.Context(), stream)
				return nil
			}), supervisor.RemoveOnExit)
		case wire.TagAgentRelay:
			connSup.AddSystem("agent-relay", supervisor.ServiceFunc(func(context.Context) error {
				s.relayAgentMuxed(instance, stream)
				return nil
			}), supervisor.RemoveOnExit)
		case wire.TagTelemetry:
			connSup.AddSystem("telemetry", supervisor.ServiceFunc(func(context.Context) error {
				s.acceptTelemetryMuxed(r.Context(), stream)
				return nil
			}), supervisor.RemoveOnExit)
		case wire.TagData:
			connSup.AddSystem("hub-relay", supervisor.ServiceFunc(func(context.Context) error {
				s.hubRelay(hc, stream)
				return nil
			}), supervisor.RemoveOnExit)
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
// The local registry is checked FIRST — a session held by this process is bridged
// with zero store traffic, byte-identical to the single-replica behavior. Only on
// a miss (and only with a distributed hub store) is the session's owner looked up
// and the stream forwarded to that replica (relayCredentialRemote); the owner then
// enforces AllowsCredential against the session it holds, so forwarding grants no
// authority a local stream would not have.
//
// ctx is the caretaker connection's request context: it carries the trace context
// across the forward hop (dialForward injects a traceparent) and bounds that dial.
// It never governs the spliced stream's lifetime, which belongs to the mux.
func (s *Server) relayCredentialMuxed(ctx context.Context, stream net.Conn) {
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
		if err := s.relayCredentialRemote(ctx, stream, session, name); err != nil {
			s.logCredentialReset(ctx, session, name, err.Error())
		}
		return
	}
	if !sess.AllowsCredential(name) {
		s.logCredentialReset(ctx, session, name, "credential name not declared by this deploy-attach session")
		return
	}
	backing, err := wire.OpenCredBacking(sess.Mux(), name)
	if err != nil {
		s.logCredentialReset(ctx, session, name, "opening the client credential backing failed: "+err.Error())
		return
	}
	defer backing.Close()
	wire.Pipe(stream, backing)
}
