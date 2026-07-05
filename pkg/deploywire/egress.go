package deploywire

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"cornus/pkg/api"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// The egress backing rides one stream per outbound connection (like the credential
// backing rides one per fetch). The server relays a pod's egress connection to the
// caller as an 'e' backing carrying the destination line; the caller (which sits at
// the client-side egress vantage point) dials that destination through its own
// network and splices, so the pod's traffic physically leaves from the client.

// EgressDialer dials an egress destination from the caller's host. It is a seam so
// a proxy-aware dialer (honoring the caller's own HTTP/SOCKS proxy) can be layered
// over the default direct net.Dial. network is always "tcp".
type EgressDialer func(ctx context.Context, network, addr string) (net.Conn, error)

// serveEgress handles one egress backing on the caller side: it re-checks the
// routing policy (defense in depth — only a destination the policy routes to the
// client is dialed here; anything else is dropped), dials the destination, and
// splices. Errors simply close the stream (the pod sees a reset), matching the
// enforcing proxy's drop-on-deny behavior.
func serveEgress(ctx context.Context, dest string, conn net.Conn, policy egresspolicy.Policy, dial EgressDialer) error {
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		return fmt.Errorf("parse destination %q: %w", dest, err)
	}
	d := egresspolicy.Dest{Host: host, Proto: "tcp"}
	if ip := net.ParseIP(host); ip != nil {
		d.IP = ip
	}
	d.Port = atoiSafe(portStr)
	// The client is the last line of defense: honor the policy even though the
	// server already routed this here. Only a "client" verdict is dialed locally.
	if policy != nil {
		route, err := policy.Route(d)
		if err != nil {
			return fmt.Errorf("client policy for %s: %w", dest, err)
		}
		if route != egresspolicy.RouteClient {
			return fmt.Errorf("client guard: %s routes to %q, not the client — dropping", dest, route)
		}
	}
	up, err := dial(ctx, "tcp", dest)
	if err != nil {
		return fmt.Errorf("dial %s from the client: %w", dest, err)
	}
	defer up.Close()
	logging.FromContext(ctx).DebugContext(ctx, "client dialing destination through its own network", slog.Group("egress", "dest", dest))
	wire.Pipe(conn, up)
	return nil
}

// egressHandler builds the ServeBackings egress callback for a session: it dials
// each relayed destination through dial, gated by policy. A nil policy means the
// caller applies no local guard (the server's decision stands); dial defaults to a
// direct net.Dial when nil.
func egressHandler(ctx context.Context, policy egresspolicy.Policy, dial EgressDialer) func(dest string, conn net.Conn) {
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	log := logging.FromContext(ctx)
	return func(dest string, conn net.Conn) {
		if err := serveEgress(ctx, dest, conn, policy, dial); err != nil {
			log.WarnContext(ctx, "client-side relay failed", slog.Group("egress", "dest", dest), "error", err)
		}
	}
}

// egressHandlerFor returns the ServeBackings egress callback for a deployment's
// egress spec, or nil when no egress backing should be served (no spec, or "env"
// mode, which needs no relay). The client compiles the routing policy as a local
// guard; if it cannot compile (e.g. a script with no evaluator linked), the server's
// decision stands (nil policy) rather than dropping all egress.
func egressHandlerFor(ctx context.Context, spec *api.EgressSpec, dial EgressDialer) func(dest string, conn net.Conn) {
	if spec == nil || spec.Mode == "" || spec.Mode == "env" {
		return nil
	}
	policy, err := egresspolicy.Compile(*spec)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "client policy guard disabled (server decision stands)", "component", "egress", "error", err)
		policy = nil
	}
	return egressHandler(ctx, policy, dial)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
