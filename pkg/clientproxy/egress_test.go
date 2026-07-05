package clientproxy

import (
	"testing"

	"cornus/pkg/api"
)

func TestApplyEgressEnvExplicitProxies(t *testing.T) {
	spec := &api.DeploySpec{
		Egress: &api.EgressSpec{
			Mode:    "env",
			Proxies: map[string]string{"ALL_PROXY": "socks5h://corp:1080"},
			Rules: []api.EgressRule{
				{Pattern: "*.internal", Route: "cluster"},
				{Pattern: "0.0.0.0/0", Route: "client"}, // not a cluster route => not in NO_PROXY
			},
		},
	}
	if err := ApplyEgressEnv(spec); err != nil {
		t.Fatal(err)
	}
	if spec.Env["ALL_PROXY"] != "socks5h://corp:1080" {
		t.Fatalf("ALL_PROXY = %q (scheme must be preserved)", spec.Env["ALL_PROXY"])
	}
	if spec.Env["NO_PROXY"] != "*.internal" || spec.Env["no_proxy"] != "*.internal" {
		t.Fatalf("NO_PROXY = %q/%q, want *.internal", spec.Env["NO_PROXY"], spec.Env["no_proxy"])
	}
}

func TestApplyEgressEnvResolvesFromEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://envproxy:3128")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("NO_PROXY", "")
	spec := &api.DeploySpec{Egress: &api.EgressSpec{Mode: "env"}}
	if err := ApplyEgressEnv(spec); err != nil {
		t.Fatal(err)
	}
	if spec.Env["HTTP_PROXY"] != "http://envproxy:3128" {
		t.Fatalf("HTTP_PROXY = %q", spec.Env["HTTP_PROXY"])
	}
}

func TestApplyEgressEnvUserEnvWins(t *testing.T) {
	spec := &api.DeploySpec{
		Env: map[string]string{"HTTP_PROXY": "http://user-set:9999"},
		Egress: &api.EgressSpec{
			Mode:    "env",
			Proxies: map[string]string{"HTTP_PROXY": "http://injected:8080"},
		},
	}
	if err := ApplyEgressEnv(spec); err != nil {
		t.Fatal(err)
	}
	if spec.Env["HTTP_PROXY"] != "http://user-set:9999" {
		t.Fatalf("user-set HTTP_PROXY overwritten: %q", spec.Env["HTTP_PROXY"])
	}
}

func TestApplyEgressEnvNonEnvModeNoop(t *testing.T) {
	spec := &api.DeploySpec{
		Egress: &api.EgressSpec{Mode: "transparent", Proxies: map[string]string{"HTTP_PROXY": "http://x"}},
	}
	if err := ApplyEgressEnv(spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Env) != 0 {
		t.Fatalf("transparent mode must not inject env, got %v", spec.Env)
	}
}

func TestApplyEgressEnvNilSpec(t *testing.T) {
	if err := ApplyEgressEnv(&api.DeploySpec{}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEgressEnv(nil); err != nil {
		t.Fatal(err)
	}
}
