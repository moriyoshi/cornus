// Package memlisten provides an addressless, in-process net.Listener: a caller
// dials it directly rather than through the kernel, so the served endpoint has no
// TCP port and no socket file.
//
// It exists so a listener and its dialer can be co-located in one process without
// binding anything. The cornus client agent uses it to host the web UI's
// backend-for-frontend and hand its SOCKS5 proxy a name that resolves straight to
// the handler: with no address, there is no port for the kernel to recycle to an
// unrelated process, so a published name can never be answered by a squatter that
// inherited its port.
//
// A Listener satisfies net.Listener (so http.Server.Serve takes it) and, via
// DialLocal, the client-local dialer the SOCKS5 router resolves a published name
// to.
package memlisten

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// addr is a Listener's stand-in address. There is no real endpoint, so it only
// carries the name for diagnostics (Server.Addr strings, log lines).
type addr struct{ name string }

func (a addr) Network() string { return "mem" }
func (a addr) String() string  { return a.name }

// Listener is an in-process listener. Accept yields the connections DialLocal
// creates; both halves are net.Pipe ends, so a write blocks until the peer reads
// (no buffering, and no kernel involvement).
//
// The zero value is not usable; call New. A Listener is safe for concurrent use.
type Listener struct {
	name string
	// conns carries each dialed connection to Accept. Unbuffered: a dial parks
	// until an Accept takes it (or the dial's ctx ends), which is the same
	// backpressure a real listener's accept queue would eventually apply.
	conns chan net.Conn

	closeOnce sync.Once
	// done is closed by Close, unblocking a parked Accept and every parked or
	// subsequent DialLocal.
	done chan struct{}
}

// New returns a Listener whose Addr reports name (for diagnostics only).
func New(name string) *Listener {
	return &Listener{
		name:  name,
		conns: make(chan net.Conn),
		done:  make(chan struct{}),
	}
}

// Accept returns the next dialed connection, blocking until one arrives. It
// returns net.ErrClosed once the Listener is closed, so http.Server.Serve exits
// cleanly.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, &net.OpError{Op: "accept", Net: "mem", Addr: l.Addr(), Err: net.ErrClosed}
	}
}

// Close stops the Listener: a parked Accept unblocks and later dials fail. It is
// idempotent and always returns nil.
//
// Close does NOT sever connections already accepted; their lifetime belongs to
// whoever is serving them (http.Server.Close / Shutdown for a served handler).
func (l *Listener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return nil
}

// Addr reports the Listener's stand-in address (network "mem"). There is no real
// endpoint behind it.
func (l *Listener) Addr() net.Addr { return addr{name: l.name} }

// DialLocal opens a connection to the Listener and hands its peer to Accept. It
// blocks until the Listener accepts, ctx ends, or the Listener closes — so a dial
// never parks past either lifetime.
func (l *Listener) DialLocal(ctx context.Context) (net.Conn, error) {
	near, far := net.Pipe()
	select {
	case l.conns <- far:
		return near, nil
	case <-ctx.Done():
		// Nobody took far: close both ends so neither leaks a parked goroutine.
		_ = near.Close()
		_ = far.Close()
		return nil, ctx.Err()
	case <-l.done:
		_ = near.Close()
		_ = far.Close()
		return nil, fmt.Errorf("memlisten: dial %s: %w", l.name, net.ErrClosed)
	}
}
