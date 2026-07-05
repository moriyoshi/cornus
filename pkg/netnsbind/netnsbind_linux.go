//go:build linux

// Package netnsbind binds sockets and addresses inside another process's network
// namespace. It exists so that ONE component can serve a network surface that
// lives inside a workload's namespace without anything running beside the
// workload to host it.
//
// That is the whole trick behind server-delivered credential endpoints. On
// Kubernetes a credential endpoint is served by a sidecar because the sidecar is
// the only thing inside the pod; the app reaches it at 127.0.0.1 and is
// authorized by nothing more than sharing the namespace. On a host backend the
// cornus server can enter the workload's namespace directly, so the sidecar is
// not needed — but the authorization story is deliberately IDENTICAL, because it
// is the namespace boundary doing the work in both cases, not a credential or a
// token on the wire. A listener bound here is reachable by that workload and by
// nothing else on the host.
//
// The key property that makes this practical: a socket is bound to the network
// namespace that was current WHEN IT WAS CREATED, and it stays bound to it. So
// the namespace only has to be entered for the length of the bind — Accept, and
// every connection that comes out of it, then runs normally on any thread. The
// same is true of an address added to a link. Nothing here holds a namespace
// open for the life of the service.
//
// Entering a namespace requires CAP_SYS_ADMIN. Callers are expected to check for
// that and refuse with a diagnosis rather than letting setns fail from inside a
// goroutine (pkg/hostcheck carries the check).
package netnsbind

import (
	"fmt"
	"net"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// CanEnter reports whether this process may enter another network namespace at
// all, which is the privilege everything else here needs.
//
// It probes the real operation rather than inferring from euid, following
// builderctr.CanMount: privilege is not the same question as uid. A process can
// be root and still blocked (seccomp, a restrictive container), or non-root and
// capable (CAP_SYS_ADMIN, or root inside a user namespace). Entering THIS
// process's own namespace is the cheapest faithful probe — it needs exactly the
// same permission and changes nothing.
//
// Callers should ask before promising a workload an endpoint. Without it the
// failure lands late and in the wrong place: the deploy succeeds, the workload
// starts, and its credential simply never appears.
func CanEnter() error {
	if err := ns.WithNetNSPath("/proc/self/ns/net", func(ns.NetNS) error { return nil }); err != nil {
		return fmt.Errorf("netnsbind: cannot enter a network namespace (needs CAP_SYS_ADMIN): %w", err)
	}
	return nil
}

// Listen binds a listener inside the network namespace at nsPath, which is a
// procfs or bind-mounted namespace path (`/proc/<pid>/ns/net`, or a pin such as
// /run/cornus/netns/<id>). An empty nsPath binds in the caller's own namespace,
// so a caller that serves both locally and remotely needs no branch.
//
// The returned listener is usable normally: it belongs to the target namespace
// for its whole life, and the calling goroutine is back in its original
// namespace by the time this returns.
func Listen(nsPath, network, addr string) (net.Listener, error) {
	if nsPath == "" {
		return net.Listen(network, addr)
	}
	var (
		ln   net.Listener
		lerr error
	)
	// WithNetNSPath locks the OS thread, setns's into the target, runs the
	// closure, and restores the original namespace even if the closure fails.
	// Doing that by hand is where this goes wrong: setns is per-THREAD, so
	// without the lock the Go runtime can migrate the goroutine mid-bind and
	// create the socket in whichever namespace the new thread happened to be in.
	if err := ns.WithNetNSPath(nsPath, func(ns.NetNS) error {
		ln, lerr = net.Listen(network, addr)
		return lerr
	}); err != nil {
		return nil, fmt.Errorf("netnsbind: listen %s in %s: %w", addr, nsPath, err)
	}
	return ln, nil
}

// EnsureLocalAddr makes ip bindable inside the namespace at nsPath by adding it
// to that namespace's loopback interface. An empty nsPath operates on the
// caller's own namespace.
//
// It is how a "well-known" credential delivery binds a link-local address the
// workload does not otherwise carry — 169.254.169.254 for the AWS IMDS shape, so
// an unmodified SDK finds its credentials at the address it already looks for,
// with no environment variable at all. Idempotent: an address that is already
// present is success, since the only thing the caller cares about is that the
// bind which follows can succeed.
func EnsureLocalAddr(nsPath, ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("netnsbind: invalid address %q", ip)
	}
	add := func() error { return addLoopbackAddr(parsed) }
	if nsPath == "" {
		return add()
	}
	if err := ns.WithNetNSPath(nsPath, func(ns.NetNS) error { return add() }); err != nil {
		return fmt.Errorf("netnsbind: add %s to lo in %s: %w", ip, nsPath, err)
	}
	return nil
}

// addLoopbackAddr adds ip to "lo" in the CURRENT network namespace.
func addLoopbackAddr(ip net.IP) error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("lookup lo: %w", err)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}}
	if err := netlink.AddrAdd(lo, addr); err != nil {
		// Already present is success — the bind will proceed. Checked by
		// listing rather than by matching on the error string, which varies.
		addrs, lerr := netlink.AddrList(lo, netlink.FAMILY_ALL)
		if lerr == nil {
			for _, a := range addrs {
				if a.IP.Equal(ip) {
					return nil
				}
			}
		}
		return fmt.Errorf("add %s to lo: %w", ip, err)
	}
	return nil
}
