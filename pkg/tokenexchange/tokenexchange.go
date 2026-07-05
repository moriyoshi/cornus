// Package tokenexchange is the client half of OAuth 2.0 Token Exchange
// (RFC 8693): trade a token the server can verify for a short-lived cornus
// credential that names its scope.
//
// The server half lives in pkg/server/tokenexchange.go. This side deliberately
// knows nothing about how the subject token was produced — a Kubernetes
// TokenRequest, an OIDC login, a file on disk — because the exchange is the same
// round trip either way, and keeping it credential-agnostic is what lets one
// implementation serve the connection profile and `cornus token exchange` alike.
package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Path is the server endpoint, relative to the server's base URL.
const Path = "/.cornus/v1/auth/exchange"

const (
	grantType            = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeJWT         = "urn:ietf:params:oauth:token-type:jwt"
	tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
)

// Options describes one exchange.
type Options struct {
	// Server is the cornus base URL (scheme://host[:port]).
	Server string
	// SubjectToken is the credential being traded in.
	SubjectToken string
	// Scope optionally NARROWS the credential. Leave it empty to receive whatever
	// the server's scope map grants; a value the policy does not grant is refused
	// rather than silently reduced, so a profile pinning a scope fails loudly when
	// policy changes underneath it.
	Scope string
	// HTTPClient defaults to a client with a 30s timeout. Callers that need a
	// tunnel, a proxy, or custom TLS pass their own.
	HTTPClient *http.Client
}

// Result is a minted credential.
type Result struct {
	Token string
	// Scope is what the server ISSUED, which may be narrower than requested and is
	// not always what the subject token claimed. Callers cache against it.
	Scope     string
	ExpiresIn time.Duration
}

// Error is a structured OAuth error response, so a caller can tell a policy
// refusal (invalid_grant: this identity is entitled to nothing) from a protocol
// mistake (invalid_request) without matching on prose.
type Error struct {
	Code        string
	Description string
	Status      int
}

func (e *Error) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("token exchange: %s: %s", e.Code, e.Description)
	}
	return "token exchange: " + e.Code
}

// ErrUnsupported reports that the server has no exchange endpoint — it is an
// older cornus, or one with no JWT/JWKS verifier configured, in which case the
// route is deliberately not registered. Callers fall back to sending the subject
// token directly, which is exactly what they did before exchange existed.
var ErrUnsupported = errors.New("token exchange: this server does not offer /.cornus/v1/auth/exchange")

// Exchange performs the round trip.
func Exchange(ctx context.Context, opts Options) (Result, error) {
	if opts.Server == "" {
		return Result{}, errors.New("token exchange: no server")
	}
	if opts.SubjectToken == "" {
		return Result{}, errors.New("token exchange: no subject token")
	}
	form := url.Values{
		"grant_type":         {grantType},
		"subject_token":      {opts.SubjectToken},
		"subject_token_type": {tokenTypeJWT},
	}
	if s := strings.TrimSpace(opts.Scope); s != "" {
		form.Set("scope", s)
	}
	endpoint := strings.TrimRight(opts.Server, "/") + Path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// 404 is the "no such endpoint" signal, and it is worth distinguishing: a
	// server with auth off, or an older one, has no route here at all, and that
	// must degrade to the direct path rather than failing the user's command.
	if resp.StatusCode == http.StatusNotFound {
		return Result{}, ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, parseError(resp.StatusCode, body)
	}

	var out struct {
		AccessToken     string `json:"access_token"`
		IssuedTokenType string `json:"issued_token_type"`
		TokenType       string `json:"token_type"`
		ExpiresIn       int    `json:"expires_in"`
		Scope           string `json:"scope"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("token exchange: malformed response: %w", err)
	}
	if out.AccessToken == "" {
		return Result{}, errors.New("token exchange: response carried no access_token")
	}
	// A token_type the caller cannot use as a bearer credential would fail later,
	// at a request whose failure says nothing about why. Check it here.
	if !strings.EqualFold(out.TokenType, "Bearer") {
		return Result{}, fmt.Errorf("token exchange: server issued token_type %q, want Bearer", out.TokenType)
	}
	if out.IssuedTokenType != "" && out.IssuedTokenType != tokenTypeAccessToken && out.IssuedTokenType != tokenTypeJWT {
		return Result{}, fmt.Errorf("token exchange: server issued token type %q", out.IssuedTokenType)
	}
	return Result{
		Token:     out.AccessToken,
		Scope:     out.Scope,
		ExpiresIn: time.Duration(out.ExpiresIn) * time.Second,
	}, nil
}

func parseError(status int, body []byte) error {
	var e struct {
		Code        string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &e) == nil && e.Code != "" {
		return &Error{Code: e.Code, Description: e.Description, Status: status}
	}
	// Not an OAuth error body: report the status and a bounded excerpt rather than
	// inventing a code the server did not send.
	excerpt := strings.TrimSpace(string(body))
	if len(excerpt) > 200 {
		excerpt = excerpt[:200] + "…"
	}
	return fmt.Errorf("token exchange: HTTP %d: %s", status, excerpt)
}
