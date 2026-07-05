//go:build linux

package caretaker

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// originalDst returns the pre-DNAT destination of a connection redirected by
// iptables REDIRECT, read from the SO_ORIGINAL_DST socket option. This is how
// the enforcing proxy learns which service the app actually dialed after its
// egress was transparently captured.
func originalDst(c *net.TCPConn) (string, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return "", err
	}
	var addr string
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		// SO_ORIGINAL_DST on the IPv4 (SOL_IP) or IPv6 level. Try IPv4 first —
		// kind pods are IPv4 — then fall back to IPv6.
		sa, e := unix.GetsockoptIPv6Mreq(int(fd), syscall.SOL_IP, unix.SO_ORIGINAL_DST)
		if e == nil {
			// Multiaddr holds a raw sockaddr_in: [0:2]=sin_family,
			// [2:4]=sin_port (big-endian), [4:8]=sin_addr.
			port := int(sa.Multiaddr[2])<<8 | int(sa.Multiaddr[3])
			ip := net.IPv4(sa.Multiaddr[4], sa.Multiaddr[5], sa.Multiaddr[6], sa.Multiaddr[7])
			addr = net.JoinHostPort(ip.String(), fmt.Sprint(port))
			return
		}
		sockErr = e
	}); err != nil {
		return "", err
	}
	if addr == "" {
		return "", fmt.Errorf("SO_ORIGINAL_DST: %v", sockErr)
	}
	return addr, nil
}
