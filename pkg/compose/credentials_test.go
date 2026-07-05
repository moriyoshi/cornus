package compose

import (
	"encoding/json"
	"strings"
	"testing"
)

// The tests below pin what `x-cornus-credentials:` actually does, as read off
// translateCredentials and the CredentialsDocument/CredentialDelivery
// unmarshalers: the two accepted block shapes, the camelCase aliases, the STRICT
// delivery-key check (the one place a compose file errors on an unknown key
// instead of warning), and the project-level default/override rule it shares
// with x-cornus-egress and x-cornus-telemetry.

// planFor loads a single compose file and returns the plan for one service.
func planFor(t *testing.T, service, content string) ServicePlan {
	t.Helper()
	project, err := Load(writeCompose(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan, ok := plans[service]
	if !ok {
		t.Fatalf("no plan for service %q", service)
	}
	return plan
}

// loadErr loads a compose file expecting a failure, and returns the error text.
// Validation runs in Plan (like egress/telemetry), so both stages are tried.
func loadErr(t *testing.T, content string) string {
	t.Helper()
	project, err := Load(writeCompose(t, content))
	if err != nil {
		return err.Error()
	}
	if _, err := project.Plan("proj"); err != nil {
		return err.Error()
	}
	t.Fatal("want an error, got none")
	return ""
}

func TestTranslateCredentials(t *testing.T) {
	spec := planFor(t, "agent", `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      sources:
        - name: claude
          backend: claude-code
          ttl: 5m
          config:
            var: ANTHROPIC_API_KEY
            duration: 3600
          deliveries:
            - kind: endpoint
              provider: anthropic-proxy
              upstream: https://my-gateway
            - kind: file
              path: /creds/claude.json
              format: json
            - kind: env
              env_var: API_KEY
              value_key: api_key
            - kind: endpoint
              provider: aws-imds
              well_known: true
`).Spec
	cs := spec.Credentials
	if cs == nil {
		t.Fatal("Credentials is nil")
	}
	if len(cs.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(cs.Sources))
	}
	src := cs.Sources[0]
	if src.Name != "claude" || src.Backend != "claude-code" || src.TTL != "5m" {
		t.Errorf("source scalars = %+v", src)
	}
	// A scalar config value is coerced to a string, so `duration: 3600` needs no
	// quoting (decodeKeyVals, shared with sysctls/labels).
	if src.Config["var"] != "ANTHROPIC_API_KEY" || src.Config["duration"] != "3600" {
		t.Errorf("config = %v", src.Config)
	}
	if len(src.Deliveries) != 4 {
		t.Fatalf("deliveries = %d entries, want 4", len(src.Deliveries))
	}
	if d := src.Deliveries[0]; d.Kind != "endpoint" || d.Provider != "anthropic-proxy" || d.Upstream != "https://my-gateway" {
		t.Errorf("deliveries[0] = %+v", d)
	}
	if d := src.Deliveries[1]; d.Kind != "file" || d.Path != "/creds/claude.json" || d.Format != "json" {
		t.Errorf("deliveries[1] = %+v", d)
	}
	// snake_case env_var / value_key land on the api's envVar / valueKey.
	if d := src.Deliveries[2]; d.Kind != "env" || d.EnvVar != "API_KEY" || d.ValueKey != "api_key" {
		t.Errorf("deliveries[2] = %+v", d)
	}
	if d := src.Deliveries[3]; !d.WellKnown {
		t.Errorf("deliveries[3] well_known = %+v", d)
	}
}

// TestCredentialsBareSequenceSugar asserts the wrapper-less form is the same
// block: `x-cornus-credentials:` may be the sources list itself.
func TestCredentialsBareSequenceSugar(t *testing.T) {
	spec := planFor(t, "agent", `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - name: claude
        backend: claude-code
        deliveries:
          - { kind: endpoint, provider: anthropic-proxy }
`).Spec
	cs := spec.Credentials
	if cs == nil || len(cs.Sources) != 1 {
		t.Fatalf("Credentials = %+v", cs)
	}
	if cs.Sources[0].Name != "claude" || cs.Sources[0].Deliveries[0].Provider != "anthropic-proxy" {
		t.Errorf("source = %+v", cs.Sources[0])
	}
}

// TestCredentialsDeliveryCamelCaseAliases asserts a `deliveries:` entry pasted out
// of a deploy spec (camelCase) is honoured rather than silently half-ignored.
func TestCredentialsDeliveryCamelCaseAliases(t *testing.T) {
	spec := planFor(t, "agent", `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - name: aws
        backend: aws-sts
        deliveries:
          - { kind: endpoint, provider: aws-imds, wellKnown: true }
          - { kind: env, envVar: API_KEY, valueKey: token }
`).Spec
	d := spec.Credentials.Sources[0].Deliveries
	if !d[0].WellKnown {
		t.Errorf("wellKnown alias dropped: %+v", d[0])
	}
	if d[1].EnvVar != "API_KEY" || d[1].ValueKey != "token" {
		t.Errorf("envVar/valueKey aliases dropped: %+v", d[1])
	}
}

// TestCredentialsDeliveryRejectsUnknownKey asserts a mistyped delivery key is an
// ERROR. Everywhere else in a compose file an unknown key is warned about and
// ignored; here that would deploy a workload that cannot read its credential.
func TestCredentialsDeliveryRejectsUnknownKey(t *testing.T) {
	msg := loadErr(t, `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - name: claude
        backend: claude-code
        deliveries:
          - { kind: file, path: /creds/c.json, formatt: json }
`)
	if !strings.Contains(msg, "formatt") {
		t.Errorf("error %q does not name the offending key", msg)
	}
}

// TestCredentialsSourceRejectsUnknownKey asserts an unknown key on a SOURCE is
// an error too, not just one inside a delivery entry. The motivating case is
// `deliver:` — the verb spelling of `deliveries:`, and the single most likely
// thing to type — which json would otherwise drop, yielding a source with no
// deliveries and a green deploy of a workload nothing can read the credential
// from. That is precisely the failure the strict delivery decoder exists to
// prevent, so the strictness has to start one level up.
func TestCredentialsSourceRejectsUnknownKey(t *testing.T) {
	for _, key := range []string{"deliver", "backends"} {
		t.Run(key, func(t *testing.T) {
			msg := loadErr(t, `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - name: claude
        backend: claude-code
        `+key+`:
          - { kind: endpoint, provider: anthropic-proxy }
`)
			if !strings.Contains(msg, key) {
				t.Errorf("error %q does not name the offending key %q", msg, key)
			}
		})
	}
}

func TestCredentialsValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		sources string
		want    string
	}{
		{
			name:    "missing name",
			sources: "        - backend: static\n",
			want:    "name is required",
		},
		{
			name:    "missing backend",
			sources: "        - name: db\n",
			want:    "backend is required",
		},
		{
			name: "duplicate name",
			sources: "        - {name: db, backend: static}\n" +
				"        - {name: db, backend: exec}\n",
			want: `duplicate name "db"`,
		},
		{
			name: "unknown delivery kind",
			sources: "        - name: db\n          backend: static\n" +
				"          deliveries: [{kind: socket}]\n",
			want: `unknown kind "socket"`,
		},
		{
			name: "file without path",
			sources: "        - name: db\n          backend: static\n" +
				"          deliveries: [{kind: file}]\n",
			want: "kind file needs a path",
		},
		{
			name: "env without env_var",
			sources: "        - name: db\n          backend: static\n" +
				"          deliveries: [{kind: env}]\n",
			want: "kind env needs env_var",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := loadErr(t, `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      sources:
`+tc.sources)
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.want)
			}
			// The message must say which service, so a multi-service project is
			// actionable.
			if !strings.Contains(msg, "agent") {
				t.Errorf("error = %q, want it to name the service", msg)
			}
		})
	}
}

// TestNoCredentials asserts a service with no block gets no CredentialSpec — the
// predicate that keeps it on the cheap stateless deploy path.
func TestNoCredentials(t *testing.T) {
	spec := planFor(t, "web", `
name: proj
services:
  web:
    image: web:latest
`).Spec
	if spec.Credentials != nil {
		t.Fatalf("Credentials = %+v, want nil", spec.Credentials)
	}
	if spec.Credentials.NeedsSession() {
		t.Error("a service with no credentials must not need a held session")
	}
}

// TestEmptyCredentialsBlockIsOff asserts an empty source list stays nil rather
// than becoming an empty spec: an empty spec would force a held deploy-attach
// session that brokers nothing.
func TestEmptyCredentialsBlockIsOff(t *testing.T) {
	spec := planFor(t, "web", `
name: proj
services:
  web:
    image: web:latest
    x-cornus-credentials:
      sources: []
`).Spec
	if spec.Credentials.NeedsSession() {
		t.Fatalf("Credentials = %+v, want no session", spec.Credentials)
	}
}

func TestProjectCredentialsDefaultAndOverride(t *testing.T) {
	project, err := Load(writeCompose(t, `
name: proj
x-cornus-credentials:
  - name: shared
    backend: static
    config: {value: s3cret}
services:
  inherits:
    image: a:latest
  overrides:
    image: b:latest
    x-cornus-credentials:
      - name: own
        backend: claude-code
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	inh := plans["inherits"].Spec.Credentials
	if inh == nil || len(inh.Sources) != 1 || inh.Sources[0].Name != "shared" {
		t.Fatalf("inherited credentials = %+v", inh)
	}
	// A service block fully overrides — the project source is not appended.
	ovr := plans["overrides"].Spec.Credentials
	if ovr == nil || len(ovr.Sources) != 1 || ovr.Sources[0].Name != "own" {
		t.Fatalf("overriding credentials = %+v", ovr)
	}
	// Each inheriting service gets its OWN config map: one plan's mutation must
	// not reach another's spec.
	inh.Sources[0].Config["value"] = "mutated"
	again, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := again["inherits"].Spec.Credentials.Sources[0].Config["value"]; got != "s3cret" {
		t.Errorf("config leaked between plans: %q", got)
	}
}

// TestProjectCredentialsValidatedOnce asserts an invalid project-level block
// fails the plan rather than being copied onto every service unchecked.
func TestProjectCredentialsValidatedOnce(t *testing.T) {
	msg := loadErr(t, `
name: proj
x-cornus-credentials:
  - name: shared
services:
  web:
    image: a:latest
`)
	if !strings.Contains(msg, "project credentials") || !strings.Contains(msg, "backend is required") {
		t.Errorf("error = %q", msg)
	}
}

// TestCredentialsMergeAcrossFiles asserts the block is cohesive across `-f`
// files: a later file's block REPLACES the earlier one wholesale, matching
// x-cornus-egress / x-cornus-telemetry. Without the mergeService entry the
// override would be silently dropped (mergeService opens with `out := base`).
func TestCredentialsMergeAcrossFiles(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - name: base
        backend: static
`,
		"override.yaml": `
services:
  agent:
    x-cornus-credentials:
      - name: overridden
        backend: claude-code
`,
	})
	project, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cs := plans["agent"].Spec.Credentials
	if cs == nil || len(cs.Sources) != 1 || cs.Sources[0].Name != "overridden" {
		t.Fatalf("merged credentials = %+v", cs)
	}
}

// TestCredentialsIsNotWarnedAsUnsupported asserts the key is recognized: it must
// not show up in the unsupported-field warnings the loader emits.
func TestCredentialsIsNotWarnedAsUnsupported(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - {name: claude, backend: claude-code}
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, l := range lines {
		if strings.Contains(l, "x-cornus-credentials") {
			t.Errorf("x-cornus-credentials warned as unsupported: %s", l)
		}
	}
}

// TestCredentialsRoundTripsThroughDocument asserts `compose config` re-renders
// the block (both levels) instead of dropping it. Both shapes normalize to the
// object form.
func TestCredentialsRoundTripsThroughDocument(t *testing.T) {
	project, err := Load(writeCompose(t, `
name: proj
x-cornus-credentials:
  - {name: shared, backend: static}
services:
  agent:
    image: agent:latest
    x-cornus-credentials:
      - {name: claude, backend: claude-code, deliveries: [{kind: endpoint, provider: anthropic-proxy}]}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	doc := project.View(nil).Document()
	if doc.Credentials == nil || len(doc.Credentials.Sources) != 1 || doc.Credentials.Sources[0].Name != "shared" {
		t.Errorf("project-level block lost: %+v", doc.Credentials)
	}
	svc := doc.Services["agent"].Credentials
	if svc == nil || len(svc.Sources) != 1 || svc.Sources[0].Deliveries[0].Provider != "anthropic-proxy" {
		t.Fatalf("service block lost: %+v", svc)
	}
	// The rendered dump must use the canonical snake_case keys, so re-loading it
	// yields the same model.
	raw, err := json.Marshal(doc.Services["agent"])
	if err != nil {
		t.Fatal(err)
	}
	var back ServiceDocument
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("re-decoding the dump: %v", err)
	}
	if back.Credentials == nil || back.Credentials.Sources[0].Name != "claude" {
		t.Errorf("dump does not re-load: %+v", back.Credentials)
	}
}
