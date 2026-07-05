package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"cornus/pkg/authtoken"
)

// ctxKey is a private type for context keys set by the authenticator, so a stored
// value never collides with a key from another package.
type ctxKey int

// subjectKey holds the authenticated caller's identity (a JWT `sub`, or the empty
// string for the static shared-secret token, which carries no identity). It is
// stashed on the request context for potential future authorization; this slice
// does authentication only and reads it nowhere.
const subjectKey ctxKey = iota

// withSubject returns a copy of ctx carrying the authenticated caller identity.
func withSubject(ctx context.Context, sub string) context.Context {
	return context.WithValue(ctx, subjectKey, sub)
}

// Subject returns the authenticated caller identity stored on the request context
// by the auth middleware, or "" when there is none (auth disabled, static-token
// caller, or anonymous pull).
func Subject(r *http.Request) string {
	if v, ok := r.Context().Value(subjectKey).(string); ok {
		return v
	}
	return ""
}

// Identity returns the authenticated caller identity: the mTLS client-cert
// CommonName or the JWT `sub`, whichever the auth middleware established. It is
// the single accessor the API-authorization checks call, so one identity model
// spans both credential types. It is "" for an anonymous or opaque-static-token
// caller (which carry no identity) and when auth is disabled.
func Identity(r *http.Request) string { return Subject(r) }

// jwtVerifier is one configured way to verify a JWT: a set of allowed signature
// algorithms bound to the single key that verifies them. Binding the algorithms
// to the key is what makes verification algorithm-confusion-safe — an HS256 token
// can never be checked against an asymmetric public key, because a public-key
// verifier only ever allows RS256/ES256.
type jwtVerifier struct {
	algs []jose.SignatureAlgorithm
	key  any // []byte for HS256; *rsa.PublicKey / *ecdsa.PublicKey for RS256/ES256
}

// authenticator enforces bearer authentication on the mux. It is nil/disabled
// unless at least one verifier is configured from the environment, in which case
// wrap is a pure pass-through and the server behaves exactly as it did before.
type authenticator struct {
	staticToken []byte // full-access opaque shared secret; nil when unset
	jwt         []jwtVerifier
	issuer      string
	audience    string
	anonPull    bool

	// caretakerToken is a SCOPED credential that authenticates ONLY the caretaker
	// endpoint (/.cornus/v1/caretaker/attach), never the client API or the registry. The
	// in-cluster caretaker sidecar carries it in its pod spec, so it must not be
	// able to deploy, build, exec, or push/pull images if it leaks. Full
	// credentials (staticToken / jwt) still authenticate every endpoint, the
	// caretaker one included. nil when unset.
	caretakerToken []byte

	// jwks, when set, resolves JWT verification keys from a JWKS (file or URL) by the
	// token's kid, with rotation. It complements the single-key jwt verifiers above.
	jwks *jwksResolver

	// mtls is true when a client-cert CA is configured (CORNUS_TLS_CLIENT_CA),
	// which turns auth on even if no bearer verifier is set. A VERIFIED client
	// certificate is then a FULL credential: authenticate reads its CommonName as
	// the caller identity, taking precedence over any bearer token. The TLS layer
	// (VerifyClientCertIfGiven) only ever populates verifiedIdentity when this CA
	// is configured, so the two stay self-consistent.
	mtls bool
}

// enabled reports whether any verifier is configured. When false, wrap returns the
// wrapped handler unchanged (zero per-request cost).
func (a *authenticator) enabled() bool {
	return a != nil && (len(a.staticToken) > 0 || len(a.jwt) > 0 || len(a.caretakerToken) > 0 || a.jwks != nil || a.mtls)
}

// newAuthenticator builds the authenticator from the environment. It returns an
// error only when a configured input is malformed (e.g. an unreadable or
// unparseable public-key PEM), so a broken auth config is a hard startup failure
// rather than a silent open door. With no auth env set it returns a disabled
// authenticator.
func newAuthenticator() (*authenticator, error) {
	a := &authenticator{
		issuer:   os.Getenv("CORNUS_JWT_ISSUER"),
		audience: os.Getenv("CORNUS_JWT_AUDIENCE"),
		anonPull: parseBoolEnv(os.Getenv("CORNUS_REGISTRY_ANONYMOUS_PULL")),
	}

	if tok := os.Getenv("CORNUS_AUTH_TOKEN"); tok != "" {
		a.staticToken = []byte(tok)
	}

	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
		a.caretakerToken = []byte(tok)
	}

	// A configured client-cert CA makes verified mTLS an additional credential.
	// The CA itself is read and wired into the TLS listener in Server.Run; here we
	// only record that the method is on so enabled()/authenticate account for it.
	a.mtls = os.Getenv("CORNUS_TLS_CLIENT_CA") != ""

	jwks, err := newJWKSResolver()
	if err != nil {
		return nil, err
	}
	a.jwks = jwks

	if secret := os.Getenv("CORNUS_JWT_HS256_SECRET"); secret != "" {
		a.jwt = append(a.jwt, jwtVerifier{
			algs: []jose.SignatureAlgorithm{jose.HS256},
			key:  []byte(secret),
		})
	}

	if path := os.Getenv("CORNUS_JWT_PUBLIC_KEY"); path != "" {
		v, err := loadPublicKeyVerifier(path)
		if err != nil {
			return nil, fmt.Errorf("CORNUS_JWT_PUBLIC_KEY: %w", err)
		}
		a.jwt = append(a.jwt, v)
	}

	return a, nil
}

// loadPublicKeyVerifier reads a PEM public key and binds it to exactly the one
// asymmetric algorithm its type supports: RSA -> RS256, ECDSA -> ES256. HS256 is
// deliberately NOT among the allowed algorithms, which is what defeats the
// algorithm-confusion attack (an attacker HMAC-signing a token with the public-key
// bytes cannot pass, because this verifier never allows HS256).
func loadPublicKeyVerifier(path string) (jwtVerifier, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return jwtVerifier{}, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return jwtVerifier{}, errors.New("no PEM block found")
	}

	var pub any
	switch block.Type {
	case "PUBLIC KEY":
		pub, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
	case "CERTIFICATE":
		var cert *x509.Certificate
		cert, err = x509.ParseCertificate(block.Bytes)
		if err == nil {
			pub = cert.PublicKey
		}
	default:
		return jwtVerifier{}, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
	if err != nil {
		return jwtVerifier{}, err
	}

	switch pub.(type) {
	case *rsa.PublicKey:
		return jwtVerifier{algs: []jose.SignatureAlgorithm{jose.RS256}, key: pub}, nil
	case *ecdsa.PublicKey:
		return jwtVerifier{algs: []jose.SignatureAlgorithm{jose.ES256}, key: pub}, nil
	default:
		return jwtVerifier{}, fmt.Errorf("unsupported public key type %T", pub)
	}
}

// wrap returns h guarded by bearer authentication. When no verifier is configured
// it returns h unchanged — no header parsing, no allocation, identical behavior to
// the no-auth build.
func (a *authenticator) wrap(h http.Handler) http.Handler {
	if !a.enabled() {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Liveness/readiness (and the optional Prometheus scrape endpoint) are
		// always open — they must answer even when a probe or scraper carries no
		// credentials. /metrics is only ever registered when the Prometheus
		// exporter is enabled, so exempting it here is a no-op otherwise. /.cornus/v1/info
		// is open too: a client discovers the advertised registry host from it
		// before it has necessarily resolved a token.
		if path == "/healthz" || path == "/readyz" || path == "/metrics" || path == "/.cornus/v1/info" {
			h.ServeHTTP(w, r)
			return
		}

		isRegistry := path == "/v2" || strings.HasPrefix(path, "/v2/")

		// Anonymous pull: when enabled, unauthenticated GET/HEAD under /v2/* is
		// allowed (image pull); push/delete and everything under /.cornus/v1/* still
		// require a token. A credential presented anyway is verified BEST-EFFORT
		// so the caller identity still reaches downstream authz (the opt-in
		// registry pull policy needs it); an absent or invalid credential falls
		// back to anonymous instead of a 401, keeping anonymous-pull semantics.
		if isRegistry && a.anonPull && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if sub, ok := a.authenticate(r, false, true); ok && sub != "" {
				r = r.WithContext(withSubject(r.Context(), sub))
			}
			h.ServeHTTP(w, r)
			return
		}

		// The caretaker endpoint additionally accepts the scoped caretaker token;
		// every other endpoint requires a full credential (and rejects the
		// caretaker token). Registry endpoints additionally accept the same
		// credential framed as HTTP Basic (docker login support).
		caretakerScope := path == "/.cornus/v1/caretaker/attach"
		sub, ok := a.authenticate(r, caretakerScope, isRegistry)
		if !ok {
			a.challenge(w, isRegistry)
			return
		}
		if sub != "" {
			r = r.WithContext(withSubject(r.Context(), sub))
		}
		h.ServeHTTP(w, r)
	})
}

// authenticate verifies the request's bearer token. It returns the caller identity
// (the JWT `sub`, or "" for a static token) and whether authentication succeeded.
// When caretakerScope is true (the /.cornus/v1/caretaker/attach endpoint), the scoped
// caretaker token is accepted; on every other endpoint it is NOT tried, so a leaked
// caretaker credential cannot reach the client API or the registry. Full
// credentials (static token, then JWT) are accepted on every endpoint.
//
// When allowBasic is true (registry endpoints, /v2/*), an HTTP Basic header is
// accepted as an alternative FRAMING of the same credential: the PASSWORD is the
// opaque static token or JWT this authenticator already verifies, and the
// username is ignored (a hint only, conventionally "token"). This is what makes
// `docker login <cornus> -u token -p $CORNUS_TOKEN` work with a stock docker
// client — there is no separate credential store, and Basic passwords go
// through exactly the verifier chain below (so a caretaker-scoped credential
// framed as Basic is still rejected on the registry).
func (a *authenticator) authenticate(r *http.Request, caretakerScope, allowBasic bool) (subject string, ok bool) {
	// A VERIFIED client certificate is a full credential and wins over any bearer
	// token when both are present. verifiedIdentity is only ever non-empty when a
	// client CA was configured (the TLS layer verified the chain), so this check
	// is self-consistent with the listener setup and needs no extra gate.
	if id := verifiedIdentity(r); id != "" {
		return id, true
	}

	token, ok := bearerToken(r)
	if !ok && allowBasic {
		if _, pw, hasBasic := r.BasicAuth(); hasBasic && pw != "" {
			token, ok = pw, true
		}
	}
	if !ok {
		return "", false
	}

	// Full static token: authenticates every endpoint.
	if len(a.staticToken) > 0 && subtle.ConstantTimeCompare([]byte(token), a.staticToken) == 1 {
		return "", true
	}

	// Scoped static caretaker token: authenticates ONLY the caretaker endpoint.
	if len(a.caretakerToken) > 0 && subtle.ConstantTimeCompare([]byte(token), a.caretakerToken) == 1 {
		return "", caretakerScope
	}

	// JWTs: a caretaker-scoped token is likewise restricted to the caretaker
	// endpoint; any other (or unscoped) token is a full credential. Try the
	// single-key verifiers, then the JWKS (kid-selected) verifier.
	scopedResult := func(sub, scope string) (string, bool) {
		if authtoken.CaretakerOnly(scope) {
			return sub, caretakerScope
		}
		return sub, true
	}
	for _, v := range a.jwt {
		if sub, scope, ok := a.verifyJWT(v, token); ok {
			return scopedResult(sub, scope)
		}
	}
	if a.jwks != nil {
		if sub, scope, ok := a.verifyJWKS(token); ok {
			return scopedResult(sub, scope)
		}
	}
	return "", false
}

// verifyJWT parses and validates token against a single verifier. ParseSigned is
// given the verifier's allowed algorithms, so a token whose header alg is not
// permitted (including `none`) is rejected before any signature check.
func (a *authenticator) verifyJWT(v jwtVerifier, token string) (subject, scope string, ok bool) {
	parsed, err := jwt.ParseSigned(token, v.algs)
	if err != nil {
		return "", "", false
	}
	var claims authtoken.Claims
	if err := parsed.Claims(v.key, &claims); err != nil {
		return "", "", false
	}
	if !a.validClaims(claims) {
		return "", "", false
	}
	return claims.Subject, claims.Scope, true
}

// verifyJWKS parses token, selects the JWKS key by its `kid`, and verifies. Only
// asymmetric algorithms are permitted (jwksAlgs), so HS256-with-a-public-key
// confusion cannot pass. An unknown kid triggers one rate-limited key-set refresh
// (rotation) inside find.
func (a *authenticator) verifyJWKS(token string) (subject, scope string, ok bool) {
	parsed, err := jwt.ParseSigned(token, jwksAlgs)
	if err != nil || len(parsed.Headers) == 0 {
		return "", "", false
	}
	jwk, err := a.jwks.find(parsed.Headers[0].KeyID)
	if err != nil {
		return "", "", false
	}
	var claims authtoken.Claims
	if err := parsed.Claims(jwk.Key, &claims); err != nil {
		return "", "", false
	}
	if !a.validClaims(claims) {
		return "", "", false
	}
	return claims.Subject, claims.Scope, true
}

// validClaims checks the registered claims (exp/nbf with a short leeway, plus
// iss/aud when configured), shared by the single-key and JWKS verify paths.
func (a *authenticator) validClaims(claims authtoken.Claims) bool {
	// A token with no `exp` is a credential that never expires. go-jose only
	// checks expiry when Expiry is set (c.Expiry != nil), so an omitted exp would
	// otherwise pass validation forever — defeating rotation/revocation-by-expiry.
	// Require exp to be present explicitly before any other check.
	if claims.Expiry == nil {
		return false
	}
	expected := jwt.Expected{Time: time.Now()}
	if a.issuer != "" {
		expected.Issuer = a.issuer
	}
	if a.audience != "" {
		expected.AnyAudience = jwt.Audience{a.audience}
	}
	return claims.ValidateWithLeeway(expected, time.Minute) == nil
}

// challenge writes a 401 with the appropriate WWW-Authenticate header: a Basic
// challenge for /v2/* and a plain Bearer for /.cornus/v1/*.
//
// The registry challenge is Basic (not Bearer) deliberately: cornus has no
// token service, so a Bearer challenge sends `docker login` off to fetch a
// token from a realm that does not exist and the login fails. A Basic challenge
// makes a stock docker/podman client retry with the docker-login credentials,
// whose password authenticate accepts as the bearer credential. Clients holding
// a bearer token (crane / go-containerregistry with authn.Bearer) still work:
// they answer a Basic challenge by setting their own `Authorization: Bearer`
// header (see transport/basic.go's RegistryToken handling) and never perform a
// token exchange.
func (a *authenticator) challenge(w http.ResponseWriter, isRegistry bool) {
	if isRegistry {
		w.Header().Set("WWW-Authenticate", `Basic realm="cornus"`)
	} else {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// parseBoolEnv reports whether a string is an affirmative flag value.
func parseBoolEnv(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
