package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// needsEgressRelay reports whether an egress spec requires a live relay session (a
// caretaker forward proxy or transparent redirect tunneling to the client). "env"
// mode injects proxy vars at deploy time and needs no session.
func needsEgressRelay(e *api.EgressSpec) bool { return e.NeedsRelay() }

// applyDetachedEgress realizes a stateless (--detach) egress deploy: it injects the
// egress caretaker with a SESSIONLESS AttachEgress (no client session — the terminus
// is the gateway), so the workload's gateway-routed traffic egresses from this
// server. Any "client"-routed traffic will find no session and be dropped, so a
// detached deploy should route only to gateway/cluster/deny (the client enforces
// this in checkDetachable).
func (s *Server) applyDetachedEgress(ctx context.Context, backend deploy.Backend, spec api.DeploySpec) (api.DeployStatus, error) {
	adv := os.Getenv("CORNUS_ADVERTISE_URL")
	if adv == "" {
		return api.DeployStatus{}, fmt.Errorf("detached client-side egress requires CORNUS_ADVERTISE_URL (the cornus URL the caretaker dials for the gateway relay)")
	}
	egress := &deploy.AttachEgress{
		Session:    "", // no client session — the gateway (this server) is the terminus
		RelayURL:   adv,
		AgentImage: os.Getenv("CORNUS_AGENT_IMAGE"),
		Spec:       spec.Egress,
	}
	if ab, ok := backend.(deploy.AttachingBackend); ok {
		return ab.ApplyWithAttachments(ctx, spec, nil, nil, egress)
	}
	if eb, ok := backend.(deploy.EgressBackend); ok {
		return eb.ApplyWithEgress(ctx, spec, egress)
	}
	return api.DeployStatus{}, fmt.Errorf("client-side egress is not supported by the %s backend", backend.Name())
}

// envTruthy reports whether an env value is an affirmative flag.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseEgressGatewayPolicy compiles the operator gateway ceiling policy from
// CORNUS_EGRESS_POLICY (a JSON api.EgressSpec: rules and/or a PAC script). Empty
// yields nil (no ceiling). A malformed value is a hard error so the server fails
// closed rather than silently egressing without the intended guard.
func parseEgressGatewayPolicy(raw string) (egresspolicy.Policy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var spec api.EgressSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("CORNUS_EGRESS_POLICY: invalid JSON EgressSpec: %w", err)
	}
	pol, err := egresspolicy.Compile(spec)
	if err != nil {
		return nil, fmt.Errorf("CORNUS_EGRESS_POLICY: %w", err)
	}
	return pol, nil
}

// relayEgressMuxed bridges a caretaker egress stream on the unified connection. The
// stream carries its deploy-attach session, the caretaker's route ("client" or
// "gateway"), then the destination ("host:port"). Two terminus models:
//
//   - CLIENT terminus (inner loop): the session id is the unguessable capability. The
//     server looks it up and RE-EVALUATES the deployment's own routing policy (defense
//     in depth — a compromised pod cannot pick its own route); a "client" verdict
//     opens an egress backing on the caller's session (dialed from the client-side
//     network), a "gateway" verdict dials from the server (see below), and
//     cluster/deny drop.
//   - GATEWAY terminus (durable / --detach): there is no client session. The server
//     itself is the egress node; it honors the caretaker's "gateway" route and dials
//     the destination directly, gated by the operator opt-in (CORNUS_EGRESS_GATEWAY)
//     and an optional operator ceiling policy (CORNUS_EGRESS_POLICY). A "client"
//     route with no session is dropped (client needs a session).
func (s *Server) relayEgressMuxed(ctx context.Context, stream net.Conn) {
	defer stream.Close()
	if err := s.relayEgress(ctx, stream); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "egress relay failed", "error", err)
	}
}

// relayEgress performs one caretaker egress relay and returns a context-enriched
// error for the boundary to log once. A clean caretaker hangup before the framing is
// read returns nil.
func (s *Server) relayEgress(ctx context.Context, stream net.Conn) error {
	session, err := wire.ReadLine(stream)
	if err != nil {
		return nil // caretaker hung up before framing — not a fault
	}
	route, err := wire.ReadLine(stream)
	if err != nil {
		return nil
	}
	dest, err := wire.ReadLine(stream)
	if err != nil {
		return nil
	}
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.String("dest", dest)))
	if sess := s.mounts.get(session); sess != nil {
		return s.relayEgressSession(ctx, stream, sess, dest)
	}
	// No session: a durable / --detach deploy whose terminus is the gateway (this
	// server). Honor ONLY the gateway route; a client route needs a live session.
	if route == egresspolicy.RouteGateway {
		return s.pipeGatewayEgress(ctx, stream, dest)
	}
	return fmt.Errorf("egress -> %s: no deploy-attach session for a %q-routed stream", dest, route)
}

// relayEgressSession bridges an egress stream for a session-holding (inner-loop)
// deploy, re-evaluating the session's own policy as the authority.
func (s *Server) relayEgressSession(ctx context.Context, stream net.Conn, sess *deploywire.ServerSession, dest string) error {
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		return fmt.Errorf("egress: parse destination %q: %w", dest, err)
	}
	port, _ := strconv.Atoi(portStr)
	route, err := sess.EgressRoute(host, port)
	if err != nil {
		return fmt.Errorf("egress -> %s: policy re-check failed: %w", dest, err)
	}
	switch route {
	case egresspolicy.RouteClient:
		backing, err := wire.OpenEgressBacking(sess.Mux(), dest)
		if err != nil {
			return fmt.Errorf("egress -> %s: open client backing: %w", dest, err)
		}
		defer backing.Close()
		logging.FromContext(ctx).DebugContext(ctx, "bridging to client terminus")
		wire.Pipe(stream, backing)
		return nil
	case egresspolicy.RouteGateway:
		return s.pipeGatewayEgress(ctx, stream, dest)
	default:
		// cluster / deny / unknown: not relayable — fail closed. A healthy caretaker
		// never relays these, so reaching here signals a policy divergence.
		return fmt.Errorf("egress -> %s: route %q is not relayable (server re-check dropped it)", dest, route)
	}
}

// pipeGatewayEgress dials dest FROM the server (the gateway / egress node) and
// splices, gated by the operator opt-in and an optional ceiling policy — so a pod's
// gateway request can never exceed what the operator permits.
func (s *Server) pipeGatewayEgress(ctx context.Context, stream net.Conn, dest string) error {
	if !s.egressGateway {
		return fmt.Errorf("egress -> %s: gateway egress is disabled on this server (set CORNUS_EGRESS_GATEWAY=1)", dest)
	}
	if s.egressPolicy != nil {
		host, portStr, _ := net.SplitHostPort(dest)
		d := egresspolicy.Dest{Host: host, Proto: "tcp"}
		if ip := net.ParseIP(host); ip != nil {
			d.IP = ip
		}
		d.Port, _ = strconv.Atoi(portStr)
		if r, err := s.egressPolicy.Route(d); err != nil || r == egresspolicy.RouteDeny {
			return fmt.Errorf("egress -> %s: denied by the operator gateway policy", dest)
		}
	}
	var dl net.Dialer
	up, err := dl.DialContext(context.Background(), "tcp", dest)
	if err != nil {
		return fmt.Errorf("egress -> %s: gateway dial: %w", dest, err)
	}
	defer up.Close()
	logging.FromContext(ctx).DebugContext(ctx, "dialing from the gateway (server)")
	wire.Pipe(stream, up)
	return nil
}
