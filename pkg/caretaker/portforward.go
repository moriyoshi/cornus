package caretaker

import (
	"context"
	"fmt"
	"net"

	"github.com/hashicorp/yamux"

	"cornus/pkg/wire"
)

// PortForwardRole lets the cornus server reach a port inside this instance
// through the caretaker, which shares the instance's network namespace (see
// the dockerhost/containerdhost "remote companion"). It carries no fields of
// its own beyond Server: any port is reachable via loopback once the
// companion's netns is joined, so there is nothing per-port to configure
// ahead of time — the server picks the port per request.
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
}

// runPortForwardAccept accepts server-initiated TagPortForward streams on the
// pod-scoped session and dials the requested local port for each, splicing
// with wire.Pipe (tcp) or wire.BridgeDatagramStream (udp) — the loopback dial
// only works because the companion shares the app instance's netns.
func runPortForwardAccept(ctx context.Context, sess *yamux.Session) error {
	for {
		tag, stream, err := wire.AcceptTagged(sess)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("portforward: accept: %w", err)
		}
		if tag != wire.TagPortForward {
			stream.Close()
			continue
		}
		go servePortForwardStream(stream)
	}
}

// servePortForwardStream reads the "port\nproto\n" header a server-opened
// TagPortForward stream carries (see wire.OpenPortForward), dials that port on
// loopback, and splices.
func servePortForwardStream(stream net.Conn) {
	defer stream.Close()
	port, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	proto, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	addr := "127.0.0.1:" + port
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
