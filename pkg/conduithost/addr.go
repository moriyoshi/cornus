// Package conduithost turns a SOCKS5 conduit into an addressable rendezvous: the
// first process to request a conduit at a bind address binds it and hosts a
// control socket beside it, and every later process requesting an address that
// the incumbent's bind COVERS joins it instead of forking a second proxy.
//
// The bind address is the identity, because it is the one thing a browser is
// actually pointed at. That replaces the older model, where a conduit's identity
// was a 12-field configuration struct and sharing was emergent from two
// configurations happening to be structurally equal — which made sharing
// unaddressable across processes and left a foreground session's proxy
// permanently unjoinable. See the JOURNAL entry "the conduit rendezvous design, as built".
package conduithost

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Addr is a normalized conduit bind address: an IP (possibly unspecified) and a
// port. It is the rendezvous identity.
//
// The IP is kept as a netip.Addr rather than a string so coverage is decided on
// the value the kernel would bind, not on spelling — "127.0.0.1", "127.1", and
// "::ffff:127.0.0.1" are one address, and answering otherwise would fork proxies
// for the same reason canonicalConduitCfg had to exist.
type Addr struct {
	IP   netip.Addr
	Port int
}

// ParseAddr normalizes a "host:port" bind string.
//
// A hostname is rejected rather than resolved: which address a name binds is a
// question with a different answer per host and per moment, so admitting one
// would make the rendezvous identity non-deterministic. Callers that want to bind
// a name resolve it themselves and pass the literal.
//
// An empty host means the IPv4 unspecified address, matching net.Listen's
// treatment of ":port" as a wildcard bind, so the wildcard has exactly one
// spelling here.
func ParseAddr(s string) (Addr, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return Addr{}, fmt.Errorf("conduit address %q: %w", s, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return Addr{}, fmt.Errorf("conduit address %q: bad port %q (want 0-65535)", s, portStr)
	}
	if host == "" {
		return Addr{IP: netip.IPv4Unspecified(), Port: port}, nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return Addr{}, fmt.Errorf("conduit address %q: host must be an IP literal, not a name", s)
	}
	return Addr{IP: canonicalIP(ip), Port: port}, nil
}

// canonicalIP folds the spellings that name one kernel-level address into one
// value: the IPv4-mapped IPv6 form ("::ffff:127.0.0.1") becomes plain IPv4, and a
// zone is dropped from an unspecified address (where it cannot mean anything).
func canonicalIP(ip netip.Addr) netip.Addr {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if ip.IsUnspecified() {
		ip = ip.WithZone("")
	}
	return ip
}

// String renders the address the way net.Listen would take it back.
func (a Addr) String() string { return net.JoinHostPort(a.IP.String(), strconv.Itoa(a.Port)) }

// Ephemeral reports whether the address names port 0 — a bind the kernel assigns.
// Such a conduit is inherently private: there is no agreed address for anyone else
// to rendezvous on, so it is never published to the rendezvous directory. This is
// what replaces the old Socks5SessionLocal boolean, as a property that FOLLOWS
// from the address instead of an independent flag that could contradict it.
func (a Addr) Ephemeral() bool { return a.Port == 0 }

// Loopback reports whether the address is reachable only from this host.
// The unspecified address is not loopback: it binds every interface.
func (a Addr) Loopback() bool { return a.IP.IsLoopback() }

// Covers reports whether a conduit bound at a serves a client that asked to bind
// at b — that is, whether b can JOIN a instead of binding its own listener.
//
// The rule is containment of the address the kernel would accept on, not equality:
//
//   - the same address always covers itself;
//   - the IPv4 unspecified address (0.0.0.0) covers every IPv4 address;
//   - the IPv6 unspecified address (::) covers every IPv6 address, and — only when
//     the listener is dual-stack — every IPv4 address too;
//   - nothing else covers anything else.
//
// Coverage is deliberately one-directional. An incumbent on 127.0.0.1 does NOT
// cover a request for 0.0.0.0: a bind cannot be widened in place, so that request
// must be refused with the incumbent named rather than silently served on a
// narrower address than it asked for.
//
// dualStack says whether an IPv6 wildcard listener on this system also accepts
// IPv4 (IPV6_V6ONLY off). It is a parameter rather than a constant because the
// answer differs by OS and by sysctl, and guessing it wrong in either direction
// forks a proxy or refuses a legitimate join. Callers pass what they MEASURED —
// see DualStackWildcard.
func Covers(a, b Addr, dualStack bool) bool {
	if a.Port != b.Port {
		return false
	}
	if a.IP == b.IP {
		return true
	}
	if !a.IP.IsUnspecified() {
		return false
	}
	if a.IP.Is4() {
		return b.IP.Is4()
	}
	// a is the IPv6 wildcard: it always covers IPv6, and covers IPv4 only when the
	// kernel actually accepts v4 on a v6 wildcard listener.
	return b.IP.Is6() || dualStack
}
