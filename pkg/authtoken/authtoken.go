// Package authtoken is the shared JWT model for cornus's bearer authentication:
// the claim set (registered claims plus a cornus scope), the scope semantics, and
// the issuer used by `cornus token`. The server (pkg/server) verifies tokens
// against the SAME Claims type and scope logic, so the issuer and verifier never
// drift. cornus issues tokens here but the server remains verify-only.
package authtoken

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Scope values. A token's scope decides which endpoints it may reach, and access
// is granted ONLY by naming it: api grants everything, registry:push grants
// registry reads and writes, registry:pull grants registry reads, caretaker
// grants the caretaker attach endpoint alone, and peer grants only inter-replica
// forward endpoints. A scope that names none of these — including an empty or
// whitespace-only one — grants NOTHING (fail closed).
const (
	ScopeAPI          = "api"
	ScopeRegistryPull = "registry:pull"
	ScopeRegistryPush = "registry:push"
	ScopeCaretaker    = "caretaker"
	ScopePeer         = "peer"
)

// Claims is the cornus JWT claim set: the standard registered claims plus a
// space-separated scope.
type Claims struct {
	jwt.Claims
	Scope string `json:"scope,omitempty"`
}

// Access is what a token's scope grants. It is an allowlist, not a filter: the
// zero value is AccessNone, so every path that forgets to consult Grant, and
// every token that names no cornus scope, denies rather than allows.
type Access int

const (
	// AccessNone grants no endpoint at all. Returned together with an error
	// explaining which scope was seen and what was expected.
	AccessNone Access = iota
	// AccessCaretaker grants ONLY /.cornus/v1/caretaker/attach — never the client
	// API and never the registry.
	AccessCaretaker
	// AccessPeer grants ONLY inter-replica forwarding endpoints. It is a narrow
	// server-to-server credential, never a client, registry, or caretaker one.
	AccessPeer
	// AccessRegistryPull grants read-only access to the OCI registry.
	AccessRegistryPull
	// AccessRegistryPush grants read-write access to the OCI registry.
	AccessRegistryPush
	// AccessFull grants every endpoint, the caretaker one included.
	AccessFull
)

// String renders an Access for logs and error messages.
func (a Access) String() string {
	switch a {
	case AccessCaretaker:
		return "caretaker"
	case AccessPeer:
		return "peer"
	case AccessRegistryPull:
		return "registry:pull"
	case AccessRegistryPush:
		return "registry:push"
	case AccessFull:
		return "full"
	default:
		return "none"
	}
}

// Endpoint is the class of request an Access may authorize. Its zero value is
// EndpointUnknown, which Allows always denies.
type Endpoint int

const (
	EndpointUnknown Endpoint = iota
	EndpointAPI
	EndpointRegistryPull
	EndpointRegistryPush
	EndpointCaretaker
	EndpointPeerForward
)

// String renders an Endpoint for diagnostics.
func (e Endpoint) String() string {
	switch e {
	case EndpointAPI:
		return "api"
	case EndpointRegistryPull:
		return "registry:pull"
	case EndpointRegistryPush:
		return "registry:push"
	case EndpointCaretaker:
		return "caretaker"
	case EndpointPeerForward:
		return "peer-forward"
	default:
		return "unknown"
	}
}

// allEndpoints is every endpoint Allows can grant. Kept beside the Endpoint
// constants so a new endpoint that is not added here shows up as a Contains that
// silently ignores it — the one failure mode worth guarding, since Contains is
// what bounds a downscope request.
var allEndpoints = []Endpoint{
	EndpointAPI,
	EndpointRegistryPull,
	EndpointRegistryPush,
	EndpointCaretaker,
	EndpointPeerForward,
}

// Contains reports whether outer grants at least everything inner grants.
//
// Access is NOT a lattice — AccessRegistryPush and AccessCaretaker reach
// disjoint endpoints and neither contains the other — so "is this weaker?" has no
// answer in terms of the enum's order, and any ordering invented for it would be
// wrong in some corner. The access matrix does have an answer, though: compare
// what the two actually reach. That is what makes a downscope request checkable
// (the requested scope must reach nothing the granted one does not), with no
// ranking to get wrong.
func Contains(outer, inner Access) bool {
	for _, e := range allEndpoints {
		if Allows(inner, e) && !Allows(outer, e) {
			return false
		}
	}
	return true
}

// Allows applies the fail-closed access matrix. AccessFull reaches every known
// endpoint; registry push includes pull; and all zero/unknown values deny.
func Allows(access Access, endpoint Endpoint) bool {
	switch access {
	case AccessFull:
		return endpoint == EndpointAPI ||
			endpoint == EndpointRegistryPull ||
			endpoint == EndpointRegistryPush ||
			endpoint == EndpointCaretaker ||
			endpoint == EndpointPeerForward
	case AccessRegistryPush:
		return endpoint == EndpointRegistryPull || endpoint == EndpointRegistryPush
	case AccessRegistryPull:
		return endpoint == EndpointRegistryPull
	case AccessCaretaker:
		return endpoint == EndpointCaretaker
	case AccessPeer:
		return endpoint == EndpointPeerForward
	default:
		return false
	}
}

// ErrScopeMissing is returned by Grant for a scope that is empty or contains only
// whitespace — a token that names no scope at all. It is a distinct error so a
// caller can tell "no scope claim" (usually a dropped claim or a token minted by
// an older/foreign issuer) from "a scope, but not one cornus knows".
var ErrScopeMissing = errors.New("token has no scope: a cornus token must name a scope (\"" + ScopeAPI + "\" for full access, \"" + ScopeRegistryPush + "\" or \"" + ScopeRegistryPull + "\" for registry access, \"" + ScopeCaretaker + "\" for the caretaker attach endpoint only, or \"" + ScopePeer + "\" for inter-replica forwarding only); mint one with `cornus token issue --scope " + ScopeAPI + "`")

// Grant resolves a space-separated scope string into the access it confers.
//
// The model is a strict allowlist, which is what makes it fail closed:
//
//	names api                          -> AccessFull
//	names registry:push, not api       -> AccessRegistryPush
//	names registry:pull, neither above -> AccessRegistryPull
//	names caretaker, none above        -> AccessCaretaker
//	names peer, none above             -> AccessPeer
//	names none of these                 -> AccessNone + error
//
// An empty scope therefore grants NOTHING. It used to grant full access, on the
// theory that a plain JWT predating scopes was a full credential; that was a
// fail-open — a dropped or mistyped scope claim silently promoted a restricted
// credential to a full one. Unknown scope names are ignored when a cornus scope
// is also present (`read caretaker` is still caretaker), but a scope built only
// out of names cornus does not know (a foreign issuer's `openid profile`) grants
// nothing, for the same reason: cornus access must be asked for by name.
//
// The returned error is written for an operator: it names the offending scope and
// the accepted values, so a 401 is never unexplained.
func Grant(scope string) (Access, error) {
	fields := strings.Fields(scope)
	if len(fields) == 0 {
		return AccessNone, ErrScopeMissing
	}
	var hasCaretaker, hasPeer, hasRegistryPull, hasRegistryPush, hasAPI bool
	for _, s := range fields {
		switch s {
		case ScopeCaretaker:
			hasCaretaker = true
		case ScopePeer:
			hasPeer = true
		case ScopeRegistryPull:
			hasRegistryPull = true
		case ScopeRegistryPush:
			hasRegistryPush = true
		case ScopeAPI:
			hasAPI = true
		}
	}
	switch {
	case hasAPI:
		return AccessFull, nil
	case hasRegistryPush:
		return AccessRegistryPush, nil
	case hasRegistryPull:
		return AccessRegistryPull, nil
	case hasCaretaker:
		return AccessCaretaker, nil
	case hasPeer:
		return AccessPeer, nil
	default:
		return AccessNone, fmt.Errorf("token scope %q names no cornus scope (want %q for full access, %q or %q for registry access, %q for the caretaker attach endpoint only, or %q for inter-replica forwarding only)", scope, ScopeAPI, ScopeRegistryPush, ScopeRegistryPull, ScopeCaretaker, ScopePeer)
	}
}

// IssueOptions configures a minted token. Exactly one signing key must be set:
// HS256Secret (symmetric — the server verifies with the same secret) or
// PrivateKeyPEM (RS256/ES256 — the server verifies with the matching public key).
type IssueOptions struct {
	Subject string
	// Scope is REQUIRED and must grant something (see Grant): a token that names
	// no scope can never be minted, so the "scope claim went missing" failure
	// surfaces here — at creation, where it is actionable — instead of as an
	// unexplained 401 against a running server.
	Scope         string
	Issuer        string
	Audience      string
	TTL           time.Duration
	Now           time.Time // defaults to time.Now() when zero (tests inject a fixed time)
	KeyID         string    // when set, stamped as the JWT `kid` header so a JWKS verifier can select the key
	HS256Secret   []byte
	PrivateKeyPEM []byte
}

// Issue mints and signs a JWT for the given options, returning the compact token.
func Issue(opts IssueOptions) (string, error) {
	if len(opts.HS256Secret) > 0 && len(opts.PrivateKeyPEM) > 0 {
		return "", errors.New("authtoken: set only one of HS256 secret or private key")
	}
	var sk jose.SigningKey
	switch {
	case len(opts.HS256Secret) > 0:
		sk = jose.SigningKey{Algorithm: jose.HS256, Key: opts.HS256Secret}
	case len(opts.PrivateKeyPEM) > 0:
		key, alg, err := parsePrivateKey(opts.PrivateKeyPEM)
		if err != nil {
			return "", err
		}
		sk = jose.SigningKey{Algorithm: alg, Key: key}
	default:
		return "", errors.New("authtoken: no signing key (set an HS256 secret or a private key)")
	}
	if opts.TTL <= 0 {
		return "", errors.New("authtoken: ttl must be positive")
	}
	// Refuse to mint a credential that grants nothing. This is the issue-time half
	// of the fail-closed scope contract: the verifier rejects a scopeless token,
	// and this makes sure cornus itself never produces one to be rejected.
	if _, err := Grant(opts.Scope); err != nil {
		return "", fmt.Errorf("authtoken: %w", err)
	}

	signerOpts := (&jose.SignerOptions{}).WithType("JWT")
	if opts.KeyID != "" {
		signerOpts = signerOpts.WithHeader("kid", opts.KeyID)
	}
	signer, err := jose.NewSigner(sk, signerOpts)
	if err != nil {
		return "", fmt.Errorf("authtoken: signer: %w", err)
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	cl := Claims{
		Claims: jwt.Claims{
			Subject:   opts.Subject,
			Issuer:    opts.Issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(now.Add(opts.TTL)),
		},
		Scope: opts.Scope,
	}
	if opts.Audience != "" {
		cl.Audience = jwt.Audience{opts.Audience}
	}

	tok, err := jwt.Signed(signer).Claims(cl).Serialize()
	if err != nil {
		return "", fmt.Errorf("authtoken: sign: %w", err)
	}
	return tok, nil
}

// parsePrivateKey decodes a PEM private key and picks the signature algorithm from
// its type: RSA -> RS256, ECDSA -> ES256.
func parsePrivateKey(raw []byte) (crypto.Signer, jose.SignatureAlgorithm, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", errors.New("authtoken: no PEM block found")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, "", fmt.Errorf("authtoken: unsupported PEM block type %q", block.Type)
	}
	if err != nil {
		return nil, "", err
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return k, jose.RS256, nil
	case *ecdsa.PrivateKey:
		return k, jose.ES256, nil
	default:
		return nil, "", fmt.Errorf("authtoken: unsupported private key type %T", key)
	}
}
