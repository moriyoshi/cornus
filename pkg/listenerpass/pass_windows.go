//go:build windows

package listenerpass

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func supported() bool { return true }

// infoSize is the wire size of a WSAPROTOCOL_INFOW. Sender and receiver are the
// same process architecture on the same machine — this is local IPC between two
// copies of one program — so the struct is shipped as raw bytes rather than given
// a hand-written codec that could only ever drift from the OS definition.
var infoSize = int(unsafe.Sizeof(windows.WSAProtocolInfo{}))

// send produces a WSAPROTOCOL_INFOW for the target process and writes it as the
// payload. Unlike the unix path, nothing travels out of band: this is ordinary
// bytes, and the only reason the API still owns the transfer is that the unix side
// cannot be expressed that way.
func send(uc *net.UnixConn, ln net.Listener, peer Peer) error {
	if peer.Pid <= 0 {
		// Fail here, loudly, rather than at WSADuplicateSocket: "invalid argument"
		// from the syscall gives no hint that the caller's protocol simply never
		// carried the peer's pid, which is the actual mistake and one a unix-only
		// author would never think to make.
		return fmt.Errorf("listenerpass: Peer.Pid is required on Windows (WSADuplicateSocket must name the receiving process), got %d", peer.Pid)
	}
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		return fmt.Errorf("listenerpass: need a *net.TCPListener, got %T", ln)
	}
	f, err := tl.File()
	if err != nil {
		return fmt.Errorf("listenerpass: obtaining the listener handle: %w", err)
	}
	defer f.Close()
	// Disassociate the handle from its I/O completion port before duplicating it.
	// Go's own net.FileListener does exactly this, with the comment "it is not safe
	// to share a duplicated handle that is associated with IOCP" — and the failure it
	// prevents is the worst kind: correct-looking on a quiet machine, corrupt
	// completion state under load.
	_ = f.Fd()

	raw, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("listenerpass: SyscallConn: %w", err)
	}
	var info windows.WSAProtocolInfo
	var dupErr error
	if err := raw.Control(func(fd uintptr) {
		dupErr = windows.WSADuplicateSocket(windows.Handle(fd), uint32(peer.Pid), &info)
	}); err != nil {
		return fmt.Errorf("listenerpass: Control: %w", err)
	}
	if dupErr != nil {
		return fmt.Errorf("listenerpass: WSADuplicateSocket for pid %d: %w", peer.Pid, dupErr)
	}

	payload := unsafe.Slice((*byte)(unsafe.Pointer(&info)), infoSize)
	buf := make([]byte, 0, headerLen+infoSize)
	buf = append(buf, encodeHeader(infoSize)...)
	buf = append(buf, payload...)
	if _, err := uc.Write(buf); err != nil {
		return fmt.Errorf("listenerpass: sending the socket descriptor: %w", err)
	}
	return nil
}

// receive rebuilds the socket from the WSAPROTOCOL_INFOW the sender produced for
// this process.
func receive(uc *net.UnixConn) (net.Listener, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(uc, header); err != nil {
		return nil, fmt.Errorf("listenerpass: receiving the socket descriptor: %w", err)
	}
	n, err := decodeHeader(header)
	if err != nil {
		return nil, err
	}
	if n != infoSize {
		return nil, fmt.Errorf("listenerpass: payload is %d bytes, want a %d-byte WSAPROTOCOL_INFOW (the peer is a different cornus build)", n, infoSize)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(uc, payload); err != nil {
		return nil, fmt.Errorf("listenerpass: receiving the socket descriptor: %w", err)
	}
	var info windows.WSAProtocolInfo
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&info)), infoSize), payload)

	// -1 for family/type/protocol tells WSASocket to take all three from info.
	h, err := windows.WSASocket(-1, -1, -1, &info, 0, windows.WSA_FLAG_OVERLAPPED)
	if err != nil {
		return nil, fmt.Errorf("listenerpass: WSASocket from the received descriptor: %w", err)
	}
	f := os.NewFile(uintptr(h), "listenerpass")
	if f == nil {
		_ = windows.Closesocket(h)
		return nil, errors.New("listenerpass: received handle is not usable")
	}
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("listenerpass: adopting the received handle as a listener: %w", err)
	}
	return ln, nil
}

// Winsock option values. x/sys/windows exports GetsockoptInt but not these two
// constants, and the numbers are fixed by the Winsock ABI (winsock2.h), so naming
// them here is the whole of the port.
const (
	winSOL_SOCKET    = 0xffff
	winSO_ACCEPTCONN = 0x0002
	winSO_ERROR      = 0x1007
)

// verify asks the kernel whether the handle behind ln is still a listening socket
// in good standing.
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
	if err := raw.Control(func(fd uintptr) {
		accepting, optErr = windows.GetsockoptInt(windows.Handle(fd), winSOL_SOCKET, winSO_ACCEPTCONN)
		if optErr != nil {
			return
		}
		soErr, optErr = windows.GetsockoptInt(windows.Handle(fd), winSOL_SOCKET, winSO_ERROR)
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
		return fmt.Errorf("listenerpass: the socket has a pending error: %w", windows.Errno(soErr))
	}
	return nil
}
