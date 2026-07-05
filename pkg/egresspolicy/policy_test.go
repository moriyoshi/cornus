package egresspolicy

import (
	"net"
	"testing"

	"cornus/pkg/api"
)

func mustCompile(t *testing.T, spec api.EgressSpec) Policy {
	t.Helper()
	p, err := Compile(spec)
	if err != nil {
		t.Fatalf("Compile(%+v): %v", spec, err)
	}
	return p
}

func route(t *testing.T, p Policy, d Dest) string {
	t.Helper()
	r, err := p.Route(d)
	if err != nil {
		t.Fatalf("Route(%+v): %v", d, err)
	}
	return r
}

func TestRulePolicyRouting(t *testing.T) {
	spec := api.EgressSpec{
		Rules: []api.EgressRule{
			{Pattern: "*.internal", Route: RouteCluster},
			{Pattern: "10.0.0.0/8", Route: RouteCluster},
			{Pattern: "secret.example.com", Route: RouteDeny},
			{Pattern: "api.example.com:443", Route: RouteClient},
			{Pattern: "api.example.com", Route: RouteGateway}, // lower priority, port-less
		},
		Default: RouteClient,
	}
	p := mustCompile(t, spec)

	cases := []struct {
		name string
		dest Dest
		want string
	}{
		{"subdomain glob", Dest{Host: "db.internal", Port: 5432}, RouteCluster},
		{"nested subdomain glob", Dest{Host: "a.b.internal", Port: 80}, RouteCluster},
		{"cidr by ip", Dest{IP: net.ParseIP("10.1.2.3"), Port: 22}, RouteCluster},
		{"cidr by host-ip", Dest{Host: "10.9.9.9", Port: 22}, RouteCluster},
		{"deny host", Dest{Host: "secret.example.com", Port: 443}, RouteDeny},
		{"port-specific first match", Dest{Host: "api.example.com", Port: 443}, RouteClient},
		{"port-less fallthrough", Dest{Host: "api.example.com", Port: 80}, RouteGateway},
		{"default", Dest{Host: "random.org", Port: 443}, RouteClient},
		{"case-insensitive host", Dest{Host: "DB.INTERNAL", Port: 1}, RouteCluster},
		{"trailing dot host", Dest{Host: "db.internal.", Port: 1}, RouteCluster},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := route(t, p, c.dest); got != c.want {
				t.Fatalf("route = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDefaultRoute(t *testing.T) {
	// Unset default => cluster.
	p := mustCompile(t, api.EgressSpec{})
	if got := route(t, p, Dest{Host: "anything", Port: 80}); got != RouteCluster {
		t.Fatalf("empty-default route = %q, want %q", got, RouteCluster)
	}
	// Explicit deny default.
	p = mustCompile(t, api.EgressSpec{Default: RouteDeny})
	if got := route(t, p, Dest{Host: "anything", Port: 80}); got != RouteDeny {
		t.Fatalf("deny-default route = %q, want %q", got, RouteDeny)
	}
}

func TestBareWildcard(t *testing.T) {
	p := mustCompile(t, api.EgressSpec{
		Rules:   []api.EgressRule{{Pattern: "*", Route: RouteClient}},
		Default: RouteCluster,
	})
	if got := route(t, p, Dest{Host: "example.com", Port: 443}); got != RouteClient {
		t.Fatalf("wildcard route = %q, want %q", got, RouteClient)
	}
	if got := route(t, p, Dest{IP: net.ParseIP("1.2.3.4"), Port: 443}); got != RouteClient {
		t.Fatalf("wildcard route (ip) = %q, want %q", got, RouteClient)
	}
}

func TestIPv6BracketedPort(t *testing.T) {
	p := mustCompile(t, api.EgressSpec{
		Rules: []api.EgressRule{
			{Pattern: "[fe80::/10]:443", Route: RouteDeny},
			{Pattern: "fe80::/10", Route: RouteCluster},
		},
	})
	if got := route(t, p, Dest{IP: net.ParseIP("fe80::1"), Port: 443}); got != RouteDeny {
		t.Fatalf("ipv6 :443 route = %q, want %q", got, RouteDeny)
	}
	if got := route(t, p, Dest{IP: net.ParseIP("fe80::1"), Port: 80}); got != RouteCluster {
		t.Fatalf("ipv6 :80 route = %q, want %q", got, RouteCluster)
	}
}

func TestExactIP(t *testing.T) {
	p := mustCompile(t, api.EgressSpec{
		Rules: []api.EgressRule{{Pattern: "192.168.1.1", Route: RouteDeny}},
	})
	if got := route(t, p, Dest{IP: net.ParseIP("192.168.1.1"), Port: 80}); got != RouteDeny {
		t.Fatalf("exact ip route = %q, want %q", got, RouteDeny)
	}
	if got := route(t, p, Dest{IP: net.ParseIP("192.168.1.2"), Port: 80}); got != RouteCluster {
		t.Fatalf("non-match ip route = %q, want %q", got, RouteCluster)
	}
}

func TestCompileErrors(t *testing.T) {
	cases := []api.EgressSpec{
		{Rules: []api.EgressRule{{Pattern: "x", Route: "bogus"}}},
		{Rules: []api.EgressRule{{Pattern: "10.0.0.0/999", Route: RouteClient}}},
		{Rules: []api.EgressRule{{Pattern: "host:notaport", Route: RouteClient}}},
		{Rules: []api.EgressRule{{Pattern: "host:70000", Route: RouteClient}}},
		{Rules: []api.EgressRule{{Pattern: "", Route: RouteClient}}},
		{Default: "bogus"},
	}
	for i, spec := range cases {
		if _, err := Compile(spec); err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		}
	}
}

func TestScriptCompiles(t *testing.T) {
	// A valid PAC script now compiles (the sobek evaluator is linked).
	if _, err := Compile(api.EgressSpec{Script: "function FindProxyForURL(u,h){return 'DIRECT'}"}); err != nil {
		t.Fatalf("valid script should compile: %v", err)
	}
}

func TestNoProxyPatterns(t *testing.T) {
	spec := api.EgressSpec{Rules: []api.EgressRule{
		{Pattern: "*.internal", Route: RouteCluster},
		{Pattern: "10.0.0.0/8", Route: RouteCluster},
		{Pattern: "api.example.com", Route: RouteClient},
		{Pattern: "bad.example.com", Route: RouteDeny},
	}}
	got := NoProxyPatterns(spec)
	want := []string{"*.internal", "10.0.0.0/8"}
	if len(got) != len(want) {
		t.Fatalf("NoProxyPatterns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NoProxyPatterns[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
