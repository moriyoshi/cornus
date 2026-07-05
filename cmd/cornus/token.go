package main

import (
	"fmt"
	"os"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/authtoken"
)

// TokenCmd is cornus's in-process JWT issuer. A cornus server is verify-only;
// this command mints the tokens it verifies. Sign with the SAME material the server
// verifies against: an HS256 secret (CORNUS_JWT_HS256_SECRET on both sides) or a
// private key whose public half is the server's CORNUS_JWT_PUBLIC_KEY.
type TokenCmd struct {
	Issue TokenIssueCmd `kong:"cmd,help='Mint a signed JWT for a cornus server with bearer auth.'"`
}

// TokenIssueCmd mints one JWT.
type TokenIssueCmd struct {
	Sub     string        `kong:"help='Subject (sub) claim — the caller identity.'"`
	Scope   string        `kong:"help='Scope: \"api\" (full, the default meaning of an empty scope) or \"caretaker\" (the caretaker endpoint only).'"`
	TTL     time.Duration `kong:"default='1h',help='Token lifetime (e.g. 1h, 720h).'"`
	Iss     string        `kong:"help='Issuer (iss) claim; must match the server CORNUS_JWT_ISSUER when that is set.'"`
	Aud     string        `kong:"help='Audience (aud) claim; must match the server CORNUS_JWT_AUDIENCE when that is set.'"`
	KID     string        `kong:"name='kid',help='Key ID header, so a JWKS verifier (CORNUS_JWT_JWKS_FILE/_URL) can select the matching key.'"`
	HS256   string        `kong:"name='hs256-secret',env='CORNUS_JWT_HS256_SECRET',help='HMAC secret (symmetric); the server verifies with the same secret. At least 32 bytes.'"`
	KeyFile string        `kong:"name='private-key',type='path',help='PEM private key file (RS256 for RSA, ES256 for ECDSA); the server verifies with the matching public key.'"`
}

// Run mints the token and prints it to stdout.
func (c *TokenIssueCmd) Run(d *cliout.Driver) error {
	opts := authtoken.IssueOptions{
		Subject:  c.Sub,
		Scope:    c.Scope,
		Issuer:   c.Iss,
		Audience: c.Aud,
		TTL:      c.TTL,
		KeyID:    c.KID,
	}
	if c.HS256 != "" {
		opts.HS256Secret = []byte(c.HS256)
	}
	if c.KeyFile != "" {
		pemBytes, err := os.ReadFile(c.KeyFile)
		if err != nil {
			return fmt.Errorf("read private key: %w", err)
		}
		opts.PrivateKeyPEM = pemBytes
	}
	tok, err := authtoken.Issue(opts)
	if err != nil {
		return err
	}
	d.Item("%s", tok)
	return nil
}
