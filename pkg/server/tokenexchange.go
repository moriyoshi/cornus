package server

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"cornus/pkg/authtoken"
	"cornus/pkg/logging"
)

// OAuth 2.0 Token Exchange (RFC 8693): trade a third party's token for a
// short-lived cornus credential that names its scope.
//
// This is the same shape as the SSH-key flow next door — present a proof, get a
// minted cornus JWT back — with a different proof and a standard wire format.
// What it changes is WHEN policy runs. On the direct path the scope map is
// consulted on every request; here it runs once, at exchange time, and the token
// that reaches the API afterwards is cornus-minted and names its own scope. So
// authtoken.Grant stays absolutely fail-closed on the request path, and the
// mapping decision becomes one audit record per credential rather than one per
// request.
//
// The exchange never widens anything. The scope map decides the ceiling; a
// requested `scope` may only narrow it; and the whole endpoint refuses to issue
// the two non-client scopes (see issuableAccess).
const (
	exchangeIssuer   = "cornus:exchange"
	exchangeAudience = "cornus:exchange"
	exchangeTokenTTL = time.Hour

	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeJWT           = "urn:ietf:params:oauth:token-type:jwt"
	tokenTypeIDToken       = "urn:ietf:params:oauth:token-type:id_token"
)

// registerTokenExchangeRoute wires the endpoint, but only where it can do
// anything: it needs a verifier for the subject token, and the installation
// secret to mint with. Registering it unconditionally would publish an endpoint
// that can only ever answer 400 on a server with no auth configured.
func (s *Server) registerTokenExchangeRoute() {
	if s.auth == nil || len(s.auth.internalSecret) == 0 {
		return
	}
	if len(s.auth.jwt) == 0 && s.auth.jwks == nil {
		return
	}
	s.mux.HandleFunc("/.cornus/v1/auth/exchange", s.handleTokenExchange)
}

// issuableAccess reports whether the exchange endpoint may mint a credential at
// this access level.
//
// Only CLIENT-facing access is issuable. The two exclusions are not arbitrary:
//
//   - AccessCaretaker reaches /.cornus/v1/caretaker/attach, the sidecar's mount,
//     credential and telemetry relay. A client never talks to a caretaker; the
//     caretaker talks to the server, presenting its OWN credential. Where a pod
//     should authenticate with its ServiceAccount token, it does so on the direct
//     path — the JWKS verifier authenticates it and a scope-map rule grants it
//     `caretaker` — which puts the credential in the hands of the component that
//     uses it rather than minting one for a client that does not.
//   - AccessPeer is a server-to-server credential for inter-replica forwarding,
//     verified against a public key published in the hub store with sub == kid
//     (verifyPeerJWT). Nothing about that shape is reachable by exchange.
//
// The check has to cover the REQUESTED scope as well as the mapped one, because
// `caretaker` is genuinely contained in `api` under the access matrix — so a
// client entitled to full access could otherwise downscope its way into a
// caretaker credential.
func issuableAccess(a authtoken.Access) bool {
	switch a {
	case authtoken.AccessFull, authtoken.AccessRegistryPush, authtoken.AccessRegistryPull:
		return true
	default:
		return false
	}
}

// oauthError writes an RFC 6749 §5.2-shaped error body. Standard clients read
// `error`; `error_description` is for the human reading the failure.
func oauthError(w http.ResponseWriter, code int, kind, description string) {
	writeJSON(w, code, map[string]string{"error": kind, "error_description": description})
}

func (s *Server) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "token exchange is POST only")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body: "+err.Error())
		return
	}
	if got := r.PostForm.Get("grant_type"); got != grantTypeTokenExchange {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"grant_type must be "+grantTypeTokenExchange)
		return
	}
	subjectToken := strings.TrimSpace(r.PostForm.Get("subject_token"))
	if subjectToken == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "subject_token is required")
		return
	}
	switch r.PostForm.Get("subject_token_type") {
	case tokenTypeJWT, tokenTypeAccessToken, tokenTypeIDToken:
	case "":
		oauthError(w, http.StatusBadRequest, "invalid_request", "subject_token_type is required")
		return
	default:
		oauthError(w, http.StatusBadRequest, "invalid_request",
			"subject_token_type must be one of "+tokenTypeJWT+", "+tokenTypeAccessToken+", "+tokenTypeIDToken)
		return
	}
	// Delegation (acting on behalf of another subject) is a distinct trust model
	// with its own audit story. Refusing it explicitly is better than accepting
	// the parameter and silently ignoring it, which would let a client believe it
	// had delegated when it had not.
	if r.PostForm.Get("actor_token") != "" {
		oauthError(w, http.StatusBadRequest, "invalid_request",
			"delegation (actor_token) is not supported")
		return
	}
	if rt := r.PostForm.Get("requested_token_type"); rt != "" && rt != tokenTypeAccessToken && rt != tokenTypeJWT {
		oauthError(w, http.StatusBadRequest, "invalid_request",
			"requested_token_type must be "+tokenTypeAccessToken+" or "+tokenTypeJWT)
		return
	}

	// The subject token is verified by exactly the chain that would verify it on
	// the request path, so an exchange can never accept a credential a direct call
	// would have rejected.
	subject, claims, ok := s.auth.verifyExternalSubject(subjectToken)
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "subject_token did not verify")
		return
	}

	mappedScope, rule, err := s.auth.scopeMap.Match(claims)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant",
			"no scope-map rule matched this subject_token's claims, so it is entitled to nothing")
		return
	}
	mapped, err := authtoken.Grant(mappedScope)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	if !issuableAccess(mapped) {
		oauthError(w, http.StatusBadRequest, "invalid_grant",
			"scope-map rule "+rule+" grants "+mapped.String()+", which is not a client credential and cannot be issued by exchange")
		return
	}

	// A requested scope may only NARROW what policy granted. Containment is by
	// what each access actually reaches (authtoken.Contains), because Access has
	// no ordering that would answer this: registry:push and caretaker reach
	// disjoint endpoints.
	issuedScope := mappedScope
	if requested := strings.TrimSpace(r.PostForm.Get("scope")); requested != "" {
		want, err := authtoken.Grant(requested)
		if err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
			return
		}
		if !issuableAccess(want) {
			oauthError(w, http.StatusBadRequest, "invalid_scope",
				requested+" is not a client credential and cannot be issued by exchange")
			return
		}
		if !authtoken.Contains(mapped, want) {
			oauthError(w, http.StatusBadRequest, "invalid_scope",
				"requested scope "+requested+" exceeds "+mappedScope+", which is what policy grants this subject")
			return
		}
		issuedScope = requested
	}

	token, err := authtoken.Issue(authtoken.IssueOptions{
		Subject:     subject,
		Scope:       issuedScope,
		Issuer:      exchangeIssuer,
		Audience:    exchangeAudience,
		TTL:         exchangeTokenTTL,
		HS256Secret: s.auth.internalSecret,
	})
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	// One audit record per CREDENTIAL — the thing the exchange buys over
	// re-deciding on every request. Info, not Debug: an operator asking "who was
	// granted what, and by which rule" should find the answer at the default level.
	logging.FromContext(r.Context(), slog.String("component", "auth")).InfoContext(r.Context(),
		"token exchange issued a cornus credential",
		"sub", subject,
		"rule", rule,
		"mapped_scope", mappedScope,
		"issued_scope", issuedScope,
		"ttl", exchangeTokenTTL.String(),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":      token,
		"issued_token_type": tokenTypeAccessToken,
		"token_type":        "Bearer",
		"expires_in":        int(exchangeTokenTTL.Seconds()),
		// RFC 8693 requires `scope` in the response whenever it differs from the
		// request; always sending it means a client never has to infer what it got.
		"scope": issuedScope,
	})
}

// verifyExternalSubject runs a token through the operator-configured verifiers —
// the same ones, in the same order, that the request path uses. It returns the
// subject and the raw claim set the scope map matches on.
//
// Deliberately NOT the internal or ssh-key verifiers: those mint credentials that
// already name a scope, so exchanging one would be a round trip that could only
// ever narrow it, and admitting them here would put a cornus-issued token through
// a policy layer written for foreign claims.
func (a *authenticator) verifyExternalSubject(token string) (string, map[string]any, bool) {
	for _, v := range a.jwt {
		if sub, claims, ok := a.verifyJWT(v, token); ok {
			return sub, claims, true
		}
	}
	if a.jwks != nil {
		if sub, claims, ok := a.verifyJWKS(token); ok {
			return sub, claims, true
		}
	}
	return "", nil, false
}

// jwtHS256Algs is the single-element algorithm set the installation-secret
// verifiers bind to. Named so tests can build the same verifier the server does
// rather than restating the algorithm and drifting from it.
func jwtHS256Algs() []jose.SignatureAlgorithm { return []jose.SignatureAlgorithm{jose.HS256} }
