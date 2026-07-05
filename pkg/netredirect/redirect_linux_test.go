//go:build linux

package netredirect

import (
	"net"
	"reflect"
	"testing"

	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// The assertions below were derived by reading the pre-refactor Setup, not from
// the code that now produces them — the point of the exercise is that separating
// construction from application changed the SHAPE of the package and nothing
// about the ruleset. Only RedirectSpec is exercised: Apply talks netlink and
// needs CAP_SYS_ADMIN, so the constructed spec is the seam a unit test can reach.

// ruleNames is the rule order as a readable sequence, which is the property that
// most matters and the one that regressed silently before: nftables evaluates in
// insertion order, so an exemption after the redirect never fires and a narrower
// rule after the catch-all never matches.
func ruleNames(s Spec) []string {
	out := make([]string, 0, len(s.Rules))
	for _, r := range s.Rules {
		out = append(out, r.Name)
	}
	return out
}

// TestRedirectSpecOrderProxyWithMounts pins the host-caretaker case: root
// caretaker alongside mounts, exempted by firewall mark rather than uid.
func TestRedirectSpecOrderProxyWithMounts(t *testing.T) {
	s := RedirectSpec(3128, 0, 0x6363)
	want := []string{"exempt-mark", "exempt-loopback", "redirect-tcp"}
	if got := ruleNames(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rule order = %v, want %v", got, want)
	}
	if s.NetNS != 0 {
		t.Errorf("NetNS = %d, want 0 — Setup programs the caller's own namespace", s.NetNS)
	}
}

// TestRedirectSpecOrderProxyOnly pins the other exemption mechanism: a caretaker
// running as a dedicated non-root uid, with no mark.
func TestRedirectSpecOrderProxyOnly(t *testing.T) {
	s := RedirectSpec(3128, 1337, 0)
	want := []string{"exempt-uid", "exempt-loopback", "redirect-tcp"}
	if got := ruleNames(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rule order = %v, want %v", got, want)
	}
}

// TestRedirectSpecOmitsZeroExemptions is the one that is not cosmetic. A uid==0
// rule would exempt the ROOT APP CONTAINER from the redirect — i.e. silently
// disable egress enforcement for the workload the redirect exists to capture —
// so "no uid given" must produce no uid rule rather than a rule matching 0.
func TestRedirectSpecOmitsZeroExemptions(t *testing.T) {
	s := RedirectSpec(3128, 0, 0)
	want := []string{"exempt-loopback", "redirect-tcp"}
	if got := ruleNames(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rule order = %v, want %v — a zero uid/mark must not become a matching rule", got, want)
	}
}

// TestRedirectSpecExpressions pins the actual expressions for both families, so
// a refactor that preserved the rule NAMES while changing what they match cannot
// pass. Every value here was read off the pre-refactor implementation.
func TestRedirectSpecExpressions(t *testing.T) {
	const port, mark = 3128, 0x6363
	s := RedirectSpec(port, 0, mark)

	byName := map[string]Rule{}
	for _, r := range s.Rules {
		byName[r.Name] = r
	}

	wantMark := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uint32(mark))},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
	// The mark exemption is L3-agnostic, so both families carry it identically.
	for _, fam := range []struct {
		name  string
		exprs []expr.Any
	}{{"IPv4", byName["exempt-mark"].IPv4}, {"IPv6", byName["exempt-mark"].IPv6}} {
		if !reflect.DeepEqual(fam.exprs, wantMark) {
			t.Errorf("exempt-mark %s = %#v, want %#v", fam.name, fam.exprs, wantMark)
		}
	}

	wantV4Loopback := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
	if got := byName["exempt-loopback"].IPv4; !reflect.DeepEqual(got, wantV4Loopback) {
		t.Errorf("loopback IPv4 = %#v, want %#v", got, wantV4Loopback)
	}
	wantV6Loopback := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: net.IPv6loopback},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}
	if got := byName["exempt-loopback"].IPv6; !reflect.DeepEqual(got, wantV6Loopback) {
		t.Errorf("loopback IPv6 = %#v, want %#v", got, wantV6Loopback)
	}

	// The port is hardcoded in NETWORK byte order rather than computed with
	// binaryutil, so a swap to native endianness fails here on every
	// architecture instead of only on big-endian ones. 3128 == 0x0c38.
	wantRedirect := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Immediate{Register: 1, Data: []byte{0x0c, 0x38}},
		&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	}
	if got := byName["redirect-tcp"].IPv4; !reflect.DeepEqual(got, wantRedirect) {
		t.Errorf("redirect IPv4 = %#v, want %#v", got, wantRedirect)
	}
	if got := byName["redirect-tcp"].IPv6; !reflect.DeepEqual(got, wantRedirect) {
		t.Errorf("redirect IPv6 = %#v, want %#v", got, wantRedirect)
	}
}

// TestRedirectIsLastAndUnconditional records WHY the package needed restructuring
// at all. The final rule matches every TCP packet with no destination condition,
// so any rule appended after it is dead — which is exactly what happened when a
// link-local DNAT was proposed as an addition to this chain. A future rule must
// be INSERTED before this one, and this test fails if that stops being true.
func TestRedirectIsLastAndUnconditional(t *testing.T) {
	s := RedirectSpec(3128, 0, 0x6363)
	last := s.Rules[len(s.Rules)-1]
	if last.Name != "redirect-tcp" {
		t.Fatalf("last rule = %q, want redirect-tcp", last.Name)
	}
	for _, e := range last.IPv4 {
		if p, ok := e.(*expr.Payload); ok {
			t.Fatalf("the catch-all redirect gained a payload match (%#v); if it is now conditional, "+
				"the ordering contract this package documents no longer holds", p)
		}
	}
}

// TestSpecSharesNoBackingSliceBetweenFamilies guards a subtle aliasing trap: the
// pre-refactor code built each family's expressions separately, and collapsing
// that into one shared slice would let a future per-family mutation silently
// affect the other family.
func TestSpecSharesNoBackingSliceBetweenFamilies(t *testing.T) {
	s := RedirectSpec(3128, 0, 0x6363)
	for _, r := range s.Rules {
		if len(r.IPv4) == 0 || len(r.IPv6) == 0 {
			continue
		}
		if &r.IPv4[0] == &r.IPv6[0] {
			t.Errorf("rule %q shares one backing array between IPv4 and IPv6", r.Name)
		}
	}
}
