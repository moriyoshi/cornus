package conduithost

import (
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func mustAddr(t *testing.T, s string) Addr {
	t.Helper()
	a, err := ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q) = %v", s, err)
	}
	return a
}

func TestParseAddrNormalizes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"127.0.0.1:1080", "127.0.0.1:1080"},
		{"0.0.0.0:1080", "0.0.0.0:1080"},
		{":1080", "0.0.0.0:1080"},                     // net.Listen's wildcard spelling
		{"[::ffff:127.0.0.1]:1080", "127.0.0.1:1080"}, // v4-mapped folds to v4
		{"[::]:1080", "[::]:1080"},
		{"[::1]:1080", "[::1]:1080"},
		{" 127.0.0.1:1080 ", "127.0.0.1:1080"},
		{"127.0.0.1:0", "127.0.0.1:0"},
	} {
		got, err := ParseAddr(tc.in)
		if err != nil {
			t.Errorf("ParseAddr(%q) = %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("ParseAddr(%q).String() = %q, want %q", tc.in, got.String(), tc.want)
		}
	}
}

// A hostname must be refused rather than resolved: which address a name binds
// differs per host and per moment, so admitting one would make the rendezvous
// identity non-deterministic — two processes could "agree" on a name and bind
// different addresses.
func TestParseAddrRejectsNamesAndBadPorts(t *testing.T) {
	for _, in := range []string{
		"localhost:1080",
		"cornus.internal:1080",
		"127.0.0.1:65536",
		"127.0.0.1:-1",
		"127.0.0.1:http",
		"127.0.0.1",
		"",
	} {
		if got, err := ParseAddr(in); err == nil {
			t.Errorf("ParseAddr(%q) = %v, want an error", in, got)
		}
	}
}

func TestEphemeralAndLoopback(t *testing.T) {
	if !mustAddr(t, "127.0.0.1:0").Ephemeral() {
		t.Error("port 0 is not Ephemeral")
	}
	if mustAddr(t, "127.0.0.1:1080").Ephemeral() {
		t.Error("port 1080 is Ephemeral")
	}
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:1080", true},
		{"127.5.5.5:1080", true},
		{"[::1]:1080", true},
		{"0.0.0.0:1080", false}, // binds every interface, so not loopback-only
		{"[::]:1080", false},
		{"192.168.1.5:1080", false},
	} {
		if got := mustAddr(t, tc.addr).Loopback(); got != tc.want {
			t.Errorf("Addr(%s).Loopback() = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		incumbent string
		request   string
		dualStack bool
		want      bool
	}{
		{"identical loopback", "127.0.0.1:1080", "127.0.0.1:1080", false, true},
		{"identical wildcard", "0.0.0.0:1080", "0.0.0.0:1080", false, true},
		{"v4 wildcard covers loopback", "0.0.0.0:1080", "127.0.0.1:1080", false, true},
		{"v4 wildcard covers a LAN address", "0.0.0.0:1080", "192.168.1.5:1080", false, true},
		{"different port never covers", "0.0.0.0:1080", "127.0.0.1:1081", false, false},

		// One-directional: a bind cannot be widened in place, so the narrower
		// incumbent must NOT swallow a request that asked for more exposure.
		{"loopback does not cover wildcard", "127.0.0.1:1080", "0.0.0.0:1080", false, false},
		{"loopback does not cover a LAN address", "127.0.0.1:1080", "192.168.1.5:1080", false, false},
		{"disjoint unicast", "192.168.1.5:1080", "192.168.1.6:1080", false, false},

		{"v4 wildcard does not cover v6", "0.0.0.0:1080", "[::1]:1080", false, false},
		{"v6 wildcard covers v6", "[::]:1080", "[::1]:1080", false, true},
		{"v6 wildcard covers v4 only when dual-stack", "[::]:1080", "127.0.0.1:1080", true, true},
		{"v6 wildcard refuses v4 when v6only", "[::]:1080", "127.0.0.1:1080", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Covers(mustAddr(t, tc.incumbent), mustAddr(t, tc.request), tc.dualStack)
			if got != tc.want {
				t.Errorf("Covers(%s, %s, dualStack=%v) = %v, want %v",
					tc.incumbent, tc.request, tc.dualStack, got, tc.want)
			}
		})
	}
}

// Covers is a claim about what the KERNEL will accept, so checking it against the
// table above only proves the table and the code agree. This drives the claim
// against a real listener: for each covering pair, a listener bound at the
// incumbent must actually answer a connection addressed to the request, and for
// each non-covering pair it must not. Without this, a wrong belief about
// wildcards would be certified by a green table test.
// firstNonLoopbackIPv4 returns an IPv4 address that is assigned to a real
// interface on this host, or "" when there is none (an isolated container, say).
// Dialing it reaches this machine, so a listener bound only to loopback must
// refuse it — which is the negative case TestCoversMatchesTheKernel needs.
func firstNonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || !n.IP.IsGlobalUnicast() {
			continue
		}
		return n.IP.String()
	}
	return ""
}

func TestCoversMatchesTheKernel(t *testing.T) {
	dual := DualStackWildcard()
	t.Logf("measured dual-stack IPv6 wildcard: %v", dual)

	cases := []struct{ incumbent, dialTo string }{
		{"127.0.0.1", "127.0.0.1"},
		{"0.0.0.0", "127.0.0.1"},
		{"::", "::1"},
		{"::", "127.0.0.1"},
	}
	// A NON-covering pair is what keeps this test honest. Without one, every case
	// expects reachable=true and a Covers that unconditionally returned true would
	// pass — certifying the one direction nobody checked. A real non-loopback
	// interface address gives us a pair the kernel genuinely refuses.
	if lan := firstNonLoopbackIPv4(t); lan != "" {
		cases = append(cases,
			struct{ incumbent, dialTo string }{"127.0.0.1", lan}, // must NOT be reachable
			struct{ incumbent, dialTo string }{"0.0.0.0", lan},   // must be reachable
		)
	} else {
		t.Log("no non-loopback IPv4 interface: skipping the non-covering pairs")
	}

	for _, tc := range cases {
		t.Run(tc.incumbent+"->"+tc.dialTo, func(t *testing.T) {
			ip, err := netip.ParseAddr(tc.incumbent)
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(tc.incumbent, "0"))
			if err != nil {
				t.Skipf("cannot bind %s on this host: %v", tc.incumbent, err)
			}
			defer ln.Close()
			port := ln.Addr().(*net.TCPAddr).Port

			incumbent := Addr{IP: canonicalIP(ip), Port: port}
			request := mustAddr(t, net.JoinHostPort(tc.dialTo, strconv.Itoa(port)))
			predicted := Covers(incumbent, request, dual)

			done := make(chan struct{})
			go func() {
				defer close(done)
				if c, err := ln.Accept(); err == nil {
					_ = c.Close()
				}
			}()

			c, err := net.DialTimeout("tcp", request.String(), time.Second)
			reachable := err == nil
			if reachable {
				_ = c.Close()
				<-done
			} else {
				_ = ln.Close()
				<-done
			}

			if reachable != predicted {
				t.Errorf("listener on %s, dialing %s: kernel reachable=%v but Covers said %v",
					incumbent, request, reachable, predicted)
			}
		})
	}
}
