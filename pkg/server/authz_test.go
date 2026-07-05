package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"

	"cornus/pkg/api"
)

// peerCertReq builds a request carrying a VERIFIED client certificate with the
// given CommonName, exactly as the TLS layer would populate it after
// VerifyClientCertIfGiven succeeds.
func peerCertReq(method, path, cn string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
	req.TLS = &tls.ConnectionState{
		VerifiedChains:   [][]*x509.Certificate{{cert}},
		PeerCertificates: []*x509.Certificate{cert},
	}
	return req
}

// TestMTLSAuthenticatesAsIdentity proves a verified client cert authenticates and
// its CommonName surfaces through Identity(r), with no bearer token present.
func TestMTLSAuthenticatesAsIdentity(t *testing.T) {
	a := &authenticator{mtls: true}
	if !a.enabled() {
		t.Fatal("mTLS-only authenticator must be enabled")
	}
	var gotID string
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = Identity(r)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, peerCertReq(http.MethodGet, "/.cornus/v1/deploy", "ci-bot"))
	if rec.Code != http.StatusOK {
		t.Fatalf("verified cert: code = %d, want 200", rec.Code)
	}
	if gotID != "ci-bot" {
		t.Fatalf("Identity = %q, want ci-bot", gotID)
	}
}

// TestMTLSPrecedenceOverBearer proves a verified client cert wins over a bearer
// token when both are present.
func TestMTLSPrecedenceOverBearer(t *testing.T) {
	a := &authenticator{mtls: true, staticToken: []byte("s3cret")}
	var gotID string
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = Identity(r)
		w.WriteHeader(http.StatusOK)
	}))

	req := peerCertReq(http.MethodGet, "/.cornus/v1/deploy", "cert-id")
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if gotID != "cert-id" {
		t.Fatalf("Identity = %q, want cert-id (cert wins over bearer)", gotID)
	}
}

// TestMTLSNoCertFallsBackToBearer proves that without a client cert, a request
// still authenticates via its bearer token.
func TestMTLSNoCertFallsBackToBearer(t *testing.T) {
	a := &authenticator{mtls: true, staticToken: []byte("s3cret")}
	h := a.wrap(okHandler())

	// No cert, valid bearer -> ok.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", "s3cret"); rec.Code != http.StatusOK {
		t.Fatalf("no cert + valid bearer: code = %d, want 200", rec.Code)
	}
	// No cert, no bearer -> 401.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cert + no bearer: code = %d, want 401", rec.Code)
	}
}

// TestAPIPolicyAllow exercises the Allow matrix: unconfigured allow-all, explicit
// action, the "*" wildcard, an unlisted identity/action, and the empty-identity
// fail-closed rule.
func TestAPIPolicyAllow(t *testing.T) {
	// Unconfigured (nil) allows everything, including an empty identity.
	var nilPol *apiPolicy
	if !nilPol.Allow("", "deploy") || !nilPol.Allow("anyone", "build") {
		t.Fatal("nil apiPolicy must allow all")
	}
	// An empty rules map is also nil (allow-all).
	if newAPIPolicy(map[string][]string{}) != nil {
		t.Fatal("empty rules must yield a nil (allow-all) policy")
	}

	pol := newAPIPolicy(map[string][]string{
		"ci-bot":   {"deploy", "build"},
		"deployer": {"deploy"},
		"admin":    {"*"},
	})
	cases := []struct {
		identity, action string
		want             bool
	}{
		{"ci-bot", "deploy", true},
		{"ci-bot", "build", true},
		{"deployer", "deploy", true},
		{"deployer", "build", false},
		{"admin", "deploy", true},
		{"admin", "build", true},
		{"admin", "anything", true}, // "*" wildcard
		{"stranger", "deploy", false},
		{"", "deploy", false}, // empty identity denied when configured
	}
	for _, c := range cases {
		if got := pol.Allow(c.identity, c.action); got != c.want {
			t.Errorf("Allow(%q, %q) = %v, want %v", c.identity, c.action, got, c.want)
		}
	}
}

// jwtFor mints an HS256 JWT with the given subject using the shared test secret.
func jwtFor(t *testing.T, secret []byte, sub string) string {
	t.Helper()
	return signHS256(t, secret, jwt.Claims{Subject: sub, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))})
}

// TestAPIPolicyHandlerEnforcement drives the full stack: a configured policy 403s
// a disallowed identity on POST /.cornus/v1/deploy and POST /.cornus/v1/build and allows an
// allowed one; identity is established via JWT `sub`.
func TestAPIPolicyHandlerEnforcement(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["deploy","build"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	ci := jwtFor(t, secret, "ci-bot")
	stranger := jwtFor(t, secret, "stranger")

	post := func(t *testing.T, path, token string, body []byte) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	spec, _ := json.Marshal(api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"})

	// Allowed identity: deploy succeeds (200).
	if code := post(t, "/.cornus/v1/deploy", ci, spec); code != http.StatusOK {
		t.Fatalf("ci-bot POST /.cornus/v1/deploy: code = %d, want 200", code)
	}
	// Disallowed identity: 403 before the backend runs.
	if code := post(t, "/.cornus/v1/deploy", stranger, spec); code != http.StatusForbidden {
		t.Fatalf("stranger POST /.cornus/v1/deploy: code = %d, want 403", code)
	}

	// Build: allowed identity passes authz and then 400s on the missing target
	// (proving it got past the 403 gate); disallowed identity is 403.
	if code := post(t, "/.cornus/v1/build", ci, nil); code != http.StatusBadRequest {
		t.Fatalf("ci-bot POST /.cornus/v1/build (no target): code = %d, want 400 (past authz)", code)
	}
	if code := post(t, "/.cornus/v1/build", stranger, nil); code != http.StatusForbidden {
		t.Fatalf("stranger POST /.cornus/v1/build: code = %d, want 403", code)
	}
}

// TestAPIPolicyGatesMutatingActions proves a configured "deploy" restriction also
// covers the deployment-item mutations (start/stop/restart, exec, attach), not just
// create/delete — otherwise the restriction could be bypassed via those actions.
func TestAPIPolicyGatesMutatingActions(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["deploy"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	ci := jwtFor(t, secret, "ci-bot")
	stranger := jwtFor(t, secret, "stranger")

	post := func(t *testing.T, path, token string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, action := range []string{"stop", "start", "restart", "exec"} {
		p := "/.cornus/v1/deploy/web/" + action
		if code := post(t, p, stranger); code != http.StatusForbidden {
			t.Fatalf("stranger POST %s: code = %d, want 403 (mutating action gated)", p, code)
		}
		// The allowed identity gets past the 403 gate (any non-403 status proves it).
		if code := post(t, p, ci); code == http.StatusForbidden {
			t.Fatalf("ci-bot POST %s: got 403, should pass the deploy gate", p)
		}
	}
}

// TestAPIPolicyAllowExec pins the exec-action semantic: "exec" is allowed by an
// explicit exec grant OR by "deploy" (deploy implies exec, so pre-existing
// policies keep working); an exec-only identity has no other action, and exec
// cannot be denied to a deploy-capable identity by construction.
func TestAPIPolicyAllowExec(t *testing.T) {
	var nilPol *apiPolicy
	if !nilPol.AllowExec("anyone") {
		t.Fatal("nil apiPolicy must allow exec")
	}
	pol := newAPIPolicy(map[string][]string{
		"runner":   {"exec"},
		"deployer": {"deploy"},
		"admin":    {"*"},
	})
	cases := []struct {
		identity string
		want     bool
	}{
		{"runner", true},   // explicit exec grant
		{"deployer", true}, // deploy implies exec
		{"admin", true},    // wildcard
		{"stranger", false},
		{"", false}, // fail closed
	}
	for _, c := range cases {
		if got := pol.AllowExec(c.identity); got != c.want {
			t.Errorf("AllowExec(%q) = %v, want %v", c.identity, got, c.want)
		}
	}
	// The exec-only identity must not hold the broader deploy action.
	if pol.Allow("runner", "deploy") {
		t.Fatal("exec-only identity must not be allowed deploy")
	}
}

// TestAPIPolicyExecOnlyIdentity drives the full stack with an exec-only
// identity: it may create an exec (and resize it) but cannot apply, delete, or
// stop deployments — nor open a deploy-attach session — while a deploy-capable
// identity keeps exec implicitly.
func TestAPIPolicyExecOnlyIdentity(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"runner":["exec"],"deployer":["deploy"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	runner := jwtFor(t, secret, "runner")
	deployer := jwtFor(t, secret, "deployer")
	stranger := jwtFor(t, secret, "stranger")

	do := func(t *testing.T, method, path, token string, body []byte) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	execBody := []byte(`{"Cmd":["sh"]}`)

	// Exec create: the exec-only identity and the deploy identity both pass the
	// gate; a stranger is denied.
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy/web/exec", runner, execBody); code != http.StatusOK {
		t.Fatalf("runner exec create: code = %d, want 200", code)
	}
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy/web/exec", deployer, execBody); code != http.StatusOK {
		t.Fatalf("deployer exec create: code = %d, want 200 (deploy implies exec)", code)
	}
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy/web/exec", stranger, execBody); code != http.StatusForbidden {
		t.Fatalf("stranger exec create: code = %d, want 403", code)
	}

	// Exec lifecycle: resize is exec-gated too (a leaked exec id is not enough).
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy/exec/exec-web/resize?h=24&w=80", runner, nil); code != http.StatusOK {
		t.Fatalf("runner exec resize: code = %d, want 200", code)
	}
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy/exec/exec-web/resize?h=24&w=80", stranger, nil); code != http.StatusForbidden {
		t.Fatalf("stranger exec resize: code = %d, want 403", code)
	}

	// The exec-only identity must NOT hold deploy powers: apply, delete,
	// lifecycle actions, and the deploy-attach WebSocket are all 403.
	spec, _ := json.Marshal(api.DeploySpec{Name: "web", Image: "img"})
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy", runner, spec); code != http.StatusForbidden {
		t.Fatalf("runner POST /.cornus/v1/deploy: code = %d, want 403", code)
	}
	if code := do(t, http.MethodDelete, "/.cornus/v1/deploy/web", runner, nil); code != http.StatusForbidden {
		t.Fatalf("runner DELETE /.cornus/v1/deploy/web: code = %d, want 403", code)
	}
	if code := do(t, http.MethodPost, "/.cornus/v1/deploy/web/stop", runner, nil); code != http.StatusForbidden {
		t.Fatalf("runner POST stop: code = %d, want 403", code)
	}
	if code := do(t, http.MethodGet, "/.cornus/v1/deploy/attach", runner, nil); code != http.StatusForbidden {
		t.Fatalf("runner GET /.cornus/v1/deploy/attach: code = %d, want 403 (deploy-attach applies a spec)", code)
	}
	// The deploy identity passes the deploy-attach gate (any non-403 proves it;
	// the request then fails the WebSocket upgrade, which is fine).
	if code := do(t, http.MethodGet, "/.cornus/v1/deploy/attach", deployer, nil); code == http.StatusForbidden {
		t.Fatalf("deployer GET /.cornus/v1/deploy/attach: got 403, should pass the deploy gate")
	}
}

// TestAPIPolicyDeniesAnonymousWhenConfigured proves the interaction rule: a
// configured API policy denies a caller with no identity (auth off, so Identity is
// empty), even though authentication itself is disabled — fail closed.
func TestAPIPolicyDeniesAnonymousWhenConfigured(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["deploy"]}`)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	spec, _ := json.Marshal(api.DeploySpec{Name: "web", Image: "img"})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(spec))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous POST /.cornus/v1/deploy with configured policy: code = %d, want 403", resp.StatusCode)
	}
}

// TestAPIPolicyUnconfiguredAllowsAll proves the default is unchanged: with no
// policy env, an anonymous caller may deploy.
func TestAPIPolicyUnconfiguredAllowsAll(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CORNUS_API_POLICY", "")

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	spec, _ := json.Marshal(api.DeploySpec{Name: "web", Image: "img"})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(spec))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unconfigured policy: code = %d, want 200", resp.StatusCode)
	}
}

// TestNewRejectsMalformedAPIPolicy proves a malformed CORNUS_API_POLICY is a
// hard startup error (fail closed), not a silent allow-all.
func TestNewRejectsMalformedAPIPolicy(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CORNUS_API_POLICY", "{not valid json")
	if _, err := loadAPIPolicy(); err == nil {
		t.Fatal("expected error for malformed CORNUS_API_POLICY")
	}
}
