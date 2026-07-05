package authscope

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/authtoken"
)

func TestMatchers(t *testing.T) {
	cases := []struct {
		name    string
		matcher Matcher
		value   any
		want    bool
	}{
		{"equals string", Matcher{Equals: "a"}, "a", true},
		{"equals string miss", Matcher{Equals: "a"}, "b", false},
		{"equals bool true", Matcher{Equals: true}, true, true},
		// `equals: false` must be a real test, not read as "unset". This is why
		// Equals is `any` rather than a string.
		{"equals bool false matches false", Matcher{Equals: false}, false, true},
		{"equals bool false rejects true", Matcher{Equals: false}, true, false},
		// A JSON claim set decodes numbers as float64 while a YAML rule decodes
		// them as int, so the comparison has to normalize or every numeric rule
		// silently never matches.
		{"equals number across decoders", Matcher{Equals: 3}, float64(3), true},
		{"equals number miss", Matcher{Equals: 3}, float64(4), false},
		{"equals rejects type mismatch", Matcher{Equals: "1"}, float64(1), false},

		{"prefix", Matcher{Prefix: "system:serviceaccount:ci:"}, "system:serviceaccount:ci:runner", true},
		{"prefix miss", Matcher{Prefix: "system:serviceaccount:ci:"}, "system:serviceaccount:prod:runner", false},
		{"suffix", Matcher{Suffix: "@example.com"}, "dev@example.com", true},
		{"suffix miss", Matcher{Suffix: "@example.com"}, "dev@evil.example.com.attacker.test", false},

		// path.Match semantics: `*` does not cross `/`, which is what keeps a
		// glob over a path-shaped claim from reaching further than it reads.
		{"glob", Matcher{Glob: "repos/*"}, "repos/app", true},
		{"glob does not cross slash", Matcher{Glob: "repos/*"}, "repos/team/app", false},
		{"glob star matches colons", Matcher{Glob: "system:serviceaccount:*"}, "system:serviceaccount:ci:runner", true},

		{"any_of hit", Matcher{AnyOf: []string{"a", "b"}}, "b", true},
		{"any_of miss", Matcher{AnyOf: []string{"a", "b"}}, "c", false},

		{"contains array", Matcher{Contains: "admins"}, []any{"users", "admins"}, true},
		{"contains array miss", Matcher{Contains: "admins"}, []any{"users"}, false},
		// OAuth spells a scope list as one space-separated string, so membership
		// has to work on both shapes or the operator must know which their issuer
		// emits.
		{"contains space separated", Matcher{Contains: "email"}, "openid profile email", true},
		{"contains is not substring", Matcher{Contains: "mail"}, "openid profile email", false},

		// A matcher that expects a string must not match a non-string claim by
		// accident.
		{"prefix rejects non-string", Matcher{Prefix: "a"}, []any{"abc"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.matcher.matches(tc.value); got != tc.want {
				t.Fatalf("matches(%#v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestLookupLiteralBeforeDotted(t *testing.T) {
	claims := map[string]any{
		// The legacy Kubernetes SA token spelling: one flat claim whose name
		// contains slashes.
		"kubernetes.io/serviceaccount/namespace": "flat",
		// The bound-token spelling: nested objects.
		"kubernetes.io": map[string]any{"pod": map[string]any{"name": "p1"}},
		// A claim whose NAME contains a dot and which must not be reinterpreted
		// as a path.
		"a.b": "literal",
		"a":   map[string]any{"b": "nested"},
	}
	for _, tc := range []struct {
		key  string
		want any
		ok   bool
	}{
		{"kubernetes.io/serviceaccount/namespace", "flat", true},
		{"kubernetes.io.pod.name", "p1", true},
		{"a.b", "literal", true}, // literal wins over the nested path
		{"a.c", nil, false},
		{"missing", nil, false},
		{"a.b.c", nil, false}, // walking into a string
	} {
		got, ok := lookup(claims, tc.key)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("lookup(%q) = (%v, %v), want (%v, %v)", tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMatchIsConjunctionAndOrdered(t *testing.T) {
	m, err := Parse([]byte(`
rules:
  - name: prod pushers
    scope: registry:push
    match:
      sub: { prefix: "system:serviceaccount:prod:" }
      email_verified: { equals: true }
  - name: everyone else pulls
    scope: registry:pull
    match:
      sub: { prefix: "system:serviceaccount:" }
`))
	if err != nil {
		t.Fatal(err)
	}

	// Both conditions hold -> the first rule.
	scope, rule, err := m.Match(map[string]any{
		"sub": "system:serviceaccount:prod:deployer", "email_verified": true,
	})
	if err != nil || scope != authtoken.ScopeRegistryPush {
		t.Fatalf("prod match = (%q, %q, %v), want registry:push", scope, rule, err)
	}

	// The conjunction fails on the second condition, so the FIRST rule does not
	// apply and the later, broader rule does. A matcher that ignored a claim it
	// could not satisfy would wrongly return registry:push here.
	scope, _, err = m.Match(map[string]any{
		"sub": "system:serviceaccount:prod:deployer", "email_verified": false,
	})
	if err != nil || scope != authtoken.ScopeRegistryPull {
		t.Fatalf("unverified prod match = (%q, %v), want registry:pull", scope, err)
	}

	// A missing claim fails its matcher rather than being skipped.
	scope, _, err = m.Match(map[string]any{"sub": "system:serviceaccount:prod:deployer"})
	if err != nil || scope != authtoken.ScopeRegistryPull {
		t.Fatalf("absent claim match = (%q, %v), want registry:pull", scope, err)
	}

	// Nothing matches -> fail closed.
	if _, _, err := m.Match(map[string]any{"sub": "someone-else"}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("unmatched claims: err = %v, want ErrNoMatch", err)
	}
}

// TestNilMapGrantsNothing pins the default for an unconfigured server: verifying
// an issuer proves identity and must not by itself confer authority.
func TestNilMapGrantsNothing(t *testing.T) {
	var m *Map
	if _, _, err := m.Match(map[string]any{"sub": "anyone"}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("nil map: err = %v, want ErrNoMatch", err)
	}
}

func TestValidateRejectsBadPolicy(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"no rules", "rules: []", "no rules"},
		{"unknown scope", "rules:\n  - scope: superuser\n    match:\n      sub: {prefix: x}\n", "names no cornus scope"},
		{"no scope", "rules:\n  - match:\n      sub: {prefix: x}\n", "names no scope"},
		// A rule matching nothing would grant its scope to every token the
		// verifier accepts. That may be the intent, but it has to be said.
		{"empty match", "rules:\n  - scope: api\n    match: {}\n", "matches no claims"},
		{"matcher with no test", "rules:\n  - scope: api\n    match:\n      sub: {}\n", "names no test"},
		{"matcher with two tests", "rules:\n  - scope: api\n    match:\n      sub: {prefix: a, suffix: b}\n", "more than one test"},
		{"bad glob", "rules:\n  - scope: api\n    match:\n      sub: {glob: \"[\"}\n", "invalid glob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want an error", tc.yaml)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestParseAcceptsJSON: YAML is a superset, so an operator who prefers JSON (or
// generates the policy) needs no separate code path.
func TestParseAcceptsJSON(t *testing.T) {
	raw, err := json.Marshal(Map{Rules: []Rule{{
		Name: "r", Scope: authtoken.ScopeAPI,
		Match: map[string]Matcher{"sub": {Equals: "s"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if scope, _, err := m.Match(map[string]any{"sub": "s"}); err != nil || scope != authtoken.ScopeAPI {
		t.Fatalf("json map match = (%q, %v)", scope, err)
	}
}

func TestLoadReportsPathAndRefusesBadPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scopes.yaml")
	if err := os.WriteFile(path, []byte("rules:\n  - scope: nope\n    match:\n      sub: {prefix: x}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded on an invalid policy, want an error")
	}
	// The path has to be in the message: a server that refuses to start over a
	// policy file must say which file.
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("Load error = %q, want it to name %q", err, path)
	}

	if _, err := Load(filepath.Join(dir, "absent.yaml")); err == nil {
		t.Fatal("Load of a missing file succeeded, want an error")
	}
}

// TestDefaultScopeMapAppendsBehind is the CORNUS_JWT_DEFAULT_SCOPE contract:
// specific rules keep deciding, and the catch-all only picks up what none of
// them claimed.
func TestDefaultScopeMapAppendsBehind(t *testing.T) {
	explicit, err := Parse([]byte(`
rules:
  - name: ci pulls
    scope: registry:pull
    match:
      sub: { prefix: "ci:" }
`))
	if err != nil {
		t.Fatal(err)
	}
	def, err := DefaultScopeMap(authtoken.ScopeAPI)
	if err != nil {
		t.Fatal(err)
	}
	m := explicit.Append(def)

	if scope, _, err := m.Match(map[string]any{"sub": "ci:runner"}); err != nil || scope != authtoken.ScopeRegistryPull {
		t.Fatalf("ci subject = (%q, %v), want registry:pull — the explicit rule must win", scope, err)
	}
	if scope, _, err := m.Match(map[string]any{"sub": "someone"}); err != nil || scope != authtoken.ScopeAPI {
		t.Fatalf("other subject = (%q, %v), want api from the catch-all", scope, err)
	}
	// The catch-all still requires a subject: a token with no `sub` at all is not
	// something to hand full access to.
	if _, _, err := m.Match(map[string]any{"email": "x@example.com"}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("subjectless token: err = %v, want ErrNoMatch", err)
	}

	if _, err := DefaultScopeMap("superuser"); err == nil {
		t.Fatal("DefaultScopeMap accepted an unknown scope, want an error")
	}
}
