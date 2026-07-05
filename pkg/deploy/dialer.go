package deploy

import (
	"context"
	"net"
	"sync"
)

// PortForwardDialer adapts a Backend to the dial-a-connection shape that the
// ingress proxies are written against (portfwd.Dialer: PortForward returns a
// net.Conn). Backend.ForwardPort has the opposite shape — it is handed a
// connection to bridge and returns when the bridge ends — so the adapter runs
// the bridge against one end of an in-memory pipe and hands the caller the
// other.
//
// It satisfies portfwd.Dialer structurally, without pkg/deploy importing
// pkg/portfwd. That is what lets the server-side ingress mux reuse the same
// reverse-proxy machinery the client-side conduit uses.
type PortForwardDialer struct {
	Backend Backend
}

// PortForward returns a connection to port inside the named deployment. proto
// is "tcp" (the default when empty) or "udp".
//
// Setup failures inside ForwardPort (no such deployment, no ready instance, RBAC
// denied) surface on the returned connection's first Read rather than from this
// call: the bridge runs asynchronously, so there is nothing to report yet when
// the caller gets its end of the pipe. Reporting it on Read is what lets a
// caller render a useful error instead of a bare EOF — see forwardConn.
func (d PortForwardDialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	if d.Backend == nil {
		return nil, net.ErrClosed
	}
	local, remote := net.Pipe()
	fc := &forwardConn{Conn: local}
	go func() {
		err := d.Backend.ForwardPort(ctx, name, port, proto, remote)
		fc.setErr(err)
		_ = remote.Close() // unblocks the caller's pending read with EOF
	}()
	return fc, nil
}

// forwardConn is the caller's end of a PortForwardDialer pipe. It remembers why
// the bridge ended so a read that would otherwise report a bare EOF reports the
// underlying cause instead.
type forwardConn struct {
	net.Conn

	mu  sync.Mutex
	err error
}

func (c *forwardConn) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *forwardConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil && n == 0 {
		c.mu.Lock()
		bridgeErr := c.err
		c.mu.Unlock()
		if bridgeErr != nil {
			return n, bridgeErr
		}
	}
	return n, err
}
