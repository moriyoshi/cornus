package compose

import (
	"reflect"
	"strings"
	"testing"
)

// x-cornus-shells carries a PREFERENCE ORDER, which is what separates these tests
// from the structural drift tests in service_fields_drift_test.go. Those prove the
// key is carried at all; these prove the three things order makes load-bearing: a
// service list REPLACES the project default rather than ranking after it, an
// override file REPLACES rather than concatenates, and a multi-word entry survives
// as one entry (splitting happens once, in the consumer, not here).

func TestServiceShellsReachThePlanInOrder(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-shells:
      - /bin/zsh
      - /bin/busybox sh
      - /bin/sh
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// The whole slice, not membership: order IS the meaning of this field, and a
	// set-comparison would pass for an implementation that sorted or reversed it.
	want := []string{"/bin/zsh", "/bin/busybox sh", "/bin/sh"}
	if got := plans["web"].Shells; !reflect.DeepEqual(got, want) {
		t.Errorf("plan.Shells = %q, want %q", got, want)
	}
}

func TestServiceShellsAcceptTheScalarForm(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-shells: /bin/bash
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"/bin/bash"}
	if got := plans["web"].Shells; !reflect.DeepEqual(got, want) {
		t.Errorf("plan.Shells = %q, want %q", got, want)
	}
}

func TestNoShellsLeavesThePlanEmpty(t *testing.T) {
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
	// An empty list must stay empty rather than acquire a built-in default here:
	// the candidate list is assembled by the consumer, and a default injected at
	// plan time would outrank the connection context and the browser's own list.
	// It must also stay off api.DeploySpec — see ServicePlan.Shells for why.
	if got := plans["web"].Shells; len(got) != 0 {
		t.Errorf("plan.Shells = %q, want empty", got)
	}
}

func TestProjectShellsDefaultIsInheritedAndFullyOverridden(t *testing.T) {
	file := writeCompose(t, `
name: proj
x-cornus-shells:
  - /bin/ash
  - /bin/sh
services:
  inherits:
    image: a:latest
  overrides:
    image: b:latest
    x-cornus-shells:
      - /bin/bash
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	wantInherited := []string{"/bin/ash", "/bin/sh"}
	if got := plans["inherits"].Shells; !reflect.DeepEqual(got, wantInherited) {
		t.Errorf("inherited Shells = %q, want %q", got, wantInherited)
	}
	// FULLY overrides: the project entries must not survive as a lower-ranked tail.
	// Asserting the whole slice is the point — a concatenating implementation would
	// still contain /bin/bash and still "work", just with the wrong preference.
	wantOverridden := []string{"/bin/bash"}
	if got := plans["overrides"].Shells; !reflect.DeepEqual(got, wantOverridden) {
		t.Errorf("overriding Shells = %q, want %q", got, wantOverridden)
	}
}

func TestProjectShellsDefaultIsCopiedPerService(t *testing.T) {
	file := writeCompose(t, `
name: proj
x-cornus-shells:
  - /bin/ash
  - /bin/sh
services:
  a:
    image: a:latest
  b:
    image: b:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Every inheriting service must get its OWN backing array. Handing each plan the
	// same slice header would let one caller's in-place edit rewrite the other's
	// preference order — silently, since both lists are still the right length.
	plans["a"].Shells[0] = "/bin/tampered"
	if got := plans["b"].Shells[0]; got != "/bin/ash" {
		t.Errorf("service b's first shell = %q after editing service a's; the project default is shared, not copied", got)
	}
}

func TestOverrideFileReplacesShellsRatherThanConcatenating(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
name: proj
services:
  web:
    image: base/web:v1
    x-cornus-shells:
      - /bin/zsh
      - /bin/sh
`,
		"override.yaml": `
services:
  web:
    x-cornus-shells:
      - /bin/bash
`,
	})
	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := p.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// mergeSeq (the additive-sequence rule used for ports/volumes/env_file) would
	// yield ["/bin/zsh","/bin/sh","/bin/bash"] here — every entry present, the
	// override ranked LAST, i.e. exactly backwards. Hence the whole-slice assert.
	want := []string{"/bin/bash"}
	if got := plans["web"].Shells; !reflect.DeepEqual(got, want) {
		t.Errorf("merged Shells = %q, want %q", got, want)
	}
}

func TestOverrideFileWithoutShellsKeepsTheBaseList(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
name: proj
services:
  web:
    image: base/web:v1
    x-cornus-shells:
      - /bin/zsh
`,
		"override.yaml": `
services:
  web:
    image: base/web:v2
`,
	})
	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := p.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"/bin/zsh"}
	if got := plans["web"].Shells; !reflect.DeepEqual(got, want) {
		t.Errorf("merged Shells = %q, want %q", got, want)
	}
}

// A later file's PROJECT-level block wins wholesale, matching how the project-level
// egress/ingress/telemetry blocks merge across -f files.
func TestLaterFileWinsTheProjectShellList(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
name: proj
x-cornus-shells:
  - /bin/zsh
services:
  web:
    image: base/web:v1
`,
		"override.yaml": `
x-cornus-shells:
  - /bin/bash
  - /bin/sh
`,
	})
	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := p.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"/bin/bash", "/bin/sh"}
	if got := plans["web"].Shells; !reflect.DeepEqual(got, want) {
		t.Errorf("merged project Shells = %q, want %q", got, want)
	}
}

// x-cornus-shells must be in supportedServiceFields, or a user who writes it gets a
// "field is not supported and was ignored" warning for a field that DID take effect
// — the most confusing possible outcome. logLines captures the load-time warnings.
func TestShellsIsNotWarnedAsUnsupported(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-shells:
      - /bin/bash
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, l := range lines {
		if strings.Contains(l, "x-cornus-shells") {
			t.Errorf("load warned about a supported field: %s", l)
		}
	}
}
