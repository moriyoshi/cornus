package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/obsstore"
	"cornus/pkg/otelcollector"
)

// TestResolveObsEnabled pins the tri-state --obs contract. The released binaries
// and the published image all link the store in, so an unspecified --obs must
// mean ON there; a plain `go build` binary compiles the stub, where it must mean
// OFF (silently — see openObsStore). Only an explicit flag overrides that.
func TestResolveObsEnabled(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name     string
		flag     *bool
		compiled bool
		want     bool
	}{
		{"unset follows a store-carrying build", nil, true, true},
		{"unset follows a stub build", nil, false, false},
		{"explicit --obs wins over a stub build", &yes, false, true},
		{"explicit --obs on a real build", &yes, true, true},
		{"explicit --no-obs wins over a store-carrying build", &no, true, false},
		{"explicit --no-obs on a stub build", &no, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveObsEnabled(tc.flag, tc.compiled); got != tc.want {
				t.Errorf("resolveObsEnabled(%v, %v) = %v, want %v", tc.flag, tc.compiled, got, tc.want)
			}
		})
	}
}

// TestResolveObsEnabledDefaultMatchesThisBuild is the regression guard that
// matters for shipping: whatever tags this test binary was built with, an
// unspecified --obs must agree with obsstore.Compiled(). It fails if someone
// reintroduces a hardcoded default, in either build flavor.
func TestResolveObsEnabledDefaultMatchesThisBuild(t *testing.T) {
	if got, want := resolveObsEnabled(nil, obsstore.Compiled()), obsstore.Compiled(); got != want {
		t.Errorf("unspecified --obs = %v, want %v (obsstore.Compiled())", got, want)
	}
}

// runVersionFeatures invokes `version --features` with the given --output mode,
// capturing stdout.
func runVersionFeatures(t *testing.T, output string) string {
	t.Helper()
	var buf bytes.Buffer
	cli := &CLI{drv: cliout.New(cliout.Options{Stdout: &buf, Output: output})}
	if err := (&VersionCmd{Features: true}).Run(cli); err != nil {
		t.Fatalf("version --features: %v", err)
	}
	return buf.String()
}

// TestVersionFeaturesJSONContract pins the exact JSON shape of
// `version --features --output json`. The Dockerfile build stage and the release
// workflow both GREP this output to refuse shipping an artifact whose
// observability store silently no-oped, so the key names and the yes/no
// vocabulary are a build-pipeline contract, not just display text.
func TestVersionFeaturesJSONContract(t *testing.T) {
	var got map[string]string
	out := runVersionFeatures(t, "json")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a JSON object: %v (got %q)", err, out)
	}
	want := map[string]string{
		"version":       version,
		"obsstore":      yesNo(obsstore.Compiled()),
		"otelcollector": yesNo(otelcollector.Compiled()),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("key %q = %q, want %q", k, got[k], w)
		}
	}
	// The pipeline greps for the literal `"obsstore":"yes"`, so a compact object
	// (no spaces after the colon) is part of the contract too.
	if obsstore.Compiled() && !strings.Contains(out, `"obsstore":"yes"`) {
		t.Errorf("release assertion substring `\"obsstore\":\"yes\"` absent from %q", out)
	}
}

// TestVersionWithoutFeaturesStaysBare guards the default `cornus version`
// output: scripts parse it as a lone version string, so --features must be the
// only thing that adds lines.
func TestVersionWithoutFeaturesStaysBare(t *testing.T) {
	var buf bytes.Buffer
	cli := &CLI{drv: cliout.New(cliout.Options{Stdout: &buf, Output: "plain"})}
	if err := (&VersionCmd{}).Run(cli); err != nil {
		t.Fatalf("version: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != version {
		t.Errorf("bare version output = %q, want %q", got, version)
	}
}
