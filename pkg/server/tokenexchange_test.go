package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/authscope"
	"cornus/pkg/authtoken"
)

// exchangeServer builds a server whose only verifier is a JWKS holding k, with
// the given scope map. That is the shape the endpoint exists for: a third party's
// key set, and a policy saying what its subjects may do.
func exchangeServer(t *testing.T, k ecKey, m *authscope.Map) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(path, jwksOf(t, k), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &authenticator{
		jwks:           &jwksResolver{src: &jwksFileSource{path: path}},
		scopeMap:       m,
		internalSecret: []byte("this-is-a-32-byte-minimum-secret!!"),
	}
	a.exchangeJWT = &jwtVerifier{algs: jwtHS256Algs(), key: a.internalSecret}
	s := &Server{auth: a, mux: http.NewServeMux()}
	s.registerTokenExchangeRoute()
	return s
}

func postExchange(t *testing.T, s *Server, form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/.cornus/v1/auth/exchange", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func exchangeForm(subjectToken string) url.Values {
	return url.Values{
		"grant_type":         {grantTypeTokenExchange},
		"subject_token":      {subjectToken},
		"subject_token_type": {tokenTypeJWT},
	}
}

func ciScopeMap(t *testing.T, scope string) *authscope.Map {
	t.Helper()
	return mustScopeMap(t, `
rules:
  - name: ci runners
    scope: `+scope+`
    match:
      sub: { prefix: "system:serviceaccount:ci:" }
`)
}

func ciSubjectToken(t *testing.T, k ecKey) string {
	t.Helper()
	return mintES256(t, k.priv, "k1", map[string]any{"sub": "system:serviceaccount:ci:runner"})
}

// TestExchangeIssuesMappedScope is the happy path, and it checks the thing that
// makes the exchange worth having: the credential that comes back is
// cornus-minted and NAMES its scope, so the request path never has to consult the
// map again.
func TestExchangeIssuesMappedScope(t *testing.T) {
	k := genEC(t, "k1")
	s := exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeRegistryPush))

	rec, body := postExchange(t, s, exchangeForm(ciSubjectToken(t, k)))
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange: code = %d, body = %v", rec.Code, body)
	}
	if body["scope"] != authtoken.ScopeRegistryPush {
		t.Fatalf("issued scope = %v, want %s", body["scope"], authtoken.ScopeRegistryPush)
	}
	if body["token_type"] != "Bearer" {
		t.Fatalf("token_type = %v, want Bearer", body["token_type"])
	}
	if body["issued_token_type"] != tokenTypeAccessToken {
		t.Fatalf("issued_token_type = %v", body["issued_token_type"])
	}

	// The issued token must authenticate on the endpoints its scope names, and
	// only those — verified through the real middleware rather than by reading the
	// claims back, since what matters is that the request path accepts it.
	tok, _ := body["access_token"].(string)
	h := s.auth.wrap(okHandler())
	if rec := doReq(t, h, http.MethodGet, "/v2/app/manifests/latest", tok); rec.Code != http.StatusOK {
		t.Fatalf("exchanged token on registry: code = %d, want 200", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("exchanged registry:push token on deploy: code = %d, want 401", rec.Code)
	}
}

// TestExchangeDownscopeOnly: a requested scope may narrow what policy granted and
// may never widen it. The widening case is the security-relevant half.
func TestExchangeDownscopeOnly(t *testing.T) {
	k := genEC(t, "k1")
	subject := ciSubjectToken(t, k)

	// Policy grants api; the client asks for registry:pull and gets exactly that.
	s := exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeAPI))
	form := exchangeForm(subject)
	form.Set("scope", authtoken.ScopeRegistryPull)
	rec, body := postExchange(t, s, form)
	if rec.Code != http.StatusOK || body["scope"] != authtoken.ScopeRegistryPull {
		t.Fatalf("downscope: code = %d, scope = %v", rec.Code, body["scope"])
	}
	tok, _ := body["access_token"].(string)
	h := s.auth.wrap(okHandler())
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("downscoped token still reached deploy: code = %d — the narrowing did not take effect", rec.Code)
	}

	// Policy grants registry:pull; the client asks for api. Refused — a request
	// parameter must never be able to exceed what the operator's map said.
	s = exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeRegistryPull))
	form = exchangeForm(subject)
	form.Set("scope", authtoken.ScopeAPI)
	rec, body = postExchange(t, s, form)
	if rec.Code != http.StatusBadRequest || body["error"] != "invalid_scope" {
		t.Fatalf("upscope: code = %d, body = %v, want 400 invalid_scope", rec.Code, body)
	}

	// registry:push does not contain caretaker and caretaker does not contain
	// push: neither is "weaker". Asking to cross between them is refused, which is
	// what containment-by-endpoint buys over an invented ordering.
	s = exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeRegistryPull))
	form = exchangeForm(subject)
	form.Set("scope", authtoken.ScopeRegistryPush)
	rec, _ = postExchange(t, s, form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pull -> push: code = %d, want 400", rec.Code)
	}
}

// TestExchangeRefusesNonClientScopes: caretaker and peer are not client
// credentials and the endpoint will not mint them, whether the map grants one or
// a client asks to downscope into one.
//
// The downscope direction is the subtle half: caretaker IS contained in api under
// the access matrix, so containment alone would let a client entitled to full
// access mint itself a caretaker credential — a surface it never talks to.
func TestExchangeRefusesNonClientScopes(t *testing.T) {
	k := genEC(t, "k1")
	subject := ciSubjectToken(t, k)

	for _, scope := range []string{authtoken.ScopeCaretaker, authtoken.ScopePeer} {
		// Mapped to it: refused at issue time.
		s := exchangeServer(t, k, ciScopeMap(t, scope))
		rec, body := postExchange(t, s, exchangeForm(subject))
		if rec.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("mapped to %s: code = %d, body = %v, want 400 invalid_grant", scope, rec.Code, body)
		}

		// Requested as a downscope from api: also refused.
		s = exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeAPI))
		form := exchangeForm(subject)
		form.Set("scope", scope)
		rec, body = postExchange(t, s, form)
		if rec.Code != http.StatusBadRequest || body["error"] != "invalid_scope" {
			t.Fatalf("downscope to %s: code = %d, body = %v, want 400 invalid_scope", scope, rec.Code, body)
		}
	}
}

// TestExchangeRejectsUnentitledAndUnverifiable covers the two ways a subject
// token fails: it does not verify, or it verifies and no rule names it. Both are
// invalid_grant, and neither may produce a token.
func TestExchangeRejectsUnentitledAndUnverifiable(t *testing.T) {
	k := genEC(t, "k1")
	s := exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeAPI))

	// Verifies, but no rule matches.
	unmapped := mintES256(t, k.priv, "k1", map[string]any{"sub": "system:serviceaccount:other:runner"})
	rec, body := postExchange(t, s, exchangeForm(unmapped))
	if rec.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("unmapped subject: code = %d, body = %v", rec.Code, body)
	}
	if _, ok := body["access_token"]; ok {
		t.Fatal("unmapped subject received an access_token")
	}

	// Signed by a key the JWKS does not hold.
	other := genEC(t, "k1") // same kid, different key
	forged := mintES256(t, other.priv, "k1", map[string]any{"sub": "system:serviceaccount:ci:runner"})
	rec, body = postExchange(t, s, exchangeForm(forged))
	if rec.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("unverifiable subject: code = %d, body = %v", rec.Code, body)
	}
}

// TestExchangeProtocolErrors pins the RFC-shaped rejections, so a standard client
// gets a diagnosis rather than a bare 400.
func TestExchangeProtocolErrors(t *testing.T) {
	k := genEC(t, "k1")
	s := exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeAPI))
	subject := ciSubjectToken(t, k)

	for _, tc := range []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{"wrong grant type", func(f url.Values) { f.Set("grant_type", "client_credentials") }, "unsupported_grant_type"},
		{"no subject token", func(f url.Values) { f.Del("subject_token") }, "invalid_request"},
		{"no subject token type", func(f url.Values) { f.Del("subject_token_type") }, "invalid_request"},
		{"unknown subject token type", func(f url.Values) { f.Set("subject_token_type", "urn:example:saml2") }, "invalid_request"},
		// Delegation is refused rather than ignored: a client that believed it had
		// delegated and had not would be a silent authorization surprise.
		{"delegation", func(f url.Values) { f.Set("actor_token", "x") }, "invalid_request"},
		{"unknown requested token type", func(f url.Values) { f.Set("requested_token_type", "urn:example:saml2") }, "invalid_request"},
		{"unknown scope word", func(f url.Values) { f.Set("scope", "superuser") }, "invalid_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := exchangeForm(subject)
			tc.mutate(form)
			rec, body := postExchange(t, s, form)
			if rec.Code != http.StatusBadRequest || body["error"] != tc.want {
				t.Fatalf("code = %d, body = %v, want 400 %s", rec.Code, body, tc.want)
			}
		})
	}

	// GET is not a token exchange.
	req := httptest.NewRequest(http.MethodGet, "/.cornus/v1/auth/exchange", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: code = %d, want 405", rec.Code)
	}
}

// TestExchangeIsReachableThroughTheAuthMiddleware covers the wiring the other
// tests structurally cannot: they drive s.mux directly, so the auth middleware
// never runs, and a MISSING exemption in wrap() would leave every one of them
// passing while the endpoint 401s every real caller.
//
// The endpoint has to be exempt because the subject token IS the credential —
// requiring a cornus credential to obtain one is a cycle with no entry point.
func TestExchangeIsReachableThroughTheAuthMiddleware(t *testing.T) {
	k := genEC(t, "k1")
	s := exchangeServer(t, k, ciScopeMap(t, authtoken.ScopeAPI))
	// A static token makes the middleware active (enabled() is true), so an
	// unexempted path would answer 401 before reaching the handler.
	s.auth.staticToken = []byte("operator-token")
	h := s.auth.wrap(s.mux)

	form := exchangeForm(ciSubjectToken(t, k))
	req := httptest.NewRequest(http.MethodPost, "/.cornus/v1/auth/exchange", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange through the middleware with NO bearer token: code = %d, body = %q; the endpoint must be exempt", rec.Code, rec.Body.String())
	}
}

// TestExchangeRouteNotRegisteredWithoutVerifier: with nothing to verify a subject
// token against, the endpoint must not exist at all rather than answer 400 —
// otherwise its presence advertises a capability the server does not have.
func TestExchangeRouteNotRegisteredWithoutVerifier(t *testing.T) {
	s := &Server{
		auth: &authenticator{internalSecret: []byte("this-is-a-32-byte-minimum-secret!!")},
		mux:  http.NewServeMux(),
	}
	s.registerTokenExchangeRoute()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/.cornus/v1/auth/exchange", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (route must not be registered)", rec.Code)
	}
}
