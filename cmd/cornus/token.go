package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/authtoken"
	"cornus/pkg/tokenexchange"
)

// TokenCmd is cornus's in-process JWT issuer. A cornus server is verify-only;
// this command mints the tokens it verifies. Sign with the SAME material the server
// verifies against: an HS256 secret (CORNUS_JWT_HS256_SECRET on both sides) or a
// private key whose public half is the server's CORNUS_JWT_PUBLIC_KEY.
type TokenCmd struct {
	Issue    TokenIssueCmd    `kong:"cmd,help='Mint a signed JWT for a cornus server with bearer auth.'"`
	Exchange TokenExchangeCmd `kong:"cmd,help='Trade a third-party token for a short-lived cornus credential (OAuth 2.0 Token Exchange, RFC 8693).'"`
}

// TokenIssueCmd mints one JWT.
type TokenIssueCmd struct {
	Sub     string        `kong:"help='Subject (sub) claim — the caller identity.'"`
	Scope   string        `kong:"default='api',help='Scope: \"api\" (full access, the default) or \"caretaker\" (the caretaker attach endpoint only). A token must name one — an empty scope grants nothing and is refused at mint time.'"`
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

// TokenExchangeCmd trades a third party's token for a cornus credential over
// RFC 8693, for scripting and for seeing what a scope map actually grants an
// identity.
//
// Unlike the connection profile's exchange, this deliberately does NOT cache:
// its output is a token the caller is about to do something specific with, and a
// cached answer would make `cornus token exchange` a poor way to ask "what does
// policy grant this subject right now" — which is most of what it is for.
type TokenExchangeCmd struct {
	Server           string `kong:"required,help='Cornus server base URL (https://host:port).'"`
	SubjectToken     string `kong:"name='subject-token',env='CORNUS_SUBJECT_TOKEN',help='The token to trade in. Puts a credential in argv/history — prefer --subject-token-file.'"`
	SubjectTokenFile string `kong:"name='subject-token-file',type='path',help='Read the subject token from this file instead of --subject-token.'"`
	Scope            string `kong:"help='Narrow the issued credential (e.g. registry:pull). Empty takes whatever the server scope map grants; a scope it does not grant is refused, never silently reduced.'"`
	Insecure         bool   `kong:"help='Skip TLS verification (test servers with self-signed certificates).'"`
}

// Run performs the exchange and prints the issued token.
func (c *TokenExchangeCmd) Run(d *cliout.Driver) error {
	subject := c.SubjectToken
	switch {
	case c.SubjectTokenFile != "" && subject != "":
		return errors.New("set only one of --subject-token and --subject-token-file")
	case c.SubjectTokenFile != "":
		raw, err := os.ReadFile(c.SubjectTokenFile)
		if err != nil {
			return fmt.Errorf("read subject token: %w", err)
		}
		subject = strings.TrimSpace(string(raw))
	}
	if subject == "" {
		return errors.New("no subject token: pass --subject-token, --subject-token-file, or CORNUS_SUBJECT_TOKEN")
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	if c.Insecure {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // explicit opt-in
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := tokenexchange.Exchange(ctx, tokenexchange.Options{
		Server:       c.Server,
		SubjectToken: subject,
		Scope:        c.Scope,
		HTTPClient:   httpClient,
	})
	if err != nil {
		return err
	}
	// The issued scope goes to stderr, not stdout: stdout is the token, so
	// `TOKEN=$(cornus token exchange ...)` stays usable, while the human still
	// learns that policy granted something narrower than they asked for.
	d.Info("issued scope %s, valid for %s", res.Scope, res.ExpiresIn)
	d.Item("%s", res.Token)
	return nil
}
