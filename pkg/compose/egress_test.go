package compose

import (
	"testing"
)

func TestTranslateEgress(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-egress:
      mode: transparent
      default: cluster
      listen_port: 15002
      proxies:
        ALL_PROXY: socks5h://corp:1080
      rules:
        - pattern: "*.internal"
          route: cluster
        - pattern: "0.0.0.0/0"
          route: client
        - pattern: "blocked.example.com"
          route: deny
      script: |
        function FindProxyForURL(url, host) { return "DIRECT"; }
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	e := plans["web"].Spec.Egress
	if e == nil {
		t.Fatal("Egress is nil")
	}
	if e.Mode != "transparent" || e.Default != "cluster" || e.ListenPort != 15002 {
		t.Fatalf("egress scalars = %+v", e)
	}
	if e.Proxies["ALL_PROXY"] != "socks5h://corp:1080" {
		t.Fatalf("proxies = %v (socks5h scheme must survive)", e.Proxies)
	}
	if len(e.Rules) != 3 {
		t.Fatalf("rules = %v", e.Rules)
	}
	if e.Rules[0].Pattern != "*.internal" || e.Rules[0].Route != "cluster" {
		t.Fatalf("rule0 = %+v", e.Rules[0])
	}
	if e.Rules[2].Route != "deny" {
		t.Fatalf("rule2 = %+v", e.Rules[2])
	}
	if e.Script == "" {
		t.Fatal("script not carried")
	}
}

// A distinct gateway node is reserved for a future release, so a compose
// `egress.gateway:` value must fail planning rather than be silently ignored.
func TestTranslateEgressGatewayRejected(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-egress:
      mode: transparent
      gateway: wss://gw.example/api
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("proj"); err == nil {
		t.Fatal("expected Plan to reject a non-empty egress gateway URL")
	}
}

// A project-level `egress:` block is the default for every service that declares no
// egress of its own; a service that does declare one fully overrides it.
func TestProjectEgressDefaultAndOverride(t *testing.T) {
	file := writeCompose(t, `
name: proj
x-cornus-egress:
  mode: proxy
  default: cluster
  rules:
    - pattern: "0.0.0.0/0"
      route: client
services:
  inherits:
    image: web:latest
  overrides:
    image: web:latest
    x-cornus-egress:
      mode: transparent
      default: deny
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	inh := plans["inherits"].Spec.Egress
	if inh == nil {
		t.Fatal("service with no egress: should inherit the project default")
	}
	if inh.Mode != "proxy" || inh.Default != "cluster" || len(inh.Rules) != 1 || inh.Rules[0].Route != "client" {
		t.Fatalf("inherited egress = %+v", inh)
	}

	ovr := plans["overrides"].Spec.Egress
	if ovr == nil || ovr.Mode != "transparent" || ovr.Default != "deny" || len(ovr.Rules) != 0 {
		t.Fatalf("service egress must FULLY override the project default, got %+v", ovr)
	}

	// The inherited spec must be a fresh copy, not aliased across services / the
	// project block: mutating one service's rules must not affect another's.
	inh.Rules[0].Route = "deny"
	if plans["overrides"].Spec.Egress.Default == "cluster" {
		t.Fatal("override service unexpectedly aliased the project default")
	}
}

// A malformed project-level egress default fails Plan up front, even if every
// service overrides it (so a typo in a shared default is caught).
func TestProjectEgressDefaultValidated(t *testing.T) {
	file := writeCompose(t, `
name: proj
x-cornus-egress:
  mode: proxy
  gateway: wss://gw.example/api
services:
  web:
    image: web:latest
    x-cornus-egress:
      mode: transparent
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("proj"); err == nil {
		t.Fatal("expected Plan to reject a malformed project-level egress default")
	}
}

func TestTranslateNoEgress(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plans["web"].Spec.Egress != nil {
		t.Fatal("Egress should be nil when unset")
	}
}
