package egresspolicy

import (
	"net"
	"sync"
	"testing"

	"cornus/pkg/api"
)

func scriptRoute(t *testing.T, script string, d Dest) string {
	t.Helper()
	p, err := Compile(api.EgressSpec{Script: script, Default: RouteCluster})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r, err := p.Route(d)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return r
}

func TestScriptReturnMapping(t *testing.T) {
	cases := []struct {
		ret  string
		want string
	}{
		{"DIRECT", RouteCluster},
		{"DENY", RouteDeny},
		{"BLOCK", RouteDeny},
		{"CLIENT", RouteClient},
		{"CLUSTER", RouteCluster},
		{"GATEWAY", RouteGateway},
		{"PROXY client", RouteClient},
		{"PROXY gateway", RouteGateway},
		{"PROXY cluster", RouteCluster},
		{"PROXY proxy.corp:8080", RouteClient},     // real proxy host => via client
		{"PROXY p.corp:8080; DIRECT", RouteClient}, // first directive wins
		{"SOCKS5 socks.corp:1080", RouteClient},    // socks proxy => via client
		{"", RouteCluster},                         // empty => default
		{"WEIRD unknown", RouteCluster},            // unknown => default
	}
	for _, c := range cases {
		script := "function FindProxyForURL(url, host){ return '" + c.ret + "'; }"
		if got := scriptRoute(t, script, Dest{Host: "x.example.com", Port: 443}); got != c.want {
			t.Errorf("return %q => %q, want %q", c.ret, got, c.want)
		}
	}
}

func TestScriptNullReturnUsesDefault(t *testing.T) {
	got := scriptRoute(t, "function FindProxyForURL(url, host){ return null; }", Dest{Host: "x", Port: 1})
	if got != RouteCluster {
		t.Fatalf("null return => %q, want default cluster", got)
	}
}

func TestScriptPACBuiltins(t *testing.T) {
	script := `
function FindProxyForURL(url, host) {
  if (isPlainHostName(host)) return "CLUSTER";
  if (dnsDomainIs(host, ".internal")) return "CLUSTER";
  if (shExpMatch(host, "*.blocked.com")) return "DENY";
  if (isInNet(host, "10.0.0.0", "255.0.0.0")) return "CLUSTER";
  return "CLIENT";
}`
	cases := []struct {
		host string
		ip   string
		port int
		want string
	}{
		{"plainname", "", 80, RouteCluster},
		{"db.internal", "", 5432, RouteCluster},
		{"ads.blocked.com", "", 443, RouteDeny},
		{"host.example.com", "10.1.2.3", 443, RouteCluster}, // isInNet via known dest IP
		{"host.example.com", "8.8.8.8", 443, RouteClient},
	}
	for _, c := range cases {
		d := Dest{Host: c.host, Port: c.port}
		if c.ip != "" {
			d.IP = net.ParseIP(c.ip)
		}
		if got := scriptRoute(t, script, d); got != c.want {
			t.Errorf("host=%s ip=%s => %q, want %q", c.host, c.ip, got, c.want)
		}
	}
}

func TestScriptCompileErrors(t *testing.T) {
	// Syntax error.
	if _, err := Compile(api.EgressSpec{Script: "function ("}); err == nil {
		t.Error("expected a syntax error")
	}
	// No FindProxyForURL defined.
	if _, err := Compile(api.EgressSpec{Script: "var x = 1;"}); err == nil {
		t.Error("expected an error when FindProxyForURL is missing")
	}
}

func TestScriptTimeoutFailsClosed(t *testing.T) {
	// An infinite loop must be interrupted and fail closed to deny, not hang.
	p, err := Compile(api.EgressSpec{
		Script:  "function FindProxyForURL(url, host){ while(true){} }",
		Default: RouteClient, // even with a permissive default, a runaway denies
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	route, err := p.Route(Dest{Host: "x", Port: 1})
	if route != RouteDeny {
		t.Fatalf("runaway script route = %q (err=%v), want deny (fail closed)", route, err)
	}
}

func TestScriptExceptionFailsClosed(t *testing.T) {
	p, _ := Compile(api.EgressSpec{Script: "function FindProxyForURL(url, host){ throw new Error('boom'); }"})
	route, _ := p.Route(Dest{Host: "x", Port: 1})
	if route != RouteDeny {
		t.Fatalf("throwing script route = %q, want deny", route)
	}
}

func TestScriptConcurrentDeterministic(t *testing.T) {
	// The pooled runtimes must give identical verdicts under concurrency.
	script := `
function FindProxyForURL(url, host) {
  return dnsDomainIs(host, ".internal") ? "CLUSTER" : "CLIENT";
}`
	p, err := Compile(api.EgressSpec{Script: script})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := "svc.internal"
			want := RouteCluster
			if i%2 == 0 {
				host = "api.example.com"
				want = RouteClient
			}
			for j := 0; j < 20; j++ {
				got, err := p.Route(Dest{Host: host, Port: 443})
				if err != nil || got != want {
					t.Errorf("host=%s => %q (err=%v), want %q", host, got, err, want)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
