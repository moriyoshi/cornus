package authtoken

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestCaretakerOnly(t *testing.T) {
	cases := []struct {
		scope string
		want  bool
	}{
		{"", false},              // empty = full
		{"api", false},           // explicit full
		{"caretaker", true},      // caretaker only
		{"caretaker api", false}, // both named = full
		{"api caretaker", false}, // order-independent
		{"read caretaker", true}, // unknown + caretaker, no api
		{"something", false},     // unknown alone = not caretaker-restricted
	}
	for _, c := range cases {
		if got := CaretakerOnly(c.scope); got != c.want {
			t.Fatalf("CaretakerOnly(%q) = %v, want %v", c.scope, got, c.want)
		}
	}
}

func TestIssueHS256RoundTrip(t *testing.T) {
	secret := []byte("this-is-a-32-byte-minimum-secret!!")
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	tok, err := Issue(IssueOptions{
		Subject: "ci-bot", Scope: ScopeCaretaker, Issuer: "cornus", Audience: "reg",
		TTL: time.Hour, Now: now, HS256Secret: secret,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		t.Fatalf("ParseSigned: %v", err)
	}
	var cl Claims
	if err := parsed.Claims(secret, &cl); err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if cl.Subject != "ci-bot" || cl.Scope != ScopeCaretaker || cl.Issuer != "cornus" {
		t.Fatalf("claims = %+v", cl)
	}
	if err := cl.Validate(jwt.Expected{Time: now.Add(30 * time.Minute), Issuer: "cornus", AnyAudience: jwt.Audience{"reg"}}); err != nil {
		t.Fatalf("mid-life validation failed: %v", err)
	}
	if err := cl.Validate(jwt.Expected{Time: now.Add(2 * time.Hour)}); err == nil {
		t.Fatal("expected expiry after TTL")
	}
}

func TestIssueES256RoundTrip(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	tok, err := Issue(IssueOptions{Subject: "svc", TTL: time.Hour, PrivateKeyPEM: pemKey})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("ParseSigned: %v", err)
	}
	var cl Claims
	if err := parsed.Claims(&priv.PublicKey, &cl); err != nil {
		t.Fatalf("verify with public key: %v", err)
	}
	if cl.Subject != "svc" {
		t.Fatalf("subject = %q", cl.Subject)
	}
}

func TestIssueStampsKeyID(t *testing.T) {
	secret := []byte("this-is-a-32-byte-minimum-secret!!")
	tok, err := Issue(IssueOptions{Subject: "x", TTL: time.Hour, KeyID: "key-1", HS256Secret: secret})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		t.Fatalf("ParseSigned: %v", err)
	}
	if len(parsed.Headers) == 0 || parsed.Headers[0].KeyID != "key-1" {
		t.Fatalf("kid header = %+v, want key-1", parsed.Headers)
	}

	// No KeyID -> no kid header.
	plain, _ := Issue(IssueOptions{Subject: "x", TTL: time.Hour, HS256Secret: secret})
	p2, _ := jwt.ParseSigned(plain, []jose.SignatureAlgorithm{jose.HS256})
	if len(p2.Headers) > 0 && p2.Headers[0].KeyID != "" {
		t.Fatalf("unexpected kid: %q", p2.Headers[0].KeyID)
	}
}

func TestIssueErrors(t *testing.T) {
	// No key.
	if _, err := Issue(IssueOptions{Subject: "x", TTL: time.Hour}); err == nil {
		t.Fatal("want error with no signing key")
	}
	// Both keys.
	if _, err := Issue(IssueOptions{TTL: time.Hour, HS256Secret: []byte("0123456789012345678901234567890123"), PrivateKeyPEM: []byte("x")}); err == nil {
		t.Fatal("want error with two signing keys")
	}
	// Non-positive TTL.
	if _, err := Issue(IssueOptions{HS256Secret: []byte("0123456789012345678901234567890123")}); err == nil {
		t.Fatal("want error with non-positive ttl")
	}
}
