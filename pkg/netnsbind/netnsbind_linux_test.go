//go:build linux

package netnsbind

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/containerd/containerd/pkg/netns"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// TestListenInCurrentNamespaceNeedsNoPrivilege pins the empty-path contract:
// callers with an optional namespace must not have to branch, and that path has
// to work unprivileged or every such caller would need root.
func TestListenInCurrentNamespaceNeedsNoPrivilege(t *testing.T) {
	ln, err := Listen("", "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen with empty nsPath: %v", err)
	}
	defer ln.Close()
	c, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial own-namespace listener: %v", err)
	}
	c.Close()
}

// TestEnsureLocalAddrRejectsANonAddress checks the argument is validated before
// any netlink call, which is what lets the error be intelligible (and is why
// this test needs no privilege).
func TestEnsureLocalAddrRejectsANonAddress(t *testing.T) {
	err := EnsureLocalAddr("", "not-an-ip")
	if err == nil {
		t.Fatal("want an error for a non-address, got nil")
	}
	if got := err.Error(); got == "" || !contains(got, "invalid address") {
		t.Fatalf("error must name the problem, got %q", got)
	}
}

// TestListenBindsInTheTargetNamespaceAndNowhereElse is the test this package
// exists for. Its two halves catch DIFFERENT faults, which is worth stating
// because it would be easy to think either one alone is enough.
//
// The positive half catches "bound in the wrong namespace": neutralized by
// dropping the nsPath branch so Listen binds locally, this is the assertion that
// fires (measured — the inside-namespace dial is refused).
//
// The negative half catches "reachable from more than the workload", and it is
// not defensive padding: the obvious ALTERNATIVE implementation — bind on the
// host and bridge it into the workload with a DNAT rule or a forwarder — passes
// the positive half and fails this one. That design is the security bug this
// package exists to avoid, because it would publish a live credential endpoint
// to every process on the host while looking correct from inside the container.
// Both halves therefore stay.
func TestListenBindsInTheTargetNamespaceAndNowhereElse(t *testing.T) {
	requireRoot(t)

	target := newNetNS(t)

	ln, err := Listen(target, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen in %s: %v", target, err)
	}
	defer ln.Close()
	go serveOne(ln)

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", port)

	// Positive: a process inside the namespace reaches it. This is the workload's
	// view — the same 127.0.0.1:<port> a pod sees on kubernetes.
	if err := ns.WithNetNSPath(target, func(ns.NetNS) error {
		c, derr := net.DialTimeout("tcp", addr, 5*time.Second)
		if derr != nil {
			return derr
		}
		return c.Close()
	}); err != nil {
		t.Fatalf("dial %s from inside the target namespace: %v", addr, err)
	}

	// Negative: the host cannot. See the doc comment for which fault this half
	// owns — it is the one the positive half cannot see.
	if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		c.Close()
		t.Fatalf("%s is reachable from the HOST namespace: the listener was not "+
			"confined to the workload's namespace, so the netns boundary is not "+
			"actually the authorization for this endpoint", addr)
	}
}

// TestEnsureLocalAddrMakesALinkLocalAddressBindable covers the well-known/IMDS
// case: 169.254.169.254 is not carried by a fresh namespace, so binding it must
// fail until the address is added, and succeed after.
func TestEnsureLocalAddrMakesALinkLocalAddressBindable(t *testing.T) {
	requireRoot(t)

	target := newNetNS(t)
	const imds = "169.254.169.254"

	if _, err := Listen(target, "tcp", net.JoinHostPort(imds, "0")); err == nil {
		t.Fatal("binding the IMDS address must fail before it is added to lo; " +
			"if this passes, the namespace already carried the address and the " +
			"rest of this test proves nothing")
	}
	if err := EnsureLocalAddr(target, imds); err != nil {
		t.Fatalf("EnsureLocalAddr(%s): %v", imds, err)
	}
	ln, err := Listen(target, "tcp", net.JoinHostPort(imds, "0"))
	if err != nil {
		t.Fatalf("bind %s after EnsureLocalAddr: %v", imds, err)
	}
	ln.Close()

	// Idempotent: a second call is success, not "file exists". A refresh or a
	// second delivery on the same namespace must not fail.
	if err := EnsureLocalAddr(target, imds); err != nil {
		t.Fatalf("EnsureLocalAddr must be idempotent, second call: %v", err)
	}
}

// newNetNS creates a pinned network namespace with loopback UP and returns its
// path. lo comes up DOWN in a fresh namespace, so without setting it up a
// 127.0.0.1 dial has no route and the positive assertion would fail for a
// reason that has nothing to do with this package.
func newNetNS(t *testing.T) string {
	t.Helper()
	n, err := netns.NewNetNS(t.TempDir())
	if err != nil {
		t.Skipf("cannot create a network namespace (%v); this needs CAP_SYS_ADMIN and a writable mount namespace", err)
	}
	t.Cleanup(func() { _ = n.Remove() })
	path := n.GetPath()
	if err := ns.WithNetNSPath(path, func(ns.NetNS) error {
		lo, lerr := netlink.LinkByName("lo")
		if lerr != nil {
			return lerr
		}
		return netlink.LinkSetUp(lo)
	}); err != nil {
		t.Fatalf("bring lo up in %s: %v", path, err)
	}
	return path
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: creating and entering a network namespace requires CAP_SYS_ADMIN")
	}
}

// serveOne accepts until the listener closes, so a dial completes rather than
// sitting in the accept backlog.
func serveOne(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = fmt.Fprintln(c, "ok")
		c.Close()
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
