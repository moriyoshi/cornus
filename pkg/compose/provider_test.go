package compose

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestProviderParse asserts a `provider:` service parses into a ServicePlan with
// a ProviderPlan (type + deterministic, sorted option flags) and no deploy spec.
func TestProviderParse(t *testing.T) {
	file := writeCompose(t, `
name: shop
services:
  database:
    provider:
      type: awesomecloud
      options:
        type: mysql
        version: "8"
        zones:
          - us-east-1
          - us-west-2
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("shop")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan := plans["database"]
	if plan.Provider == nil {
		t.Fatalf("database plan has no Provider")
	}
	if plan.Provider.Type != "awesomecloud" {
		t.Errorf("Provider.Type = %q, want awesomecloud", plan.Provider.Type)
	}
	// Options are sorted by key at decode time (type < version < zones); a list
	// value becomes one flag per element in order.
	want := []string{"--type=mysql", "--version=8", "--zones=us-east-1", "--zones=us-west-2"}
	if !reflect.DeepEqual(plan.Provider.Flags, want) {
		t.Errorf("Flags = %v, want %v", plan.Provider.Flags, want)
	}
	if plan.Spec.Image != "" || plan.Build != nil {
		t.Errorf("provider service should have no image/build, got image=%q build=%v", plan.Spec.Image, plan.Build)
	}
}

// TestProviderNoWarning asserts a `provider:` block is recognized (no
// "unsupported field" warning path): it is in supportedServiceFields.
func TestProviderInSupportedFields(t *testing.T) {
	if _, ok := supportedServiceFields["provider"]; !ok {
		t.Error(`"provider" missing from supportedServiceFields`)
	}
}

// TestProviderMutualExclusion asserts provider cannot be combined with
// image/build/deploy, and that type is required.
func TestProviderMutualExclusion(t *testing.T) {
	cases := []struct {
		name    string
		compose string
		wantErr string
	}{
		{
			name: "with image",
			compose: `
services:
  db:
    image: postgres
    provider:
      type: awesomecloud
`,
			wantErr: "cannot be combined",
		},
		{
			name: "with build",
			compose: `
services:
  db:
    build: .
    provider:
      type: awesomecloud
`,
			wantErr: "cannot be combined",
		},
		{
			name: "missing type",
			compose: `
services:
  db:
    provider:
      options:
        foo: bar
`,
			wantErr: "provider.type is required",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := writeCompose(t, c.compose)
			proj, err := Load(file)
			if err != nil {
				// Some invalid shapes may fail at Load; that is acceptable too.
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("Load error = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			_, err = proj.Plan("proj")
			if err == nil {
				t.Fatalf("Plan succeeded, want error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Plan error = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

// TestProviderScalarOption asserts a scalar (non-string) option value stringifies
// without a trailing ".0".
func TestProviderScalarOption(t *testing.T) {
	file := writeCompose(t, `
services:
  cache:
    provider:
      type: awesomecloud
      options:
        port: 6379
        tls: true
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"--port=6379", "--tls=true"}
	if got := plans["cache"].Provider.Flags; !reflect.DeepEqual(got, want) {
		t.Errorf("Flags = %v, want %v", got, want)
	}
}

// TestProviderConfigRoundTrip asserts the provider block marshals back to its
// Compose shape (`type` + `options` map) rather than the internal flattened
// slice, so `cornus compose config` renders it faithfully.
func TestProviderConfigRoundTrip(t *testing.T) {
	p := &Provider{
		Type: "awesomecloud",
		Options: ProviderOptions{
			{Key: "type", Values: []string{"mysql"}},
			{Key: "zones", Values: []string{"a", "b"}},
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["type"] != "awesomecloud" {
		t.Errorf("type = %v, want awesomecloud", got["type"])
	}
	opts, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("options is %T, want object", got["options"])
	}
	if opts["type"] != "mysql" {
		t.Errorf("options.type = %v, want mysql (scalar)", opts["type"])
	}
	zones, ok := opts["zones"].([]any)
	if !ok || len(zones) != 2 || zones[0] != "a" || zones[1] != "b" {
		t.Errorf("options.zones = %v, want [a b]", opts["zones"])
	}
}
