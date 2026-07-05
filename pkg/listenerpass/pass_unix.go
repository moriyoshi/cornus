//go:build unix

package listenerpass

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
)

func supported() bool { return true }

// send passes ln's descriptor as SCM_RIGHTS ancillary data. The kernel installs a
// new descriptor in the receiving process pointing at the same open file
// description, which is what makes the two references share one backlog.
func send(uc *net.UnixConn, ln net.Listener, _ Peer) error {
	sc, ok := ln.(syscall.Conn)
	if !ok {
		return fmt.Errorf("listenerpass: listener of type %T exposes no syscall.Conn", ln)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return fmt.Errorf("listenerpass: SyscallConn: %w", err)
	}
	// The sendmsg runs INSIDE Control so the descriptor cannot be closed by another
	// goroutine between being observed and being sent — a use-after-close here would
	// pass whatever unrelated file had taken the number.
	//
	// Control is also why this does not use (*net.TCPListener).File(): File dups the
	// descriptor AND puts the returned one in blocking mode, which drags the socket
	// out of the runtime poller for no benefit when all we need is its number.
	var sendErr error
	if err := raw.Control(func(fd uintptr) {
		rights := syscall.UnixRights(int(fd))
		_, _, sendErr = uc.WriteMsgUnix(encodeHeader(0), rights, nil)
	}); err != nil {
		return fmt.Errorf("listenerpass: Control: %w", err)
	}
	if sendErr != nil {
		return fmt.Errorf("listenerpass: sending the descriptor: %w", sendErr)
	}
	return nil
}

// receive reads one descriptor out of the ancillary data.
func receive(uc *net.UnixConn) (net.Listener, error) {
	header := make([]byte, headerLen)
	oob := make([]byte, syscall.CmsgSpace(4)) // exactly one descriptor
	n, oobn, _, _, err := uc.ReadMsgUnix(header, oob)
	if err != nil {
		return nil, fmt.Errorf("listenerpass: receiving the descriptor: %w", err)
	}
	if n < headerLen {
		return nil, fmt.Errorf("listenerpass: %w", io.ErrUnexpectedEOF)
	}
	if _, err := decodeHeader(header); err != nil {
		return nil, err
	}
	if oobn == 0 {
		// The bytes arrived without their ancillary data. Overwhelmingly this means a
		// buffering reader on this connection consumed the message first; say so,
		// because the raw symptom gives no hint of the cause.
		return nil, errors.New("listenerpass: the message carried no descriptor (a buffered reader on this connection almost certainly consumed it first; see the package documentation)")
	}
	fd, err := parseOneRight(oob[:oobn])
	if err != nil {
		return nil, err
	}
	// Adopt the descriptor. On failure it must be closed here: nothing else knows
	// about it, so it would otherwise leak for the life of the process.
	f := os.NewFile(uintptr(fd), "listenerpass")
	if f == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("listenerpass: received descriptor is not usable")
	}
	defer f.Close() // FileListener dups; this closes our copy either way
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("listenerpass: adopting the received descriptor as a listener: %w", err)
	}
	return ln, nil
}

// parseOneRight extracts exactly one descriptor from ancillary data, closing any
// extras rather than leaking them.
func parseOneRight(oob []byte) (int, error) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, fmt.Errorf("listenerpass: parsing ancillary data: %w", err)
	}
	var fds []int
	for _, m := range msgs {
		got, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue // not a rights message
		}
		fds = append(fds, got...)
	}
	if len(fds) == 0 {
		return 0, errors.New("listenerpass: ancillary data carried no descriptor")
	}
	for _, extra := range fds[1:] {
		_ = syscall.Close(extra)
	}
	return fds[0], nil
}

// verify asks the kernel whether the descriptor behind ln is still a listening
// socket in good standing.
func verify(ln net.Listener) error {
	sc, ok := ln.(syscall.Conn)
	if !ok {
		return fmt.Errorf("listenerpass: listener of type %T exposes no syscall.Conn", ln)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return fmt.Errorf("listenerpass: listener is not usable: %w", err)
	}
	var accepting, soErr int
	var optErr error
	// A closed descriptor fails HERE, in Control, before any getsockopt runs — which
	// is the most common way a replica goes bad, so it must not be mistaken for a
	// successful check.
	if err := raw.Control(func(fd uintptr) {
		accepting, optErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
		if optErr != nil {
			return
		}
		soErr, optErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ERROR)
	}); err != nil {
		return fmt.Errorf("listenerpass: listener is not usable: %w", err)
	}
	if optErr != nil {
		return fmt.Errorf("listenerpass: querying the listener: %w", optErr)
	}
	if accepting == 0 {
		return errors.New("listenerpass: the socket is not listening (SO_ACCEPTCONN is 0), so adopting it would bind an address that never accepts")
	}
	if soErr != 0 {
		return fmt.Errorf("listenerpass: the socket has a pending error: %w", syscall.Errno(soErr))
	}
	return nil
}
