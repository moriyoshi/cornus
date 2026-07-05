package egressflags

import (
	"os"
	"path/filepath"
	"testing"

	"cornus/pkg/api"
)

func TestApplyBuildsSpec(t *testing.T) {
	f := Flags{
		Mode:    "transparent",
		Default: "cluster",
		Route:   []string{"*.internal=cluster", "0.0.0.0/0=client", "bad.example.com=deny"},
	}
	var spec api.DeploySpec
	if err := f.Apply(&spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	e := spec.Egress
	if e == nil || e.Mode != "transparent" || e.Default != "cluster" {
		t.Fatalf("egress = %+v", e)
	}
	if len(e.Rules) != 3 || e.Rules[1].Route != "client" {
		t.Fatalf("rules = %+v", e.Rules)
	}
}

func TestApplyNoFlagsNoop(t *testing.T) {
	var spec api.DeploySpec
	if err := (Flags{}).Apply(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Egress != nil {
		t.Fatalf("expected no egress, got %+v", spec.Egress)
	}
}

func TestApplyOverridesSpecFile(t *testing.T) {
	spec := api.DeploySpec{Egress: &api.EgressSpec{Mode: "env", Default: "deny"}}
	f := Flags{Mode: "proxy"}
	if err := f.Apply(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Egress.Mode != "proxy" {
		t.Fatalf("mode = %q, want proxy (flag overrides file)", spec.Egress.Mode)
	}
	if spec.Egress.Default != "deny" {
		t.Fatalf("default = %q, want deny (untouched by flags)", spec.Egress.Default)
	}
}

func TestApplyErrors(t *testing.T) {
	cases := []Flags{
		{Mode: "bogus"},
		{Default: "bogus"},
		{Route: []string{"pattern-no-equals"}},
		{Route: []string{"host=bogusroute"}},
		{Route: []string{"=client"}},
		{Route: []string{"10.0.0.0/999=client"}}, // compile failure
	}
	for i, f := range cases {
		var spec api.DeploySpec
		if err := f.Apply(&spec); err == nil {
			t.Errorf("case %d (%+v): expected error", i, f)
		}
	}
}

// A distinct gateway URL is reserved for a future release: Apply must reject one
// that arrives via the spec file, even when the flags themselves are innocuous.
func TestApplyRejectsSpecFileGateway(t *testing.T) {
	spec := api.DeploySpec{Egress: &api.EgressSpec{Mode: "transparent", Gateway: "wss://gw/api"}}
	if err := (Flags{Mode: "proxy"}).Apply(&spec); err == nil {
		t.Fatal("expected error for a non-empty egress gateway URL")
	}
}

func TestApplyPACFile(t *testing.T) {
	dir := t.TempDir()
	pac := filepath.Join(dir, "proxy.pac")
	if err := os.WriteFile(pac, []byte("function FindProxyForURL(u,h){return 'DIRECT'}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Flags{Mode: "transparent", PAC: pac}
	var spec api.DeploySpec
	if err := f.Apply(&spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if spec.Egress.Script == "" {
		t.Fatal("script not loaded from PAC file")
	}
}

func TestApplyPACMissingFile(t *testing.T) {
	f := Flags{PAC: "/no/such/file.pac"}
	var spec api.DeploySpec
	if err := f.Apply(&spec); err == nil {
		t.Fatal("expected error for missing PAC file")
	}
}
