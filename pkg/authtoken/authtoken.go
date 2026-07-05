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

// Scope values. A token's scope decides which endpoints it may reach. An empty
// scope (or one naming api) grants full access; caretaker restricts to the
// caretaker endpoint only.
const (
	ScopeAPI       = "api"
	ScopeCaretaker = "caretaker"
)

// Claims is the cornus JWT claim set: the standard registered claims plus a
// space-separated scope.
type Claims struct {
	jwt.Claims
	Scope string `json:"scope,omitempty"`
}

// CaretakerOnly reports whether a scope grants ONLY the caretaker endpoint — it
// names caretaker and does not name api. An empty scope grants full access (so a
// plain JWT with no scope is a full credential, as before scopes existed).
func CaretakerOnly(scope string) bool {
	var hasCaretaker, hasAPI bool
	for _, s := range strings.Fields(scope) {
		switch s {
		case ScopeCaretaker:
			hasCaretaker = true
		case ScopeAPI:
			hasAPI = true
		}
	}
	return hasCaretaker && !hasAPI
}

// IssueOptions configures a minted token. Exactly one signing key must be set:
// HS256Secret (symmetric — the server verifies with the same secret) or
// PrivateKeyPEM (RS256/ES256 — the server verifies with the matching public key).
type IssueOptions struct {
	Subject       string
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
