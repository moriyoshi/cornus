// Package listenerpass replicates a live listening socket into another process.
//
// The receiving process gets an independent reference to the SAME kernel socket,
// not a copy of its configuration and not a second bind. Both references accept
// from one backlog, and the address stays bound until the last of them is closed.
// That property is the whole reason this package exists: it lets the ownership of
// a bound address move between processes without the address ever going down, so
// a client dialing it across the handover is never refused.
//
// # Why this cannot be a byte-encoding function
//
// The two platform mechanisms are not shaped alike, and no API that only produces
// bytes can cover both:
//
//   - On unix a descriptor travels as ancillary data (SCM_RIGHTS) on a unix-domain
//     socket. It is not expressible as bytes at all: the kernel installs a
//     descriptor in the receiver as a side effect of the message.
//   - On Windows a socket travels as a WSAPROTOCOL_INFOW blob produced by
//     WSADuplicateSocket, which IS ordinary bytes — but producing it requires
//     naming the receiving process by pid up front.
//
// So this package owns the transfer rather than the encoding, takes a connection,
// and takes a Peer that carries the pid the Windows path needs and the unix path
// ignores.
//
// # The connection must be a unix-domain socket, and unbuffered at this point
//
// Send and Receive require a *net.UnixConn on every platform (Windows has
// supported AF_UNIX since Windows 10), so callers have one transport to reason
// about.
//
// The caller MUST NOT have a buffering reader positioned on that connection when
// Receive runs. This is a real hazard, not a formality: on unix the descriptor is
// attached to specific bytes, so a bufio.Reader or json.Decoder that already read
// ahead past those bytes consumes the message and the descriptor is dropped on the
// floor — silently, and only when replies happen to arrive back-to-back. Either
// perform the transfer before any such reader is created, or give it a connection
// of its own.
//
// # Lifetime
//
// The sender keeps its own listener; Send neither closes nor consumes it. The
// listener returned by Receive is independent — closing one does not disturb the
// other, and the address survives until every reference is gone.
package listenerpass

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

// ErrUnsupported reports that listener replication is unavailable on this
// platform. Callers are expected to degrade rather than fail: without
// replication, a bound address simply dies with the process that owns it.
var ErrUnsupported = errors.New("listenerpass: listener replication is not supported on this platform")

// Peer identifies the process that will receive the replica.
//
// Pid is REQUIRED on Windows, where WSADuplicateSocket must name the target
// process before it can produce a usable WSAPROTOCOL_INFOW, and ignored on unix,
// where SCM_RIGHTS needs nothing. Callers therefore have to obtain the peer's pid
// on every platform even though only one uses it — an asymmetry that belongs here,
// in one documented place, rather than in each caller's protocol.
type Peer struct {
	Pid int
}

// maxPayload bounds what Receive will allocate from a length header. The only
// real payload is a fixed-size WSAPROTOCOL_INFOW (a few hundred bytes), so this is
// a sanity bound against a corrupt or hostile header, not a tunable.
const maxPayload = 64 << 10

// Supported reports whether Send and Receive work in this build. It is a function
// rather than a constant so callers can branch at runtime and log a reason.
func Supported() bool { return supported() }

// Send replicates ln into the process on the other end of conn.
//
// ln must be a listener the runtime owns (as returned by net.Listen); conn must be
// a *net.UnixConn. Send does not close or consume ln — the caller keeps serving on
// it, and typically keeps serving after the peer has a replica.
func Send(conn net.Conn, ln net.Listener, peer Peer) error {
	uc, err := unixConn(conn)
	if err != nil {
		return err
	}
	if ln == nil {
		return errors.New("listenerpass: nil listener")
	}
	return send(uc, ln, peer)
}

// Receive accepts a replica sent by Send and returns it as a listener.
//
// The returned listener is ready to Accept, but a caller need not: holding it
// without accepting still keeps the address bound and the backlog alive, which is
// exactly what a process waiting to take over ownership wants.
func Receive(conn net.Conn) (net.Listener, error) {
	uc, err := unixConn(conn)
	if err != nil {
		return nil, err
	}
	return receive(uc)
}

// Verify reports whether ln is still a usable LISTENING socket.
//
// It exists because a replica can outlive its usefulness in ways that are
// invisible from Go: the descriptor may have been closed, or may no longer be a
// listening socket at all. A caller that adopts a broken replica advertises a
// service and then serves nothing — and that failure is silent in the worst
// possible way, because a bound-but-unserved socket does not REFUSE connections,
// it accepts them into the backlog and leaves them there. Clients hang instead of
// failing over.
//
// It asks the kernel two questions — is this socket listening (SO_ACCEPTCONN),
// and does it have a pending error (SO_ERROR) — without touching the backlog, so
// it is safe to call on a live listener with connections queued. It cannot detect
// a socket that is listening but whose owner will never call Accept; that is the
// caller's contract, not a property of the descriptor.
//
// A nil error means the descriptor is sound. It is not a promise that anything is
// accepting from it.
func Verify(ln net.Listener) error {
	if ln == nil {
		return errors.New("listenerpass: nil listener")
	}
	return verify(ln)
}

func unixConn(conn net.Conn) (*net.UnixConn, error) {
	if conn == nil {
		return nil, errors.New("listenerpass: nil connection")
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		// Naming the actual type matters: the usual way to get here is wrapping the
		// connection in something (a TLS layer, a metering wrapper) that is invisible
		// at the call site.
		return nil, fmt.Errorf("listenerpass: need a *net.UnixConn, got %T (the descriptor travels as ancillary data, which only a unix-domain socket carries)", conn)
	}
	return uc, nil
}

// The wire form is one 4-byte big-endian length followed by that many payload
// bytes. Both platforms send the header; only Windows sends a payload, because on
// unix the descriptor rides in the header write's ancillary data and there is
// nothing to put in the body.
//
// A header is sent even in the unix case because ancillary data must accompany at
// least one ordinary byte to be delivered reliably, and because having one framing
// on both platforms means Receive's structure does not fork.
const headerLen = 4

func encodeHeader(n int) []byte {
	var b [headerLen]byte
	binary.BigEndian.PutUint32(b[:], uint32(n))
	return b[:]
}

func decodeHeader(b []byte) (int, error) {
	if len(b) < headerLen {
		return 0, fmt.Errorf("listenerpass: short header (%d bytes)", len(b))
	}
	n := int(binary.BigEndian.Uint32(b[:headerLen]))
	if n > maxPayload {
		return 0, fmt.Errorf("listenerpass: payload header claims %d bytes, over the %d-byte limit", n, maxPayload)
	}
	return n, nil
}
