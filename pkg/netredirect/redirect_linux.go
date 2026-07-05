//go:build linux

// Package netredirect programs cornus's nftables NAT rules in a network
// namespace. Its original and still principal use is the transparent egress
// redirect — capturing a workload's outbound TCP into a local proxy port while
// exempting the caretaker's own egress (by uid or firewall mark) and loopback —
// shared by the `cornus net-redirect` subcommand (a NET_ADMIN init container on
// Kubernetes) and the host companion caretaker, which programs the redirect in
// the network namespace it already shares with the workload.
//
// The package is structured so that ruleset CONSTRUCTION is separate from
// APPLICATION. That separation is not decoration: for as long as there was one
// caller, Setup could hardcode its rules, delete the whole `cornus` table on
// every call, and append its catch-all TCP redirect last — and each of those
// silently made the package unusable by a second concern. A rule added by
// anyone else was destroyed on the next converge, and one appended after the
// catch-all never matched at all. Describing a ruleset as a Spec and applying it
// whole means there is no "anyone else": every caller states the complete desired
// state, and ordering is something you can see rather than something you inherit.
package netredirect

import (
	"fmt"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// tableName is cornus's own nftables table, in both address families.
const tableName = "cornus"

// Rule is one rule of the cornus nat OUTPUT chain, given per address family.
//
// Both families are carried on one value because most rules are L3-agnostic
// (meta-based) and would otherwise have to be written twice and kept in step. A
// family whose expression list is nil is skipped, which is what lets a caller
// express something that only makes sense for one — a link-local IPv4 DNAT has
// no IPv6 counterpart, and inventing one would be worse than omitting it.
//
// Name is for diagnostics and tests only; nftables never sees it.
type Rule struct {
	Name string
	IPv4 []expr.Any
	IPv6 []expr.Any
}

// Spec is the complete desired state of cornus's nat OUTPUT chain. Rules are
// programmed in slice order, and nftables evaluates them in that order, so a
// rule that must win against a broader one below it simply comes first.
type Spec struct {
	// NetNS, when non-zero, is a file descriptor for the network namespace to
	// program; zero programs the caller's own. Note that a zero fd is
	// indistinguishable from "unset" to the underlying library, which treats it
	// as the caller's namespace — so a caller that forgets to open the handle
	// silently programs the HOST ruleset rather than failing.
	//
	// Programming another namespace still needs CAP_SYS_ADMIN: the library sets
	// the namespace by calling setns on a locked OS thread internally, so the
	// caller does not manage threads but does need the privilege.
	NetNS int
	// Rules is the ordered rule set. An empty Spec is legal and means "cornus
	// owns no rules here", which is how a concern removes itself.
	Rules []Rule
}

// Setup programs the transparent-egress redirect. It is the original entry point
// and keeps its exact signature, so both callers and the non-Linux stub are
// unaffected by the restructuring above.
func Setup(toPort, exemptUID, exemptMark int) error {
	return Apply(RedirectSpec(toPort, exemptUID, exemptMark))
}

// RedirectSpec builds the transparent-egress ruleset: the caretaker's own
// exemption, the loopback exemption, then the catch-all TCP redirect to toPort.
//
// Order is the contract. The exemptions must precede the redirect or they never
// match, and the redirect is deliberately last because it matches every TCP
// packet — anything that needs to act on a subset has to come before it.
//
// Both IPv4 (`ip`) and IPv6 (`ip6`) are programmed: a dual-stack pod can reach an
// AAAA/IPv6-literal destination, and without an ip6 chain that traffic would
// leave the pod unproxied, silently bypassing egress enforcement.
func RedirectSpec(toPort, exemptUID, exemptMark int) Spec {
	var rules []Rule
	// Exempt the caretaker's own egress so its forwarded upstream dials are not
	// re-redirected. Two mutually-exclusive mechanisms: by uid when the caretaker
	// runs as a dedicated non-root uid (proxy-only), or by firewall mark when it
	// runs as root alongside mounts (proxy+mounts) and marks its sockets. Only the
	// provided one is programmed — a uid==0 rule would wrongly exempt the root app
	// container.
	if exemptUID > 0 {
		rules = append(rules, Rule{
			Name: "exempt-uid",
			IPv4: metaExempt(expr.MetaKeySKUID, exemptUID),
			IPv6: metaExempt(expr.MetaKeySKUID, exemptUID),
		})
	}
	if exemptMark > 0 {
		rules = append(rules, Rule{
			Name: "exempt-mark",
			IPv4: metaExempt(expr.MetaKeyMARK, exemptMark),
			IPv6: metaExempt(expr.MetaKeyMARK, exemptMark),
		})
	}
	rules = append(rules, Rule{
		Name: "exempt-loopback",
		IPv4: ipv4LoopbackExempt(),
		IPv6: ipv6LoopbackExempt(),
	})
	rules = append(rules, Rule{
		Name: "redirect-tcp",
		IPv4: redirectTCP(toPort),
		IPv6: redirectTCP(toPort),
	})
	return Spec{Rules: rules}
}

// Apply replaces cornus's tables with exactly spec, in both address families.
//
// It talks to the kernel nf_tables subsystem over netlink directly (no CLI), so
// the image needs no packages. Idempotent: the cornus tables are deleted and
// recreated, so a restart re-converges cleanly. nftables and any legacy-iptables
// rules coexist at the netfilter NAT hook, so this works regardless of what the
// node's kube-proxy/CNI use.
//
// The delete lands in its own batch, before the adds, so the new rules start from
// a clean slate rather than racing the old ones within a single transaction. That
// two-batch shape is deliberate and unchanged — what changed is that the delete
// now removes state this same call is about to rewrite in full, instead of
// destroying rules some other concern owned.
func Apply(spec Spec) error {
	opts := []nftables.ConnOption{}
	if spec.NetNS != 0 {
		opts = append(opts, nftables.WithNetNSFd(spec.NetNS))
	}
	c, err := nftables.New(opts...)
	if err != nil {
		return fmt.Errorf("open nftables netlink: %w", err)
	}

	// Drop any prior cornus tables (best-effort — ignore if absent).
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv6, Name: tableName})
	_ = c.Flush()

	if len(spec.Rules) == 0 {
		// Nothing to own here. The delete above already converged.
		return nil
	}
	addChain(c, nftables.TableFamilyIPv4, spec.Rules, func(r Rule) []expr.Any { return r.IPv4 })
	addChain(c, nftables.TableFamilyIPv6, spec.Rules, func(r Rule) []expr.Any { return r.IPv6 })

	if err := c.Flush(); err != nil {
		return fmt.Errorf("apply nftables ruleset: %w", err)
	}
	return nil
}

// addChain creates the cornus nat OUTPUT chain for one address family and adds
// that family's expressions from each rule, in order. A rule contributing no
// expressions for this family is skipped rather than programmed empty.
func addChain(c *nftables.Conn, family nftables.TableFamily, rules []Rule, exprsFor func(Rule) []expr.Any) {
	table := c.AddTable(&nftables.Table{Family: family, Name: tableName})
	chain := c.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityNATDest,
	})
	for _, r := range rules {
		exprs := exprsFor(r)
		if len(exprs) == 0 {
			continue
		}
		c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs})
	}
}

// metaExempt is `meta <key> == value -> return`, the shape both caretaker
// exemptions take. Built per call so the two address families never share a
// backing slice.
func metaExempt(key expr.MetaKey, value int) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uint32(value))},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
}

// ipv4LoopbackExempt is `ip daddr in 127.0.0.0/8 -> return` (which includes the
// proxy itself).
func ipv4LoopbackExempt() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4}, // IPv4 daddr
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
}

// ipv6LoopbackExempt is `ip6 daddr == ::1 -> return`. The IPv6 destination
// address sits at offset 24 (16 bytes) in the network header.
func ipv6LoopbackExempt() []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16}, // IPv6 daddr
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: net.IPv6loopback},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
}

// redirectTCP is `meta l4proto tcp -> redirect to :toPort`. It carries no
// destination condition, so it matches every TCP packet the exemptions above let
// through — which is why anything narrower must be ordered before it.
func redirectTCP(toPort int) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(toPort))},
		&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	}
}
