//go:build linux

package hostrun

// Bridge reaping for the CNI networks generated in network_linux.go. The CNI
// bridge plugin is create-only: it brings a network's bridge up on the first
// attach and leaves it there forever, so freeing a network's /24 without also
// deleting its bridge strands an addressed interface whose route collides with
// whichever network is handed that index next.

import (
	"errors"
	"strings"

	"github.com/vishvananda/netlink"
)

// deleteBridgeByName removes a cornus bridge interface. A link that is already
// gone is success; a link of some other type sharing the name is left alone
// rather than deleted, since cornus did not create it.
func deleteBridgeByName(ifName string) error {
	link, err := netlink.LinkByName(ifName)
	if err != nil {
		var missing netlink.LinkNotFoundError
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}
	if link.Type() != "bridge" {
		return nil
	}
	if err := netlink.LinkDel(link); err != nil {
		var missing netlink.LinkNotFoundError
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}
	return nil
}

// dropStaleBridgesFor deletes every cornus bridge other than keep that already
// carries gw, the gateway address of a /24 about to be brought up. Only a leaked
// bridge can hold it (the allocator never hands one index to two live networks),
// and leaving it in place would give the host two routes for the same prefix.
// Best-effort: this is a repair, not a precondition for creating the network.
func dropStaleBridgesFor(gw, keep string) {
	links, err := netlink.LinkList()
	if err != nil {
		return
	}
	for _, l := range links {
		name := l.Attrs().Name
		if name == keep || !strings.HasPrefix(name, bridgeIfPrefix) || l.Type() != "bridge" {
			continue
		}
		addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a.IP != nil && a.IP.String() == gw {
				_ = netlink.LinkDel(l)
				break
			}
		}
	}
}
