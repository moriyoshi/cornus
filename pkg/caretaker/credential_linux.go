//go:build linux

package caretaker

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// ensureLocalAddr makes ip bindable inside the pod netns by adding it to the
// loopback interface (idempotent). It is how the aws-imds "well-known" delivery
// binds 169.254.169.254 — the link-local IMDS address a pod does not otherwise
// carry. Requires NET_ADMIN (the kubernetes backend grants it for WellKnown).
func ensureLocalAddr(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid address %q", ip)
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("lookup lo: %w", err)
	}
	bits := 32
	if parsed.To4() == nil {
		bits = 128
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: parsed, Mask: net.CIDRMask(bits, bits)}}
	if err := netlink.AddrAdd(lo, addr); err != nil {
		// Already present is success — the bind will proceed.
		addrs, lerr := netlink.AddrList(lo, netlink.FAMILY_ALL)
		if lerr == nil {
			for _, a := range addrs {
				if a.IP.Equal(parsed) {
					return nil
				}
			}
		}
		return fmt.Errorf("add %s to lo: %w", ip, err)
	}
	return nil
}
