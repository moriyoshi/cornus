package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestStructuralEnvAccessorsTrim pins that every structural CORNUS_* accessor
// strips surrounding whitespace, and that a whitespace-ONLY value reads as
// unset.
//
// Both halves matter and they fail differently. An untrimmed VALUE travels: it
// satisfies every `!= ""` predicate on the way and is malformed only at the one
// place that dials, opens, or compares it, so the error surfaces far from the
// configuration that caused it — the CORNUS_ADVERTISE_URL defect exactly. A
// whitespace-only value that reads as SET is worse, because it turns a feature
// on with nothing behind it.
func TestStructuralEnvAccessorsTrim(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		set    string // a realistic value, wrapped in whitespace
		want   string
		access func() string
	}{
		{"hubRedisURL", "CORNUS_HUB_REDIS", " redis://db:6379\n", "redis://db:6379", hubRedisURL},
		{"hubStore", "CORNUS_HUB_STORE", "kube\n", "kube", hubStore},
		{"hubForwardURL", "CORNUS_HUB_FORWARD_URL", "\tws://10.0.0.5:8080 ", "ws://10.0.0.5:8080", hubForwardURL},
		{"registryMirror", "CORNUS_REGISTRY_MIRROR", " mirror.example:5000\n", "mirror.example:5000", registryMirror},
		{"k8sNamespace", "CORNUS_K8S_NAMESPACE", "cornus\n", "cornus", k8sNamespace},
		{"replicaIDFromEnv", "CORNUS_REPLICA_ID", " replica-2\n", "replica-2", replicaIDFromEnv},
		{"jwtIssuer", "CORNUS_JWT_ISSUER", " https://issuer.example\n", "https://issuer.example", jwtIssuer},
		{"jwksURL", "CORNUS_JWT_JWKS_URL", " https://issuer.example/jwks\n", "https://issuer.example/jwks", jwksURL},
		{"jwksFile", "CORNUS_JWT_JWKS_FILE", " /etc/cornus/jwks.json\n", "/etc/cornus/jwks.json", jwksFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.set)
			if got := tc.access(); got != tc.want {
				t.Errorf("%s() = %q, want %q — the untrimmed value satisfies every non-empty check and fails only where it is finally used", tc.name, got, tc.want)
			}
			t.Setenv(tc.env, "   \n\t")
			if got := tc.access(); got != "" {
				t.Errorf("%s() = %q for a whitespace-only %s, want \"\": otherwise the feature switches on with nothing behind it", tc.name, got, tc.env)
			}
		})
	}
}

// TestHubStoreSelectionAgreesAcrossPredicates is the concrete failure the
// accessors exist to prevent, rather than a property of the accessors
// themselves.
//
// CORNUS_HUB_STORE is read by two consumers that ask DIFFERENT questions:
// multiReplicaHubConfigured() asks `!= ""` ("is this deployment clustered?")
// and newHubStore asks `== "kube"` ("which backend?"). Untrimmed, "kube\n"
// answers yes to the first and no to the second, so the server believes it is
// clustered while quietly building the in-memory registry — every replica then
// serves its own private hub and peers never see each other's providers. There
// is no error on that path; it is a correctness split that only shows up as
// names that will not resolve.
func TestHubStoreSelectionAgreesAcrossPredicates(t *testing.T) {
	t.Setenv("CORNUS_HUB_REDIS", "")
	t.Setenv("CORNUS_HUB_STORE", "kube\n")
	if !multiReplicaHubConfigured() {
		t.Fatal("multiReplicaHubConfigured() = false for a set CORNUS_HUB_STORE; this test can no longer see the split it guards")
	}
	if hubStore() != "kube" {
		t.Errorf("hubStore() = %q: the clustered-vs-backend predicates disagree, so the server reports a multi-replica hub and builds a replica-local one", hubStore())
	}
}

// TestSecretEnvIsNotTrimmed pins a DELIBERATE non-uniformity, because it looks
// exactly like an oversight and the tidying instinct is to "finish the job".
//
// Trimming a credential means authenticating with a value the operator did not
// configure. A token may legitimately carry trailing whitespace, and cornus
// cannot tell that apart from a stray newline — so where a malformed URL is
// unambiguously a mistake, a malformed secret is not cornus's call to make. If
// this is ever revisited (see .agents/docs/TODO.md), the answer should be a
// decision recorded there, not a sweep that quietly makes every variable behave
// alike.
func TestSecretEnvIsNotTrimmed(t *testing.T) {
	const src = "env.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	// Collect every string literal env name env.go mentions. The policy is about
	// which variables this file claims, so the file's own source is the subject —
	// calling the accessors could not detect a NEW one being added for a secret.
	claimed := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			claimed[s] = true
		}
		return true
	})
	if !claimed["CORNUS_HUB_REDIS"] {
		t.Fatal("env.go does not mention CORNUS_HUB_REDIS; the parse or this test's premise is broken, not the policy")
	}
	for _, secret := range []string{"CORNUS_AUTH_TOKEN", "CORNUS_JWT_HS256_SECRET"} {
		if claimed[secret] {
			t.Errorf("env.go has taken over %s, which trims it. Trimming a credential authenticates with a value "+
				"the operator did not configure, and a token may legitimately end in whitespace — unlike a URL, cornus "+
				"cannot tell a stray newline from intent. If this is being changed deliberately, record the decision in "+
				".agents/docs/TODO.md and delete this test; do not let a tidy-up sweep make every variable behave alike.", secret)
		}
	}
}
