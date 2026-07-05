package server

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"cornus/pkg/authscope"
	"cornus/pkg/authtoken"
)

// catchAllScopeMap is the permissive map used by tests whose subject is
// something other than scope policy (key selection, rotation). It stands for
// CORNUS_JWT_DEFAULT_SCOPE=api.
func catchAllScopeMap(t *testing.T) *authscope.Map {
	t.Helper()
	m, err := authscope.DefaultScopeMap(authtoken.ScopeAPI)
	if err != nil {
		t.Fatalf("DefaultScopeMap: %v", err)
	}
	return m
}

func mustScopeMap(t *testing.T, yaml string) *authscope.Map {
	t.Helper()
	m, err := authscope.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse scope map: %v", err)
	}
	return m
}

// mintES256 signs a token with arbitrary extra claims, so a test can model what
// a third party actually emits (a scope in someone else's vocabulary, a
// Kubernetes SA subject) rather than only what cornus mints.
func mintES256(t *testing.T, priv *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid),
	)
	if err != nil {
		t.Fatal(err)
	}
	base := jwt.Claims{Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	tok, err := jwt.Signed(signer).Claims(base).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func jwksAuthenticator(t *testing.T, k ecKey, m *authscope.Map) http.Handler {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(path, jwksOf(t, k), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &authenticator{jwks: &jwksResolver{src: &jwksFileSource{path: path}}, scopeMap: m}
	return a.wrap(okHandler())
}

// TestJWKSScopeClaimIsNotAuthoritative is the security property the scope map
// exists for: a third party that cornus trusts to prove IDENTITY must not
// thereby be able to grant itself AUTHORITY.
//
// The token here is signed by a key in the configured JWKS and says `scope: api`
// — the strongest thing a cornus token can say. Because the operator does not
// hold that key, the claim decides nothing: with no map the token reaches
// nothing, and with a map granting registry:pull it reaches exactly registry
// pull, its own claim notwithstanding.
func TestJWKSScopeClaimIsNotAuthoritative(t *testing.T) {
	k := genEC(t, "k1")
	tok := mintES256(t, k.priv, "k1", map[string]any{
		"sub":   "system:serviceaccount:ci:runner",
		"scope": authtoken.ScopeAPI,
	})

	// 1. No scope map: the issuer is verified, and grants nothing.
	h := jwksAuthenticator(t, k, nil)
	rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tok)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no scope map: code = %d, want 401 (a third party's scope claim must not grant)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "scope map") {
		t.Fatalf("no scope map: body %q must tell the operator a scope map is missing", rec.Body.String())
	}

	// 2. A map that grants LESS than the token asked for. The map wins.
	h = jwksAuthenticator(t, k, mustScopeMap(t, `
rules:
  - name: ci runners pull
    scope: registry:pull
    match:
      sub: { prefix: "system:serviceaccount:ci:" }
`))
	if rec := doReq(t, h, http.MethodGet, "/v2/app/manifests/latest", tok); rec.Code != http.StatusOK {
		t.Fatalf("mapped to registry:pull, registry read: code = %d, want 200", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mapped to registry:pull, deploy: code = %d, want 401 — the token's own `scope: api` must not win", rec.Code)
	}
}

// TestJWKSScopelessTokenIsMapped is the case that broke incluster-kubeauth: a
// Kubernetes ServiceAccount token carries no scope claim and cannot be given
// one, so it is reachable ONLY through the map.
func TestJWKSScopelessTokenIsMapped(t *testing.T) {
	k := genEC(t, "k1")
	tok := mintES256(t, k.priv, "k1", map[string]any{
		"sub":                                    "system:serviceaccount:cornus-e2e:default",
		"aud":                                    []string{"cornus"},
		"kubernetes.io/serviceaccount/namespace": "cornus-e2e",
	})

	h := jwksAuthenticator(t, k, mustScopeMap(t, `
rules:
  - name: the e2e service account is an operator
    scope: api
    match:
      sub: { prefix: "system:serviceaccount:cornus-e2e:" }
      "kubernetes.io/serviceaccount/namespace": { equals: cornus-e2e }
`))
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tok); rec.Code != http.StatusOK {
		t.Fatalf("mapped SA token: code = %d, want 200", rec.Code)
	}

	// A SA from another namespace is not covered by the rule and gets nothing,
	// which is the half that makes the rule mean something.
	other := mintES256(t, k.priv, "k1", map[string]any{
		"sub":                                    "system:serviceaccount:kube-system:default",
		"kubernetes.io/serviceaccount/namespace": "kube-system",
	})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", other); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unmatched SA token: code = %d, want 401", rec.Code)
	}
}

// TestOperatorKeyScopeStaysAuthoritative pins the other side of the dividing
// line. CORNUS_JWT_HS256_SECRET is the key `cornus token issue --hs256-secret`
// signs with, so the operator holds it and a scope it asserts is the operator's
// own assertion — it must keep deciding, with no scope map involved. Treating it
// as third-party would break every documented operator token.
func TestOperatorKeyScopeStaysAuthoritative(t *testing.T) {
	secret := []byte("this-is-a-32-byte-minimum-secret!!")
	a := &authenticator{jwt: []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}}}
	h := a.wrap(okHandler())

	tok, err := authtoken.Issue(authtoken.IssueOptions{
		Subject: "ci-bot", Scope: authtoken.ScopeRegistryPull, TTL: time.Hour, HS256Secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, http.MethodGet, "/v2/app/manifests/latest", tok); rec.Code != http.StatusOK {
		t.Fatalf("operator-issued registry:pull on registry: code = %d, want 200 (no scope map configured)", rec.Code)
	}
	// ...and it is still bounded by what it named.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("operator-issued registry:pull on deploy: code = %d, want 401", rec.Code)
	}
}

// TestOperatorKeyScopelessFallsThroughToMap covers the foreign IdP reached
// through CORNUS_JWT_PUBLIC_KEY rather than a JWKS. The operator configured the
// key, but the tokens are someone else's and carry no cornus scope, so the map
// has to be able to grant them something — otherwise that configuration has no
// working form at all.
func TestOperatorKeyScopelessFallsThroughToMap(t *testing.T) {
	k := genEC(t, "k1")
	der, err := x509.MarshalECPrivateKey(k.priv)
	if err != nil {
		t.Fatal(err)
	}
	_ = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	a := &authenticator{
		jwt: []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.ES256}, key: &k.priv.PublicKey}},
		scopeMap: mustScopeMap(t, `
rules:
  - name: verified staff read the registry
    scope: registry:pull
    match:
      email: { suffix: "@example.com" }
      email_verified: { equals: true }
`),
	}
	h := a.wrap(okHandler())

	ok := mintES256(t, k.priv, "k1", map[string]any{
		"sub": "u1", "email": "dev@example.com", "email_verified": true,
		"scope": "openid profile email", // a foreign vocabulary: names nothing cornus knows
	})
	if rec := doReq(t, h, http.MethodGet, "/v2/app/manifests/latest", ok); rec.Code != http.StatusOK {
		t.Fatalf("mapped OIDC token on registry: code = %d, want 200", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", ok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mapped OIDC token on deploy: code = %d, want 401", rec.Code)
	}

	// Unverified email: the rule's second condition fails, so nothing is granted.
	// A conjunction that silently ignored a claim it could not find would pass
	// this on the first condition alone.
	unverified := mintES256(t, k.priv, "k1", map[string]any{
		"sub": "u2", "email": "attacker@example.com", "email_verified": false,
	})
	if rec := doReq(t, h, http.MethodGet, "/v2/app/manifests/latest", unverified); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unverified email: code = %d, want 401", rec.Code)
	}
}
