//go:build !linux

package netnsbind

import (
	"errors"
	"net"
)

// errUnsupported is returned for any cross-namespace operation off Linux.
// Network namespaces are a Linux facility; the callers that need one (the host
// deploy backends and the caretaker) only ever run on Linux.
var errUnsupported = errors.New("netnsbind: entering a network namespace is only supported on linux")

// CanEnter is always an error off Linux: there are no network namespaces to
// enter, so a caller must not promise a workload an endpoint it cannot bind.
func CanEnter() error { return errUnsupported }

// Listen binds in the caller's own namespace when nsPath is empty, matching the
// Linux behaviour, and otherwise reports that it cannot. The empty case is not a
// courtesy: it is what lets a caller with an optional namespace compile and run
// off Linux without branching on GOOS.
func Listen(nsPath, network, addr string) (net.Listener, error) {
	if nsPath == "" {
		return net.Listen(network, addr)
	}
	return nil, errUnsupported
}

// EnsureLocalAddr is unsupported off Linux in both forms: adding a loopback
// alias is a netlink operation with no portable equivalent.
func EnsureLocalAddr(nsPath, ip string) error { return errUnsupported }
