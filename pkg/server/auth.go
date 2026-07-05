package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"cornus/pkg/authscope"
	"cornus/pkg/authtoken"
	"cornus/pkg/logging"
)

// caretakerPath is the one endpoint a caretaker-scoped credential may reach.
const caretakerPath = "/.cornus/v1/caretaker/attach"

// ctxKey is a private type for context keys set by the authenticator, so a stored
// value never collides with a key from another package.
type ctxKey int

// subjectKey holds the authenticated caller's identity (a JWT `sub`, or the empty
// string for the static shared-secret token, which carries no identity). It is
// stashed on the request context for potential future authorization; this slice
// does authentication only and reads it nowhere.
const (
	subjectKey ctxKey = iota
	// internalKey is true only for a credential verified by the installation
	// secret. It is never inferred from claims such as sub, which an operator
	// issuer can choose freely.
	internalKey
)

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

func withInternal(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalKey, true)
}

// Internal reports whether the request used a credential signed by this
// installation's internal issuer.
func Internal(r *http.Request) bool {
	v, _ := r.Context().Value(internalKey).(bool)
	return v
}

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

	// scopeMap decides what a token from an EXTERNAL verifier (jwt / jwks) may
	// reach, by matching its claims. It is consulted for those paths only:
	// cornus's own issuers always name a scope, so authtoken.Grant decides there.
	// nil means external tokens grant nothing, which is the correct default —
	// verifying an issuer proves identity and must not by itself confer
	// authority. See pkg/authscope.
	scopeMap *authscope.Map

	// mtls is true when a client-cert CA is configured (--tls-client-ca /
	// CORNUS_TLS_CLIENT_CA), which turns auth on even if no bearer verifier is
	// set. A VERIFIED client certificate is then a FULL credential: authenticate
	// reads its CommonName as the caller identity, taking precedence over any
	// bearer token. The TLS layer (VerifyClientCertIfGiven) only ever populates
	// verifiedIdentity when this CA is configured, so the two stay self-consistent.
	//
	// It is set from the RESOLVED config value, never by re-reading the
	// environment. This used to be `os.Getenv("CORNUS_TLS_CLIENT_CA") != ""`,
	// which meant the two mechanisms could disagree: passing --tls-client-ca as a
	// flag with the env unset configured certificate verification while leaving
	// this false, so the server verified client certs with auth switched off —
	// the opposite of what the flag documents.
	mtls bool

	// internal verifies credentials minted by this server for its own dataplane.
	// It is deliberately excluded from enabled(): internal auth must never turn
	// operator authentication on by itself.
	internal           *jwtVerifier
	internalSecret     []byte
	internalProvenance installationSecretProvenance

	// sshKeys verifies enrolled SSH public keys and owns the optional writable
	// authorized_keys store. A nil store means SSH-key auth is unconfigured.
	sshKeys *sshKeyStore
	sshJWT  *jwtVerifier

	// exchangeJWT verifies the credentials this server minted at
	// /.cornus/v1/auth/exchange. Like internal and sshJWT it rides the
	// installation secret and is excluded from enabled(): being able to verify
	// one's own minted tokens must never, by itself, turn operator auth on.
	exchangeJWT *jwtVerifier

	// peerKeyLookup resolves the public signing identity of a live replica from
	// the distributed hub store. It is deliberately excluded from enabled(): peer
	// trust supplements operator-configured auth and must never enable auth by
	// itself.
	peerKeyLookup func(replicaID string) ([]byte, bool, error)
}

// enabled reports whether any verifier is configured. When false, wrap returns the
// wrapped handler unchanged (zero per-request cost).
func (a *authenticator) enabled() bool {
	return a != nil && (len(a.staticToken) > 0 || len(a.jwt) > 0 || len(a.caretakerToken) > 0 || a.jwks != nil || a.mtls || a.sshKeys != nil)
}

// newAuthenticator builds the authenticator from the environment. It returns an
// error only when a configured input is malformed (e.g. an unreadable or
// unparseable public-key PEM), so a broken auth config is a hard startup failure
// rather than a silent open door. With no auth env set it returns a disabled
// authenticator.
func newAuthenticator(dataDir, tlsClientCA string) (*authenticator, error) {
	a := &authenticator{
		issuer:   jwtIssuer(),
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
	// The caller resolves the value (flag or env) — see the mtls field comment for
	// why this must not re-read the environment.
	a.mtls = tlsClientCA != ""

	jwks, err := newJWKSResolver()
	if err != nil {
		return nil, err
	}
	a.jwks = jwks

	sshKeys, err := newSSHKeyStore(dataDir)
	if err != nil {
		return nil, err
	}
	a.sshKeys = sshKeys

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

	// The scope map, and the one-env-var catch-all that stands for a single
	// match-everything rule. The default is APPENDED, so an explicit file keeps
	// deciding and the catch-all only picks up what no rule of its own claimed.
	// Both are validated here: a policy that failed to load must stop the server,
	// never degrade to a server that silently grants nothing (or everything).
	if path := os.Getenv("CORNUS_JWT_SCOPE_MAP"); path != "" {
		m, err := authscope.Load(path)
		if err != nil {
			return nil, fmt.Errorf("CORNUS_JWT_SCOPE_MAP: %w", err)
		}
		a.scopeMap = m
	}
	if scope := strings.TrimSpace(os.Getenv("CORNUS_JWT_DEFAULT_SCOPE")); scope != "" {
		m, err := authscope.DefaultScopeMap(scope)
		if err != nil {
			return nil, fmt.Errorf("CORNUS_JWT_DEFAULT_SCOPE: %w", err)
		}
		a.scopeMap = a.scopeMap.Append(m)
	}

	// Preserve the zero-cost default exactly: no installation key is read or
	// generated until an operator verifier has already enabled auth.
	if a.enabled() {
		secret, provenance, err := loadInstallationSecret(dataDir)
		if err != nil {
			return nil, err
		}
		a.internalSecret = secret
		a.internalProvenance = provenance
		a.internal = &jwtVerifier{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}
		if a.sshKeys != nil {
			a.sshJWT = &jwtVerifier{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}
		}
		a.exchangeJWT = &jwtVerifier{algs: []jose.SignatureAlgorithm{jose.HS256}, key: secret}
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
		// The three /auth/ endpoints carry their OWN proof — an SSH-key signature
		// or, for the exchange, a third party's token — so they cannot sit behind
		// the middleware that would demand a cornus credential to obtain one.
		if path == "/healthz" || path == "/readyz" || path == "/metrics" || path == "/.cornus/v1/info" ||
			path == "/.cornus/v1/auth/enroll" || path == "/.cornus/v1/auth/token" ||
			path == "/.cornus/v1/auth/exchange" {
			h.ServeHTTP(w, r)
			return
		}

		endpoint := endpointForRequest(r)
		isRegistry := endpoint == authtoken.EndpointRegistryPull || endpoint == authtoken.EndpointRegistryPush

		// Anonymous pull: when enabled, unauthenticated GET/HEAD under /v2/* is
		// allowed (image pull); push/delete and everything under /.cornus/v1/* still
		// require a token. A credential presented anyway is verified BEST-EFFORT
		// so the caller identity still reaches downstream authz (the opt-in
		// registry pull policy needs it); an absent or invalid credential falls
		// back to anonymous instead of a 401, keeping anonymous-pull semantics.
		if endpoint == authtoken.EndpointRegistryPull && a.anonPull {
			if sub, _, internal, ok := a.authenticate(r, endpoint, true); ok {
				if sub != "" {
					r = r.WithContext(withSubject(r.Context(), sub))
				}
				if internal {
					r = r.WithContext(withInternal(r.Context()))
				}
			}
			h.ServeHTTP(w, r)
			return
		}

		// The caretaker endpoint additionally accepts the scoped caretaker token;
		// every other endpoint requires a full credential (and rejects the
		// caretaker token). Registry endpoints additionally accept the same
		// credential framed as HTTP Basic (docker login support).
		sub, reason, internal, ok := a.authenticate(r, endpoint, isRegistry)
		if !ok {
			a.challenge(w, isRegistry, reason)
			return
		}
		if sub != "" {
			r = r.WithContext(withSubject(r.Context(), sub))
		}
		if internal {
			r = r.WithContext(withInternal(r.Context()))
		}
		h.ServeHTTP(w, r)
	})
}

// endpointForRequest classifies a request for the scope matrix. Registry reads
// are GET/HEAD; every other registry method is a write. Unknown non-registry
// paths are API endpoints so full client credentials retain their existing
// behavior.
func endpointForRequest(r *http.Request) authtoken.Endpoint {
	path := r.URL.Path
	switch path {
	case "/.cornus/v1/hub/forward", "/.cornus/v1/mount/forward", "/.cornus/v1/cred/forward":
		return authtoken.EndpointPeerForward
	}
	if path == caretakerPath {
		return authtoken.EndpointCaretaker
	}
	if path == "/v2" || strings.HasPrefix(path, "/v2/") {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return authtoken.EndpointRegistryPull
		}
		return authtoken.EndpointRegistryPush
	}
	return authtoken.EndpointAPI
}

// authenticate verifies the request's bearer token. It returns the caller identity
// (the JWT `sub`, or "" for a static token), an operator-facing reason when a
// CRYPTOGRAPHICALLY VALID credential was nonetheless refused (so the 401 can say
// why instead of looking like a signature failure), and whether authentication
// succeeded. The reason is empty for an absent or unverifiable credential, which
// must stay opaque.
//
// endpoint selects the fail-closed scope matrix. Full credentials (mTLS and the
// static token) are accepted on every endpoint; scoped credentials are accepted
// only when authtoken.Allows says so.
//
// When allowBasic is true (registry endpoints, /v2/*), an HTTP Basic header is
// accepted as an alternative FRAMING of the same credential: the PASSWORD is the
// opaque static token or JWT this authenticator already verifies, and the
// username is ignored (a hint only, conventionally "token"). This is what makes
// `docker login <cornus> -u token -p $CORNUS_TOKEN` work with a stock docker
// client — there is no separate credential store, and Basic passwords go
// through exactly the verifier chain below (so a caretaker-scoped credential
// framed as Basic is still rejected on the registry).
func (a *authenticator) authenticate(r *http.Request, endpoint authtoken.Endpoint, allowBasic bool) (subject, reason string, internal, ok bool) {
	// A VERIFIED client certificate is a full credential and wins over any bearer
	// token when both are present. verifiedIdentity is only ever non-empty when a
	// client CA was configured (the TLS layer verified the chain), so this check
	// is self-consistent with the listener setup and needs no extra gate.
	if id := verifiedIdentity(r); id != "" {
		return id, "", false, true
	}

	token, ok := bearerToken(r)
	if !ok && allowBasic {
		if _, pw, hasBasic := r.BasicAuth(); hasBasic && pw != "" {
			token, ok = pw, true
		}
	}
	if !ok {
		return "", "", false, false
	}

	// Full static token: authenticates every endpoint.
	if len(a.staticToken) > 0 && subtle.ConstantTimeCompare([]byte(token), a.staticToken) == 1 {
		return "", "", false, true
	}

	// Scoped static caretaker token: authenticates ONLY the caretaker endpoint.
	if len(a.caretakerToken) > 0 && subtle.ConstantTimeCompare([]byte(token), a.caretakerToken) == 1 {
		if endpoint == authtoken.EndpointCaretaker {
			return "", "", false, true
		}
		return "", "the caretaker token is scoped to " + caretakerPath + " and cannot be used on this endpoint", false, false
	}

	// JWTs cornus MINTED: what they may reach is decided ONLY by their scope claim
	// (authtoken.Grant, the same model `cornus token issue` mints against). The
	// scope is an allowlist — a token that names neither `api` nor `caretaker`,
	// including one carrying no scope claim at all, is refused outright rather
	// than treated as a full credential. authtoken.Issue refuses to mint a
	// scopeless token, so on these paths a missing scope is a real anomaly.
	scopedResult := func(sub, scope string, internal bool) (string, string, bool, bool) {
		access, err := authtoken.Grant(scope)
		if err != nil {
			// The signature checked out, so this is a configuration/minting
			// mistake an operator needs to see, not attacker noise.
			a.logScopeDenial(r, sub, scope, err.Error())
			return "", "rejected: " + err.Error(), false, false
		}
		if !authtoken.Allows(access, endpoint) {
			msg := "token scope grants only " + access.String() + " access, which does not allow the " + endpoint.String() + " endpoint"
			a.logScopeDenial(r, sub, scope, msg)
			return "", "rejected: " + msg, false, false
		}
		return sub, "", internal, true
	}
	// JWTs a THIRD PARTY issued: the scope map decides, always, and the token's
	// own scope claim grants nothing by itself.
	//
	// The dividing line is who holds the signing key. A scope claim is an
	// assertion by whoever signed the token, so it is authoritative exactly when
	// cornus's own operator controls that key — the installation secret
	// (internal/sshJWT) and the keys configured for `cornus token issue`
	// (CORNUS_JWT_HS256_SECRET, CORNUS_JWT_PUBLIC_KEY). A JWKS verifier is
	// definitionally the other case: it points at someone else's key set, and the
	// operator cannot mint with it.
	//
	// Honoring a third party's scope would let any issuer trusted to prove
	// IDENTITY also grant itself AUTHORITY — emitting `scope: api`, or carrying
	// permissions that mean something else entirely in its own vocabulary, with
	// no rule of the operator's ever matching. Configuring a verifier must not do
	// that. The claim stays matchable like any other, so a cooperating issuer can
	// still be honored — explicitly, by a rule that says so.
	externalResult := func(sub string, claims map[string]any) (string, string, bool, bool) {
		scope, rule, err := a.scopeMap.Match(claims)
		if err != nil {
			// Both messages name the two ways out, because the operator reading
			// this 401 has to know whether to fix the TOKEN or the POLICY, and
			// which of the two is missing is exactly what distinguishes them.
			msg := "no scope-map rule matched this token's claims, so it grants nothing; add a matching CORNUS_JWT_SCOPE_MAP rule, or mint a cornus token that names its scope (`cornus token issue --scope " + authtoken.ScopeAPI + "`)"
			if a.scopeMap == nil {
				msg = "this token names no cornus scope and this server has no scope map, so it grants nothing; mint a cornus token that names its scope (`cornus token issue --scope " + authtoken.ScopeAPI + "`), or map the issuer's claims with CORNUS_JWT_SCOPE_MAP (CORNUS_JWT_DEFAULT_SCOPE=" + authtoken.ScopeAPI + " for a catch-all)"
			}
			a.logScopeDenial(r, sub, claimString(claims, "scope"), msg)
			return "", "rejected: " + msg, false, false
		}
		access, err := authtoken.Grant(scope)
		if err != nil {
			// Unreachable via Load/Parse, which validate every rule's scope
			// against this same function — but a map built any other way must
			// not fail open.
			a.logScopeDenial(r, sub, scope, err.Error())
			return "", "rejected: " + err.Error(), false, false
		}
		if !authtoken.Allows(access, endpoint) {
			msg := "scope-map rule " + rule + " grants only " + access.String() + " access, which does not allow the " + endpoint.String() + " endpoint"
			a.logScopeDenial(r, sub, scope, msg)
			return "", "rejected: " + msg, false, false
		}
		a.logScopeMapped(r, sub, rule, scope)
		return sub, "", false, true
	}
	if a.internal != nil {
		if sub, scope, ok := a.verifyInternal(token); ok {
			return scopedResult(sub, scope, true)
		}
	}
	if a.sshJWT != nil {
		if sub, scope, ok := verifyFixedJWT(*a.sshJWT, token, sshKeyIssuer, sshKeyAudience); ok {
			return scopedResult(sub, scope, false)
		}
	}
	// A credential this server minted at /.cornus/v1/auth/exchange. It is signed
	// with the installation secret and pinned to its own issuer/audience, so it is
	// cornus-issued by construction and its scope claim decides — the scope map
	// already ran, once, when it was issued.
	if a.exchangeJWT != nil {
		if sub, scope, ok := verifyFixedJWT(*a.exchangeJWT, token, exchangeIssuer, exchangeAudience); ok {
			return scopedResult(sub, scope, false)
		}
	}
	// An operator-configured KEY: the operator holds it (it is what `cornus token
	// issue --hs256-secret` / `--private-key` signs with), so a cornus scope it
	// asserts is the operator's own assertion and decides. A token from such a key
	// that names NO cornus scope falls through to the map instead of being
	// refused — that is the foreign-IdP-behind-a-public-key case, and refusing it
	// would leave it with no way to be granted anything at all.
	for _, v := range a.jwt {
		if sub, claims, ok := a.verifyJWT(v, token); ok {
			scope := claimString(claims, "scope")
			if _, err := authtoken.Grant(scope); err == nil {
				return scopedResult(sub, scope, false)
			}
			return externalResult(sub, claims)
		}
	}
	if a.jwks != nil {
		if sub, claims, ok := a.verifyJWKS(token); ok {
			return externalResult(sub, claims)
		}
	}
	// Peer credentials are tried after operator JWTs so an operator-issued ES256
	// token with a kid remains usable on this endpoint. They are accepted nowhere
	// else, even before the scope matrix is consulted.
	if endpoint == authtoken.EndpointPeerForward && a.peerKeyLookup != nil {
		if sub, scope, err, ok := a.verifyPeerJWT(token); err != nil {
			logging.FromContext(r.Context(), slog.String("component", "auth")).Warn(
				"rejected a peer JWT because its signing key could not be resolved",
				"method", r.Method, "path", r.URL.Path, "error", err)
			return "", "", false, false
		} else if ok {
			return scopedResult(sub, scope, false)
		}
	}
	return "", "", false, false
}

func (a *authenticator) verifyInternal(token string) (subject, scope string, ok bool) {
	return verifyFixedJWT(*a.internal, token, internalIssuer, internalAudience)
}

func verifyFixedJWT(verifier jwtVerifier, token, issuer, audience string) (subject, scope string, ok bool) {
	parsed, err := jwt.ParseSigned(token, verifier.algs)
	if err != nil {
		return "", "", false
	}
	var claims authtoken.Claims
	if err := parsed.Claims(verifier.key, &claims); err != nil {
		return "", "", false
	}
	if !validClaimsFor(claims, issuer, audience) {
		return "", "", false
	}
	return claims.Subject, claims.Scope, true
}

// logScopeDenial records a token that verified cryptographically but was refused
// on scope. Without it a scope mistake is indistinguishable from a bad signature
// in the server log — both are a bare 401 — and an operator has nothing to act
// on. The token itself is never logged; only its subject and scope claim, which
// are the two things needed to find the offending issuer.
func (a *authenticator) logScopeDenial(r *http.Request, sub, scope, why string) {
	logging.FromContext(r.Context(), slog.String("component", "auth")).Warn(
		"rejected a valid JWT on scope",
		"sub", sub,
		"scope", scope,
		"method", r.Method,
		"path", r.URL.Path,
		"reason", why,
	)
}

// logScopeMapped records which rule granted an externally-issued token its
// access. Without it a scope map is an invisible policy: the 401s are explained
// (logScopeDenial) but the ACCEPTANCES are not, so "why can this ServiceAccount
// deploy" has no answer in the log. Debug rather than Info — it is one line per
// authenticated request on the external path, and the exchange endpoint
// (tokenexchange.go) is what turns this into one line per credential.
func (a *authenticator) logScopeMapped(r *http.Request, sub, rule, scope string) {
	logging.FromContext(r.Context(), slog.String("component", "auth")).Debug(
		"scope map granted an external JWT its access",
		"sub", sub,
		"rule", rule,
		"scope", scope,
		"method", r.Method,
		"path", r.URL.Path,
	)
}

// claimString reads a string claim, tolerating absence and any non-string shape
// by returning "". That empty result is safe on both paths it feeds: a log line,
// and reading `scope` off an operator-key token, where "" is rejected by
// authtoken.Grant and falls through to the scope map rather than granting
// anything. A scope claim that is not a string is not a scope.
func claimString(claims map[string]any, name string) string {
	if v, ok := claims[name]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// verifyJWT parses and validates token against a single verifier. ParseSigned is
// given the verifier's allowed algorithms, so a token whose header alg is not
// permitted (including `none`) is rejected before any signature check.
// The raw claim set is returned alongside the subject because what an
// externally-issued token may reach is decided by the scope map, which matches
// on arbitrary claims (see pkg/authscope). go-jose fills several destinations
// from ONE verification, so capturing it costs an argument and no second parse.
func (a *authenticator) verifyJWT(v jwtVerifier, token string) (subject string, raw map[string]any, ok bool) {
	parsed, err := jwt.ParseSigned(token, v.algs)
	if err != nil {
		return "", nil, false
	}
	var claims authtoken.Claims
	raw = map[string]any{}
	if err := parsed.Claims(v.key, &claims, &raw); err != nil {
		return "", nil, false
	}
	if !a.validClaims(claims) {
		return "", nil, false
	}
	return claims.Subject, raw, true
}

// verifyJWKS parses token, selects the JWKS key by its `kid`, and verifies. Only
// asymmetric algorithms are permitted (jwksAlgs), so HS256-with-a-public-key
// confusion cannot pass. An unknown kid triggers one rate-limited key-set refresh
// (rotation) inside find.
func (a *authenticator) verifyJWKS(token string) (subject string, raw map[string]any, ok bool) {
	parsed, err := jwt.ParseSigned(token, jwksAlgs)
	if err != nil || len(parsed.Headers) == 0 {
		return "", nil, false
	}
	jwk, err := a.jwks.find(parsed.Headers[0].KeyID)
	if err != nil {
		return "", nil, false
	}
	var claims authtoken.Claims
	raw = map[string]any{}
	if err := parsed.Claims(jwk.Key, &claims, &raw); err != nil {
		return "", nil, false
	}
	if !a.validClaims(claims) {
		return "", nil, false
	}
	return claims.Subject, raw, true
}

// verifyPeerJWT verifies an ES256 credential against the public key published
// for its kid. The signed subject must equal kid, binding the credential to the
// replica identity whose liveness record authorized the key lookup.
func (a *authenticator) verifyPeerJWT(token string) (subject, scope string, lookupErr error, ok bool) {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return "", "", nil, false
	}
	if len(parsed.Headers) != 1 || parsed.Headers[0].KeyID == "" {
		return "", "", errors.New("peer JWT has no unique kid"), false
	}
	kid := parsed.Headers[0].KeyID
	publicPEM, found, err := a.peerKeyLookup(kid)
	if err != nil {
		return "", "", fmt.Errorf("lookup peer key %q: %w", kid, err), false
	}
	if !found {
		return "", "", fmt.Errorf("peer key %q is not live", kid), false
	}
	block, rest := pem.Decode(publicPEM)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		return "", "", fmt.Errorf("peer key %q is not exactly one PUBLIC KEY PEM block", kid), false
	}
	parsedKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("parse peer key %q: %w", kid, err), false
	}
	publicKey, ok := parsedKey.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return "", "", fmt.Errorf("peer key %q must be ECDSA P-256, got %T", kid, parsedKey), false
	}
	var claims authtoken.Claims
	if err := parsed.Claims(publicKey, &claims); err != nil {
		return "", "", nil, false
	}
	if claims.Subject != kid || !validClaimsFor(claims, peerIssuer, peerAudience) {
		return "", "", nil, false
	}
	return claims.Subject, claims.Scope, nil, true
}

// validClaims checks the registered claims (exp/nbf with a short leeway, plus
// iss/aud when configured), shared by the single-key and JWKS verify paths.
func (a *authenticator) validClaims(claims authtoken.Claims) bool {
	return validClaimsFor(claims, a.issuer, a.audience)
}

func validClaimsFor(claims authtoken.Claims, issuer, audience string) bool {
	// A token with no `exp` is a credential that never expires. go-jose only
	// checks expiry when Expiry is set (c.Expiry != nil), so an omitted exp would
	// otherwise pass validation forever — defeating rotation/revocation-by-expiry.
	// Require exp to be present explicitly before any other check.
	if claims.Expiry == nil {
		return false
	}
	expected := jwt.Expected{Time: time.Now()}
	if issuer != "" {
		expected.Issuer = issuer
	}
	if audience != "" {
		expected.AnyAudience = jwt.Audience{audience}
	}
	return claims.ValidateWithLeeway(expected, time.Minute) == nil
}

// challenge writes a 401 with the appropriate WWW-Authenticate header: a Basic
// challenge for /v2/* and a plain Bearer for /.cornus/v1/*.
//
// reason, when non-empty, explains why a credential that VERIFIED was still
// refused (today: its scope grants nothing, or grants only the caretaker
// endpoint). It is echoed as a `detail` field and, on the Bearer path, as RFC
// 6750 error parameters, so a scope mistake reads as a scope mistake instead of
// an unexplained 401. Nothing is disclosed to a caller that did not already
// present a validly signed token: an absent or unverifiable credential yields an
// empty reason and the bare challenge below.
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
func (a *authenticator) challenge(w http.ResponseWriter, isRegistry bool, reason string) {
	switch {
	case isRegistry:
		// Left as a bare Basic challenge even with a reason: a stock docker client
		// parses this header to decide how to retry, and extra parameters only
		// confuse that. The reason still reaches the operator in the body.
		w.Header().Set("WWW-Authenticate", `Basic realm="cornus"`)
	case reason != "":
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", error_description="`+headerSafe(reason)+`"`)
	default:
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	body := map[string]string{"error": "authentication required"}
	if reason != "" {
		body["detail"] = reason
	}
	writeJSON(w, http.StatusUnauthorized, body)
}

// headerSafe strips the characters that would break out of a quoted
// WWW-Authenticate parameter (double quotes, backslashes) or split the header
// (CR/LF), so a reason string can never forge a header.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '\r', '\n':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
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
