package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"cornus/pkg/authtoken"
)

// okHandler is the wrapped handler under test: it 200s so any 401 must have come
// from the auth middleware.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func doReq(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// clearAuthEnv blanks every auth env var so a test is not perturbed by ambient
// configuration.
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CORNUS_AUTH_TOKEN", "CORNUS_CARETAKER_TOKEN", "CORNUS_JWT_HS256_SECRET",
		"CORNUS_JWT_PUBLIC_KEY", "CORNUS_JWT_ISSUER", "CORNUS_JWT_AUDIENCE",
		"CORNUS_REGISTRY_ANONYMOUS_PULL", "CORNUS_JWT_JWKS_FILE", "CORNUS_JWT_JWKS_URL",
	} {
		t.Setenv(k, "")
	}
}

// TestCaretakerTokenScope proves the caretaker credential is scoped to the
// caretaker endpoint only: it authenticates /.cornus/v1/caretaker/attach but is rejected
// on the client API and the registry, while a full token works everywhere.
func TestCaretakerTokenScope(t *testing.T) {
	a := &authenticator{staticToken: []byte("full"), caretakerToken: []byte("ct")}
	h := a.wrap(okHandler())

	// Caretaker token: accepted ONLY on the caretaker endpoint.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/caretaker/attach", "ct"); rec.Code != http.StatusOK {
		t.Fatalf("caretaker token on caretaker endpoint: code = %d, want 200", rec.Code)
	}
	for _, path := range []string{"/.cornus/v1/deploy", "/.cornus/v1/build", "/v2/foo/manifests/latest"} {
		if rec := doReq(t, h, http.MethodGet, path, "ct"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("caretaker token on %s: code = %d, want 401 (out of scope)", path, rec.Code)
		}
	}

	// Full token: accepted everywhere, including the caretaker endpoint.
	for _, path := range []string{"/.cornus/v1/caretaker/attach", "/.cornus/v1/deploy", "/v2/foo/manifests/latest"} {
		if rec := doReq(t, h, http.MethodGet, path, "full"); rec.Code != http.StatusOK {
			t.Fatalf("full token on %s: code = %d, want 200", path, rec.Code)
		}
	}

	// A caretaker-only authenticator still enforces the client API (fail closed):
	// no full verifier means the client endpoints reject everything.
	only := &authenticator{caretakerToken: []byte("ct")}
	ho := only.wrap(okHandler())
	if !only.enabled() {
		t.Fatal("a caretaker-only authenticator must be enabled")
	}
	if rec := doReq(t, ho, http.MethodGet, "/.cornus/v1/caretaker/attach", "ct"); rec.Code != http.StatusOK {
		t.Fatalf("caretaker-only: caretaker endpoint code = %d, want 200", rec.Code)
	}
	if rec := doReq(t, ho, http.MethodGet, "/.cornus/v1/deploy", "ct"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("caretaker-only: client API code = %d, want 401", rec.Code)
	}
}

// TestJWTScopeEnforced proves an issued caretaker-scoped JWT authenticates only the
// caretaker endpoint, while an unscoped/api JWT is a full credential — the
// issuer/verifier sharing one scope model (authtoken), end to end.
func TestJWTScopeEnforced(t *testing.T) {
	secret := []byte("this-is-a-32-byte-minimum-secret!!")
	a := &authenticator{jwt: []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}}}
	h := a.wrap(okHandler())

	mint := func(scope string) string {
		tok, err := authtoken.Issue(authtoken.IssueOptions{Subject: "s", Scope: scope, TTL: time.Hour, HS256Secret: secret})
		if err != nil {
			t.Fatalf("Issue(%q): %v", scope, err)
		}
		return tok
	}

	// caretaker-scoped: accepted on the caretaker endpoint, 401 elsewhere.
	ct := mint(authtoken.ScopeCaretaker)
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/caretaker/attach", ct); rec.Code != http.StatusOK {
		t.Fatalf("caretaker JWT on caretaker endpoint: code = %d, want 200", rec.Code)
	}
	for _, p := range []string{"/.cornus/v1/deploy", "/v2/foo/manifests/latest"} {
		if rec := doReq(t, h, http.MethodGet, p, ct); rec.Code != http.StatusUnauthorized {
			t.Fatalf("caretaker JWT on %s: code = %d, want 401 (out of scope)", p, rec.Code)
		}
	}

	// api-scoped and unscoped: full credentials, accepted everywhere.
	for _, scope := range []string{authtoken.ScopeAPI, ""} {
		full := mint(scope)
		for _, p := range []string{"/.cornus/v1/caretaker/attach", "/.cornus/v1/deploy", "/v2/foo/manifests/latest"} {
			if rec := doReq(t, h, http.MethodGet, p, full); rec.Code != http.StatusOK {
				t.Fatalf("scope %q JWT on %s: code = %d, want 200", scope, p, rec.Code)
			}
		}
	}
}

func TestAuthDisabledPassThrough(t *testing.T) {
	clearAuthEnv(t)
	// No auth env: wrap must return the handler unchanged.
	a, err := newAuthenticator()
	if err != nil {
		t.Fatal(err)
	}
	if a.enabled() {
		t.Fatal("authenticator should be disabled with no env")
	}
	h := a.wrap(okHandler())
	for _, path := range []string{"/.cornus/v1/deploy", "/v2/foo/manifests/latest", "/healthz"} {
		if rec := doReq(t, h, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200 (pass-through)", path, rec.Code)
		}
	}
}

func TestStaticTokenAcceptReject(t *testing.T) {
	a := &authenticator{staticToken: []byte("s3cret")}
	h := a.wrap(okHandler())

	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "s3cret"); rec.Code != http.StatusOK {
		t.Fatalf("valid token: code = %d, want 200", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: code = %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code = %d, want 401", rec.Code)
	}
}

func TestHealthAlwaysOpen(t *testing.T) {
	a := &authenticator{staticToken: []byte("s3cret")}
	h := a.wrap(okHandler())
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := doReq(t, h, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s must be open: code = %d, want 200", path, rec.Code)
		}
	}
}

func TestChallengeHeaders(t *testing.T) {
	a := &authenticator{staticToken: []byte("s3cret")}
	h := a.wrap(okHandler())

	rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "")
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("api challenge = %q, want Bearer", got)
	}
	// The registry challenge is Basic so a stock docker client (docker login)
	// retries with Basic credentials instead of chasing a token service cornus
	// does not have; bearer-holding clients set their own Authorization header.
	rec = doReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", "")
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="cornus"` {
		t.Fatalf("registry challenge = %q, want Basic realm=\"cornus\"", got)
	}
}

// doBasicReq issues a request carrying HTTP Basic credentials.
func doBasicReq(t *testing.T, h http.Handler, method, path, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.SetBasicAuth(user, pass)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestBasicAuthOnRegistry proves docker-login support: on /v2/* an HTTP Basic
// header whose PASSWORD is the verified credential (static token or JWT)
// authenticates — the username is ignored — while a wrong password stays 401
// and Basic is NOT accepted outside the registry.
func TestBasicAuthOnRegistry(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := &authenticator{
		staticToken: []byte("s3cret"),
		jwt:         []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}},
	}
	var gotID string
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = Identity(r)
		w.WriteHeader(http.StatusOK)
	}))

	// Static token as the Basic password: authenticates a registry read and a
	// push, whatever the username says.
	for _, user := range []string{"token", "anything", ""} {
		if rec := doBasicReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", user, "s3cret"); rec.Code != http.StatusOK {
			t.Fatalf("basic user=%q static-token pull: code = %d, want 200", user, rec.Code)
		}
	}
	if rec := doBasicReq(t, h, http.MethodPut, "/v2/foo/blobs/upload", "token", "s3cret"); rec.Code != http.StatusOK {
		t.Fatalf("basic static-token push: code = %d, want 200", rec.Code)
	}

	// A JWT as the Basic password: authenticates AND establishes the identity,
	// so per-identity registry authz (pull/push) composes with docker login.
	gotID = ""
	tok := signHS256(t, secret, jwt.Claims{Subject: "ci-bot", Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))})
	if rec := doBasicReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", "token", tok); rec.Code != http.StatusOK {
		t.Fatalf("basic JWT pull: code = %d, want 200", rec.Code)
	}
	if gotID != "ci-bot" {
		t.Fatalf("Identity via basic JWT = %q, want ci-bot", gotID)
	}

	// Wrong password: 401.
	if rec := doBasicReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", "token", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("basic wrong password: code = %d, want 401", rec.Code)
	}
	// Empty password: 401 (never treated as a token).
	if rec := doBasicReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", "token", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("basic empty password: code = %d, want 401", rec.Code)
	}
	// Basic is registry-only framing: the same credential as Basic on /.cornus/v1/* is
	// rejected (the client API speaks Bearer).
	if rec := doBasicReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "token", "s3cret"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("basic on /.cornus/v1: code = %d, want 401", rec.Code)
	}
	// Bearer still works on the registry, unchanged.
	if rec := doReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", "s3cret"); rec.Code != http.StatusOK {
		t.Fatalf("bearer pull: code = %d, want 200", rec.Code)
	}
}

// TestBasicAuthCaretakerTokenStillScoped proves the scoped caretaker credential
// framed as Basic cannot reach the registry — Basic goes through the same
// verifier chain, scope rules included.
func TestBasicAuthCaretakerTokenStillScoped(t *testing.T) {
	a := &authenticator{staticToken: []byte("full"), caretakerToken: []byte("ct")}
	h := a.wrap(okHandler())
	if rec := doBasicReq(t, h, http.MethodGet, "/v2/foo/manifests/latest", "token", "ct"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("caretaker token as basic password on /v2: code = %d, want 401", rec.Code)
	}
}

func signHS256(t *testing.T, secret []byte, claims jwt.Claims) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: secret}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWTHS256(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := &authenticator{jwt: []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}}}
	h := a.wrap(okHandler())

	now := time.Now()
	valid := signHS256(t, secret, jwt.Claims{Subject: "alice", Expiry: jwt.NewNumericDate(now.Add(time.Hour)), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", valid); rec.Code != http.StatusOK {
		t.Fatalf("valid HS256: code = %d, want 200", rec.Code)
	}

	expired := signHS256(t, secret, jwt.Claims{Subject: "alice", Expiry: jwt.NewNumericDate(now.Add(-time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", expired); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired HS256: code = %d, want 401", rec.Code)
	}

	badSig := signHS256(t, []byte("FEDCBA9876543210FEDCBA9876543210"), jwt.Claims{Subject: "alice", Expiry: jwt.NewNumericDate(now.Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", badSig); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-signature HS256: code = %d, want 401", rec.Code)
	}
}

// TestJWTRejectsMissingExp proves a validly-signed token with NO exp claim is
// rejected: go-jose only checks expiry when it is present, so without an explicit
// requirement such a token would authenticate forever, defeating expiry-based
// rotation/revocation.
func TestJWTRejectsMissingExp(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := &authenticator{jwt: []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}}}
	h := a.wrap(okHandler())

	noExp := signHS256(t, secret, jwt.Claims{Subject: "alice"}) // no Expiry set
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", noExp); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token without exp: code = %d, want 401 (a never-expiring credential)", rec.Code)
	}

	// Control: the same token WITH an exp still authenticates.
	withExp := signHS256(t, secret, jwt.Claims{Subject: "alice", Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", withExp); rec.Code != http.StatusOK {
		t.Fatalf("token with exp: code = %d, want 200", rec.Code)
	}
}

func TestJWTIssuerAudience(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := &authenticator{
		jwt:      []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}},
		issuer:   "https://issuer.example",
		audience: "cornus",
	}
	h := a.wrap(okHandler())
	now := time.Now()

	good := signHS256(t, secret, jwt.Claims{Subject: "a", Issuer: "https://issuer.example", Audience: jwt.Audience{"cornus"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", good); rec.Code != http.StatusOK {
		t.Fatalf("matching iss/aud: code = %d, want 200", rec.Code)
	}
	wrongIss := signHS256(t, secret, jwt.Claims{Subject: "a", Issuer: "https://evil.example", Audience: jwt.Audience{"cornus"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", wrongIss); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong issuer: code = %d, want 401", rec.Code)
	}
	wrongAud := signHS256(t, secret, jwt.Claims{Subject: "a", Issuer: "https://issuer.example", Audience: jwt.Audience{"other"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", wrongAud); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience: code = %d, want 401", rec.Code)
	}
}

// writePEM writes a PKIX public-key PEM to a temp file and returns its path.
func writePubKeyPEM(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pub.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJWTRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := writePubKeyPEM(t, &key.PublicKey)
	v, err := loadPublicKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.algs) != 1 || v.algs[0] != jose.RS256 {
		t.Fatalf("RSA verifier algs = %v, want [RS256]", v.algs)
	}
	a := &authenticator{jwt: []jwtVerifier{v}}
	h := a.wrap(okHandler())

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(sig).Claims(jwt.Claims{Subject: "bob", Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", token); rec.Code != http.StatusOK {
		t.Fatalf("valid RS256: code = %d, want 200", rec.Code)
	}
}

func TestJWTES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := writePubKeyPEM(t, &key.PublicKey)
	v, err := loadPublicKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.algs) != 1 || v.algs[0] != jose.ES256 {
		t.Fatalf("ECDSA verifier algs = %v, want [ES256]", v.algs)
	}
	a := &authenticator{jwt: []jwtVerifier{v}}
	h := a.wrap(okHandler())

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(sig).Claims(jwt.Claims{Subject: "carol", Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", token); rec.Code != http.StatusOK {
		t.Fatalf("valid ES256: code = %d, want 200", rec.Code)
	}
}

// TestAlgorithmConfusion is the security-critical case: a token HMAC-signed with
// the PEM public-key bytes as the secret must be REJECTED when only a public key
// is configured, because a public-key verifier never permits HS256.
func TestAlgorithmConfusion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := writePubKeyPEM(t, &key.PublicKey)
	v, err := loadPublicKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &authenticator{jwt: []jwtVerifier{v}}
	h := a.wrap(okHandler())

	// Forge a token HMAC-signed with the public key PEM bytes as the HMAC secret.
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	forged := signHS256(t, pemBytes, jwt.Claims{Subject: "attacker", Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", forged); rec.Code != http.StatusUnauthorized {
		t.Fatalf("algorithm-confusion token accepted (code = %d); MUST be 401", rec.Code)
	}
}

func TestAnonymousPull(t *testing.T) {
	a := &authenticator{staticToken: []byte("s3cret"), anonPull: true}
	h := a.wrap(okHandler())

	// GET/HEAD under /v2/* is allowed unauthenticated.
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if rec := doReq(t, h, m, "/v2/foo/manifests/latest", ""); rec.Code != http.StatusOK {
			t.Fatalf("anon %s /v2: code = %d, want 200", m, rec.Code)
		}
	}
	// PUT (push) still requires auth.
	if rec := doReq(t, h, http.MethodPut, "/v2/foo/blobs/upload", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon PUT /v2: code = %d, want 401", rec.Code)
	}
	// /.cornus/v1/* is unaffected by anonymous-pull: GET still requires auth.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon GET /.cornus/v1: code = %d, want 401", rec.Code)
	}
	// A valid token still works on /v2 push.
	if rec := doReq(t, h, http.MethodPut, "/v2/foo/blobs/upload", "s3cret"); rec.Code != http.StatusOK {
		t.Fatalf("authed PUT /v2: code = %d, want 200", rec.Code)
	}
}

func TestMultipleVerifiersEitherAccepts(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := writePubKeyPEM(t, &key.PublicKey)
	pv, err := loadPublicKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &authenticator{
		staticToken: []byte("static-tok"),
		jwt: []jwtVerifier{
			{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret},
			pv,
		},
	}
	h := a.wrap(okHandler())
	now := time.Now()

	// Static token accepted.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "static-tok"); rec.Code != http.StatusOK {
		t.Fatalf("static: code = %d, want 200", rec.Code)
	}
	// HS256 accepted.
	hs := signHS256(t, secret, jwt.Claims{Subject: "a", Expiry: jwt.NewNumericDate(now.Add(time.Hour))})
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", hs); rec.Code != http.StatusOK {
		t.Fatalf("HS256: code = %d, want 200", rec.Code)
	}
	// RS256 accepted.
	sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	rs, _ := jwt.Signed(sig).Claims(jwt.Claims{Subject: "b", Expiry: jwt.NewNumericDate(now.Add(time.Hour))}).Serialize()
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", rs); rec.Code != http.StatusOK {
		t.Fatalf("RS256: code = %d, want 200", rec.Code)
	}
	// Garbage rejected.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "not-a-token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage: code = %d, want 401", rec.Code)
	}
}

func TestNewAuthenticatorFromEnv(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CORNUS_AUTH_TOKEN", "tok")
	t.Setenv("CORNUS_JWT_HS256_SECRET", "sekret")
	t.Setenv("CORNUS_JWT_ISSUER", "iss")
	t.Setenv("CORNUS_JWT_AUDIENCE", "aud")
	t.Setenv("CORNUS_REGISTRY_ANONYMOUS_PULL", "yes")

	a, err := newAuthenticator()
	if err != nil {
		t.Fatal(err)
	}
	if !a.enabled() {
		t.Fatal("should be enabled")
	}
	if string(a.staticToken) != "tok" {
		t.Fatalf("staticToken = %q", a.staticToken)
	}
	if len(a.jwt) != 1 || a.jwt[0].algs[0] != jose.HS256 {
		t.Fatalf("jwt verifiers = %+v", a.jwt)
	}
	if a.issuer != "iss" || a.audience != "aud" || !a.anonPull {
		t.Fatalf("iss/aud/anon = %q/%q/%v", a.issuer, a.audience, a.anonPull)
	}
}

func TestNewAuthenticatorBadPublicKey(t *testing.T) {
	clearAuthEnv(t)
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORNUS_JWT_PUBLIC_KEY", path)
	if _, err := newAuthenticator(); err == nil {
		t.Fatal("expected error for malformed public key")
	}
}

func TestSubjectOnContext(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := &authenticator{jwt: []jwtVerifier{{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}}}
	var gotSub string
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSub = Subject(r)
		w.WriteHeader(http.StatusOK)
	}))
	token := signHS256(t, secret, jwt.Claims{Subject: "dave", Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))})
	doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", token)
	if gotSub != "dave" {
		t.Fatalf("Subject = %q, want dave", gotSub)
	}
}
