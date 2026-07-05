package conduithost

import (
	"net"
	"strconv"
	"sync"
	"time"
)

// dualStackProbeTimeout bounds the one-off probe below. It dials loopback on this
// same machine, so anything beyond a few hundred milliseconds means the probe
// itself is wedged rather than that the answer is "no".
const dualStackProbeTimeout = 500 * time.Millisecond

var (
	dualStackOnce sync.Once
	dualStackVal  bool
)

// DualStackWildcard reports whether an IPv6 wildcard listener ("[::]:port") on
// this system also accepts IPv4 connections — i.e. whether IPV6_V6ONLY is off.
//
// It MEASURES rather than assumes, because the answer is genuinely per-system: it
// is off by default on Linux (net.ipv6.bindv6only=0) and on by default on OpenBSD,
// and a sysctl or a container's netns can flip it either way. Coverage decisions
// ride on this, and both wrong answers are bad — claiming dual-stack when it is
// off makes a join hand back a conduit that never receives the client's IPv4
// connections, while denying it forks a second proxy that then fails to bind.
//
// The probe binds an IPv6 wildcard listener on an ephemeral port and dials it over
// IPv4 loopback. A false answer is returned for any failure to even set the probe
// up (no IPv6 at all, for instance), which is the conservative direction: it costs
// a missed consolidation, never a silently unreachable conduit.
//
// It binds with net.Listen("tcp", ...) — the SAME call socks5.Start makes
// (pkg/socks5/socks5.go:552) — and that detail is load-bearing, not incidental. Go
// sets IPV6_V6ONLY on a listener whose network is explicitly "tcp6", so probing
// with net.ListenTCP("tcp6", ...) measures a socket the conduit never creates and
// reports v6only on a dual-stack Linux host. That exact error was live here until
// TestCoversMatchesTheKernel drove the prediction against a real connect.
//
// The result is computed once per process. It describes the kernel, which does not
// change under us in any way that matters within one process's lifetime.
func DualStackWildcard() bool {
	dualStackOnce.Do(func() { dualStackVal = probeDualStack() })
	return dualStackVal
}

func probeDualStack() bool {
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		return false // no IPv6 wildcard available; nothing to be dual-stack about
	}
	defer ln.Close()
	tcpLn, ok := ln.(*net.TCPListener)
	if !ok {
		return false
	}
	port := tcpLn.Addr().(*net.TCPAddr).Port
	// Dial the SAME port over IPv4 loopback. Only a dual-stack listener answers.
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), dualStackProbeTimeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	// Drain the accepted side so the probe leaves nothing for the deferred Close to
	// sever, and so a lingering half-open connection cannot outlive the probe.
	_ = tcpLn.SetDeadline(time.Now().Add(dualStackProbeTimeout))
	if ac, err := tcpLn.Accept(); err == nil {
		_ = ac.Close()
	}
	return true
}
