//go:build linux

// Package netredirect programs the nftables NAT rules that transparently redirect
// a workload's outbound TCP into a local proxy port, exempting the caretaker's own
// egress (by uid or firewall mark) and loopback. It is shared by the `cornus
// net-redirect` subcommand (a NET_ADMIN init container on Kubernetes) and the host
// companion caretaker, which programs the redirect itself in the shared network
// namespace it already runs in with NET_ADMIN.
package netredirect

import (
	"fmt"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Setup programs nftables nat OUTPUT chains that redirect the app's outbound TCP
// into the local proxy on toPort, while exempting the caretaker's own uid and
// loopback. Both IPv4 (`ip`) and IPv6 (`ip6`) tables are installed: a dual-stack
// pod can reach an AAAA/IPv6-literal destination, and without an ip6 chain that
// traffic would leave the pod unproxied, silently bypassing egress enforcement. It
// talks to the kernel nf_tables subsystem over netlink directly (no CLI).
// Idempotent: it deletes and recreates the cornus tables so a restart re-converges
// cleanly. nftables and any legacy-iptables rules coexist at the netfilter NAT
// hook, so this works regardless of what the node's kube-proxy/CNI use.
func Setup(toPort, exemptUID, exemptMark int) error {
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open nftables netlink: %w", err)
	}

	// Drop any prior cornus tables (best-effort — ignore if absent), applied in
	// their own batch so the subsequent adds start from a clean slate.
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: "cornus"})
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv6, Name: "cornus"})
	_ = c.Flush()

	// IPv4 loopback exemption: ip daddr in 127.0.0.0/8 -> return (incl. the proxy).
	ipv4Loopback := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4}, // IPv4 daddr
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
	// IPv6 loopback exemption: ip6 daddr == ::1 -> return. The IPv6 destination
	// address sits at offset 24 (16 bytes) in the network header.
	ipv6Loopback := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16}, // IPv6 daddr
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: net.IPv6loopback},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}

	addRedirectChain(c, nftables.TableFamilyIPv4, ipv4Loopback, toPort, exemptUID, exemptMark)
	addRedirectChain(c, nftables.TableFamilyIPv6, ipv6Loopback, toPort, exemptUID, exemptMark)

	if err := c.Flush(); err != nil {
		return fmt.Errorf("apply nftables redirect: %w", err)
	}
	return nil
}

// addRedirectChain adds the cornus nat OUTPUT chain for a single address family:
// the uid/mark caretaker exemptions, the family-specific loopback exemption, and
// the TCP redirect to toPort. The uid/mark/TCP rules are L3-agnostic (meta-based)
// so only the loopback exemption differs between IPv4 and IPv6.
func addRedirectChain(c *nftables.Conn, family nftables.TableFamily, loopbackExempt []expr.Any, toPort, exemptUID, exemptMark int) {
	table := c.AddTable(&nftables.Table{Family: family, Name: "cornus"})
	chain := c.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityNATDest,
	})

	// Exempt the caretaker's own egress so its forwarded upstream dials are not
	// re-redirected. Two mutually-exclusive mechanisms: by uid when the caretaker
	// runs as a dedicated non-root uid (proxy-only), or by firewall mark when it
	// runs as root alongside mounts (proxy+mounts) and marks its sockets. Only the
	// provided one is programmed — a uid==0 rule would wrongly exempt the root app
	// container.
	if exemptUID > 0 {
		c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uint32(exemptUID))},
			&expr.Verdict{Kind: expr.VerdictReturn},
		}})
	}
	if exemptMark > 0 {
		c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uint32(exemptMark))},
			&expr.Verdict{Kind: expr.VerdictReturn},
		}})
	}

	// Loopback exemption (family-specific).
	c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: loopbackExempt})

	// meta l4proto tcp -> redirect to :toPort.
	c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(toPort))},
		&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	}})
}
