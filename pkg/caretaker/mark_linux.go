//go:build linux

package caretaker

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// markControl returns a socket Control hook that stamps SO_MARK on a connection,
// or nil when mark is 0. When the enforcing proxy shares a pod with client-local
// mounts the caretaker must run as root (mounts need it), so it can no longer be
// exempted from the egress redirect by uid; instead it marks every socket it
// opens and the nftables rule exempts that firewall mark. Needs CAP_NET_ADMIN,
// which the mount caretaker already has (it runs privileged).
func markControl(mark int) func(network, address string, c syscall.RawConn) error {
	if mark == 0 {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
		}); err != nil {
			return err
		}
		return setErr
	}
}

// markDialer returns a dialer that stamps SO_MARK on its connections (a plain
// dialer when mark is 0).
func markDialer(mark int) *net.Dialer {
	return &net.Dialer{Control: markControl(mark)}
}
