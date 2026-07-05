package main

import (
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"cornus/pkg/api"
)

// TestBearerForRegistry confirms the push keychain hands the cornus bearer token
// only to the destination registry host and stays anonymous for every other
// host, so a cross-registry crane.Copy cannot leak the token to the source.
func TestBearerForRegistry(t *testing.T) {
	kc := &bearerForRegistry{registry: "localhost:5000", token: "sekret"}

	// Destination host: the bearer token is attached.
	destRef, err := name.ParseReference("localhost:5000/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kc.Resolve(destRef.Context())
	if err != nil {
		t.Fatalf("Resolve(dest) error: %v", err)
	}
	if b, ok := got.(*authn.Bearer); !ok || b.Token != "sekret" {
		t.Fatalf("Resolve(dest) = %#v, want *authn.Bearer{Token: \"sekret\"}", got)
	}

	// An unrelated source host: must stay anonymous (no token leak).
	srcRef, err := name.ParseReference("privateregistry.thirdparty.com/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err = kc.Resolve(srcRef.Context())
	if err != nil {
		t.Fatalf("Resolve(source) error: %v", err)
	}
	if got != authn.Anonymous {
		cfg, _ := got.Authorization()
		t.Fatalf("Resolve(source) = %#v (authz %#v), want authn.Anonymous", got, cfg)
	}
}

// TestDeployDetachFlagParse confirms --detach (and its -d short) binds onto
// DeployCmd alongside the existing flags.
func TestDeployDetachFlagParse(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli, kong.Name("cornus"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{
		"deploy", "-f", "spec.yaml", "--detach", "--server", "https://cornus.example:5000",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cli.Deploy.Detach {
		t.Error("Detach = false, want true")
	}
	if cli.Deploy.File != "spec.yaml" || cli.Deploy.Server != "https://cornus.example:5000" {
		t.Errorf("File = %q, Server = %q", cli.Deploy.File, cli.Deploy.Server)
	}

	cli = CLI{}
	parser, err = kong.New(&cli, kong.Name("cornus"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"deploy", "-f", "spec.yaml", "-d"}); err != nil {
		t.Fatalf("parse -d: %v", err)
	}
	if !cli.Deploy.Detach {
		t.Error("Detach (via -d) = false, want true")
	}
}

// TestCheckDetachable confirms specs with client-local bind mounts — which only
// a live attach session can serve over 9P — are rejected for --detach, while
// named-volume mounts (server-host sources) pass.
func TestCheckDetachable(t *testing.T) {
	err := checkDetachable(api.DeploySpec{
		Name:   "shop-web",
		Mounts: []api.Mount{{Source: "./src", Target: "/app"}},
	})
	if err == nil {
		t.Fatal("checkDetachable(local mount) = nil, want error")
	}
	if !strings.Contains(err.Error(), "--detach") || !strings.Contains(err.Error(), "./src") {
		t.Errorf("error = %q, want it to name --detach and the offending source", err)
	}

	if err := checkDetachable(api.DeploySpec{
		Name:   "shop-web",
		Mounts: []api.Mount{{Source: "named-vol", Target: "/cache"}},
	}); err != nil {
		t.Errorf("checkDetachable(named volume) = %v, want nil", err)
	}
	if err := checkDetachable(api.DeploySpec{Name: "shop-web"}); err != nil {
		t.Errorf("checkDetachable(no mounts) = %v, want nil", err)
	}
}

func TestCheckDetachableEgress(t *testing.T) {
	// env mode always detaches.
	if err := checkDetachable(api.DeploySpec{Name: "x", Egress: &api.EgressSpec{Mode: "env"}}); err != nil {
		t.Errorf("env-mode egress detach = %v, want nil", err)
	}
	// A relay mode routing to the gateway detaches (durable egress node).
	if err := checkDetachable(api.DeploySpec{Name: "x", Egress: &api.EgressSpec{Mode: "proxy", Default: "gateway"}}); err != nil {
		t.Errorf("gateway-routed egress detach = %v, want nil", err)
	}
	// A relay mode that could route to the client cannot detach (needs a session).
	if err := checkDetachable(api.DeploySpec{Name: "x", Egress: &api.EgressSpec{Mode: "proxy", Default: "client"}}); err == nil {
		t.Error("client-routed egress detach = nil, want an error")
	}
	if err := checkDetachable(api.DeploySpec{Name: "x", Egress: &api.EgressSpec{
		Mode:  "transparent",
		Rules: []api.EgressRule{{Pattern: "*.corp", Route: "client"}},
	}}); err == nil {
		t.Error("egress with a client rule detach = nil, want an error")
	}
	// A script policy is conservatively treated as possibly client-routing.
	if err := checkDetachable(api.DeploySpec{Name: "x", Egress: &api.EgressSpec{
		Mode:   "proxy",
		Script: "function FindProxyForURL(u,h){return 'DIRECT'}",
	}}); err == nil {
		t.Error("script egress detach = nil, want an error (conservative)")
	}
}

func TestCheckDetachedConduitOptions(t *testing.T) {
	cases := []struct {
		name string
		set  func(*DeployCmd)
	}{
		{name: "conduit", set: func(c *DeployCmd) { c.Conduit = "socks5" }},
		{name: "ingress-conduit", set: func(c *DeployCmd) { c.IngressConduit = "native" }},
		{name: "ingress-controller", set: func(c *DeployCmd) { c.IngressController = "ns/svc" }},
		{name: "ingress-emulate-ca", set: func(c *DeployCmd) { c.IngressEmulateCA = "ca.pem" }},
		{name: "ingress-emulate-ca-key", set: func(c *DeployCmd) { c.IngressEmulateCAKey = "ca.key" }},
	}
	if err := checkDetachedConduitOptions(&DeployCmd{}); err != nil {
		t.Fatalf("empty options: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &DeployCmd{}
			tc.set(c)
			err := checkDetachedConduitOptions(c)
			if err == nil || !strings.Contains(err.Error(), "--detach") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
