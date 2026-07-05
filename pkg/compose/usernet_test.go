package compose

import (
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

// planTwice loads and plans the same file twice, so tests can assert the
// allocation is deterministic across re-deploys.
func planTwice(t *testing.T, file, project string) (a, b map[string]ServicePlan) {
	t.Helper()
	for _, out := range []*map[string]ServicePlan{&a, &b} {
		p, err := Load(file)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		plans, err := p.Plan(project)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		*out = plans
	}
	return a, b
}

// mustCIDRIn parses a pinned attachment address and asserts it is a usable
// host address inside subnet (not the network, gateway, or broadcast address).
func mustCIDRIn(t *testing.T, cidr, subnet string) netip.Addr {
	t.Helper()
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		t.Fatalf("pinned IP %q is not CIDR: %v", cidr, err)
	}
	sub := netip.MustParsePrefix(subnet)
	if pfx.Bits() != sub.Bits() || !sub.Contains(pfx.Addr()) {
		t.Fatalf("pinned IP %s is not inside %s", cidr, subnet)
	}
	base := sub.Masked().Addr().As4()
	last := base
	size := 1 << (32 - sub.Bits())
	carry := int(last[3]) + size - 1
	last[3] = byte(carry & 0xff)
	last[2] += byte(carry >> 8) // enough for the /24 and /29 subnets used here
	got := pfx.Addr().As4()
	if got == base {
		t.Fatalf("pinned IP %s is the network address", cidr)
	}
	gw := base
	gw[3]++
	if got == gw {
		t.Fatalf("pinned IP %s is the gateway address", cidr)
	}
	if got == last {
		t.Fatalf("pinned IP %s is the broadcast address", cidr)
	}
	return pfx.Addr()
}

// TestUserNetStaticIPs covers the happy path of the plan-time allocator: on a
// Multus-driver network each member gets a deterministic CIDR pin inside the
// derived default subnet, and every member's DNSSpec records all peer aliases
// at their user-network addresses (RequireUserNet so the backend can degrade
// gracefully off-Multus). A service not on the network is untouched.
func TestUserNetStaticIPs(t *testing.T) {
	file := writeCompose(t, `
services:
  a:
    image: nginx
    networks: [mesh]
  b:
    image: alpine
    networks:
      mesh:
        aliases: [bee]
  other:
    image: x
networks:
  mesh:
    driver: bridge
`)
	plans, again := planTwice(t, file, "proj")
	subnet := MultusDefaultSubnet("proj_mesh")

	ipA := mustCIDRIn(t, plans["a"].Spec.Networks[0].IP, subnet)
	ipB := mustCIDRIn(t, plans["b"].Spec.Networks[0].IP, subnet)
	if ipA == ipB {
		t.Fatalf("a and b share the pinned IP %s", ipA)
	}
	// Deterministic across a full reload+replan.
	if plans["a"].Spec.Networks[0].IP != again["a"].Spec.Networks[0].IP ||
		plans["b"].Spec.Networks[0].IP != again["b"].Spec.Networks[0].IP {
		t.Error("re-planning the same project changed the pinned IPs")
	}

	for _, svc := range []string{"a", "b"} {
		dns := plans[svc].Spec.DNS
		if dns == nil || !dns.RequireUserNet {
			t.Fatalf("%s DNS = %+v, want RequireUserNet records", svc, dns)
		}
		want := map[string]string{"a": ipA.String(), "b": ipB.String(), "bee": ipB.String()}
		for name, ip := range want {
			if dns.Records[name] != ip {
				t.Errorf("%s record %s = %q, want %s", svc, name, dns.Records[name], ip)
			}
		}
		if len(dns.Records) != len(want) {
			t.Errorf("%s records = %v, want exactly %v", svc, dns.Records, want)
		}
	}

	// The off-network service keeps the implicit default network untouched.
	if other := plans["other"].Spec; other.DNS != nil || other.Networks[0].IP != "" {
		t.Errorf("other = DNS %+v IP %q, want no user-net addressing", other.DNS, other.Networks[0].IP)
	}
}

// TestUserNetExplicitIPv4Address: the compose `ipv4_address` pin wins (and is
// normalised to CIDR), peers' records point at it, and conflicting or
// out-of-subnet pins are plan errors.
func TestUserNetExplicitIPv4Address(t *testing.T) {
	file := writeCompose(t, `
services:
  db:
    image: db
    networks:
      mesh:
        ipv4_address: 10.99.0.50
  web:
    image: web
    networks: [mesh]
networks:
  mesh:
    driver: macvlan
    driver_opts:
      master: eth0
      subnet: 10.99.0.0/24
`)
	p, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := p.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plans["db"].Spec.Networks[0].IP; got != "10.99.0.50/24" {
		t.Errorf("db pinned IP = %q, want the explicit 10.99.0.50/24", got)
	}
	if got := plans["web"].Spec.DNS.Records["db"]; got != "10.99.0.50" {
		t.Errorf("web's db record = %q, want the explicit 10.99.0.50", got)
	}
	web := mustCIDRIn(t, plans["web"].Spec.Networks[0].IP, "10.99.0.0/24")
	if web.String() == "10.99.0.50" {
		t.Error("derived web IP collided with the explicit pin (must probe around it)")
	}

	for name, body := range map[string]string{
		"outside subnet": `
services:
  db:
    image: db
    networks:
      mesh: {ipv4_address: 10.98.0.50}
networks:
  mesh: {driver: bridge, driver_opts: {subnet: 10.99.0.0/24}}
`,
		"duplicate pins": `
services:
  a:
    image: a
    networks:
      mesh: {ipv4_address: 10.99.0.5}
  b:
    image: b
    networks:
      mesh: {ipv4_address: 10.99.0.5}
networks:
  mesh: {driver: bridge, driver_opts: {subnet: 10.99.0.0/24}}
`,
		"pin under host-local opt-out": `
services:
  a:
    image: a
    networks:
      mesh: {ipv4_address: 10.99.0.5}
networks:
  mesh: {driver: bridge, driver_opts: {subnet: 10.99.0.0/24, ipam: host-local}}
`,
	} {
		p, err := Load(writeCompose(t, body))
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if _, err := p.Plan("proj"); err == nil {
			t.Errorf("%s: Plan succeeded, want an error", name)
		}
	}
}

// TestUserNetDynamicFallbacks: a scaled member or an explicit
// `ipam: host-local` opt-out keeps the whole network dynamically addressed —
// no pins, no DNS records (the pre-A' behaviour).
func TestUserNetDynamicFallbacks(t *testing.T) {
	for name, body := range map[string]string{
		"scaled member": `
services:
  a:
    image: a
    networks: [mesh]
  b:
    image: b
    deploy: {replicas: 3}
    networks: [mesh]
networks:
  mesh: {driver: bridge}
`,
		"host-local opt-out": `
services:
  a:
    image: a
    networks: [mesh]
  b:
    image: b
    networks: [mesh]
networks:
  mesh: {driver: bridge, driver_opts: {ipam: host-local}}
`,
	} {
		p, err := Load(writeCompose(t, body))
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		plans, err := p.Plan("proj")
		if err != nil {
			t.Fatalf("%s: Plan: %v", name, err)
		}
		for svc, plan := range plans {
			if plan.Spec.Networks[0].IP != "" {
				t.Errorf("%s: %s got pinned IP %q, want dynamic addressing", name, svc, plan.Spec.Networks[0].IP)
			}
			if plan.Spec.DNS != nil {
				t.Errorf("%s: %s got DNS %+v, want none", name, svc, plan.Spec.DNS)
			}
		}
	}
}

// TestUserNetProxySkipsDNS: a proxied service (the enforcing proxy cannot share
// a pod with the DNS caretaker) still gets its pinned address — peers resolve
// and reach it over the user network — but no DNSSpec of its own.
func TestUserNetProxySkipsDNS(t *testing.T) {
	file := writeCompose(t, `
services:
  a:
    image: a
    networks: [mesh]
  b:
    image: b
    networks: [mesh]
networks:
  mesh:
    driver: bridge
    driver_opts:
      proxy: "true"
`)
	p, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := p.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for svc, plan := range plans {
		if plan.Spec.Proxy == nil {
			t.Fatalf("%s: want a ProxySpec (proxy-enabled network)", svc)
		}
		if plan.Spec.DNS != nil {
			t.Errorf("%s: DNS = %+v, must not coexist with the proxy", svc, plan.Spec.DNS)
		}
		if plan.Spec.Networks[0].IP == "" {
			t.Errorf("%s: want a pinned IP even when proxied", svc)
		}
	}
}

// TestStaticIPForBounds exercises the allocator primitive on a tiny /29: every
// usable host slot (5 of them: 8 minus network/gateway/broadcast) is handed
// out exactly once, and the next request fails rather than reusing or stepping
// outside the range.
func TestStaticIPForBounds(t *testing.T) {
	subnet := netip.MustParsePrefix("192.168.7.0/29")
	taken := map[netip.Addr]string{}
	seen := map[netip.Addr]bool{}
	for i := 0; i < 5; i++ {
		addr, err := staticIPFor("svc-"+strconv.Itoa(i), subnet, taken)
		if err != nil {
			t.Fatalf("allocation %d: %v", i, err)
		}
		if !subnet.Contains(addr) {
			t.Fatalf("allocation %d = %s, outside %s", i, addr, subnet)
		}
		if s := addr.String(); s == "192.168.7.0" || s == "192.168.7.1" || s == "192.168.7.7" {
			t.Fatalf("allocation %d = %s, a reserved address", i, addr)
		}
		if seen[addr] {
			t.Fatalf("allocation %d = %s, already handed out", i, addr)
		}
		seen[addr] = true
		taken[addr] = "svc"
	}
	if _, err := staticIPFor("one-too-many", subnet, taken); err == nil || !strings.Contains(err.Error(), "no free host address") {
		t.Errorf("6th allocation on a /29 = %v, want a subnet-full error", err)
	}
}

// TestUserNetIgnoresPinOffMultus: an ipv4_address on a network no backend can
// statically address (the services driver here) is dropped from the plan with
// a warning — no backend consumes it, and a dangling pin must not leak into
// the spec (it would, for one, flip the kubernetes update strategy).
func TestUserNetIgnoresPinOffMultus(t *testing.T) {
	file := writeCompose(t, `
services:
  a:
    image: a
    networks:
      mesh: {ipv4_address: 10.99.0.5}
networks:
  mesh: {driver: services}
`)
	p, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var plans map[string]ServicePlan
	warnings := captureWarnings(t, func() {
		plans, err = p.Plan("proj")
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plans["a"].Spec.Networks[0].IP; got != "" {
		t.Errorf("pinned IP = %q, want it dropped on a non-Multus driver", got)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "ignoring ipv4_address") {
		t.Errorf("warnings = %v, want the ignored-ipv4_address warning", warnings)
	}
}
