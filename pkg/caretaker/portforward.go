package caretaker

import (
	"context"
	"net"

	"github.com/hashicorp/yamux"

	"cornus/pkg/wire"
)

// PortForwardRole lets the cornus server reach a port inside this instance
// through the caretaker. On dockerhost/containerdhost/bare the companion
// shares the instance's network namespace, so any port is reachable via
// loopback and there is nothing per-port to configure ahead of time — the
// server picks the port per request.
//
// Host overrides the dial host for backends whose companion CANNOT share the
// app instance's network namespace and instead runs beside it on the same
// network — the incus backend, where a companion is a sibling Incus instance
// on the same bridge (Incus exposes no way to join another instance's netns).
// Empty means loopback, i.e. the netns-sharing case, which is still the norm.
//
// Unlike every other caretaker role, this is the one direction where the
// SERVER opens a stream TOWARD the caretaker instead of the caretaker
// initiating one: an external cornus port-forward/tunnel connection arrives
// at the server, which looks up this instance's companion session in its
// registry and opens a TagPortForward stream on it (see wire.OpenPortForward
// and pkg/server's ForwardPort rerouting). runPortForwardAccept is therefore
// the caretaker's only accept loop for a server-initiated stream.
type PortForwardRole struct {
	Server string `json:"server"`
	Host   string `json:"host,omitempty"`
}

// dialHost is the host the role's port-forward streams dial, defaulting to
// loopback (the netns-sharing companion).
func (r PortForwardRole) dialHost() string {
	if r.Host == "" {
		return "127.0.0.1"
	}
	return r.Host
}

// runPortForwardAccept serves server-initiated TagPortForward streams on the
// pod-scoped session, dialing the requested port on host for each and splicing
// with wire.Pipe (tcp) or wire.BridgeDatagramStream (udp) — the default
// loopback dial only works because the companion shares the app instance's
// netns; see PortForwardRole.Host for the sibling-instance case.
//
// It owns a dispatcher of its own, which is right only when this is the session's
// ONLY tagged role. runCaretakerConn does not use it: a caretaker carrying several
// roles registers them all on one dispatcher, because a second accept loop on the
// same session would steal streams from the first (see dispatch.go).
func runPortForwardAccept(ctx context.Context, sess *yamux.Session, host string) error {
	d := newTagDispatch()
	registerPortForward(d, host)
	return d.run(ctx, sess)
}

// registerPortForward wires the TagPortForward handler onto d.
func registerPortForward(d *tagDispatch, host string) {
	d.handle(wire.TagPortForward, func(stream net.Conn) { servePortForwardStream(stream, host) })
}

// servePortForwardStream reads the "port\nproto\n" header a server-opened
// TagPortForward stream carries (see wire.OpenPortForward), dials that port on
// host, and splices.
func servePortForwardStream(stream net.Conn, host string) {
	defer stream.Close()
	port, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	proto, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)
	if proto == "udp" {
		conn, err := net.Dial("udp", addr)
		if err != nil {
			return
		}
		defer conn.Close()
		wire.BridgeDatagramStream(stream, conn)
		return
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer conn.Close()
	wire.Pipe(stream, conn)
}
