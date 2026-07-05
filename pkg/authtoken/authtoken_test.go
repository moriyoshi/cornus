package authtoken

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// TestGrant pins the fail-closed scope contract: access is granted only by naming
// a cornus scope. A token that names none — empty, whitespace-only, or built
// solely from names cornus does not know — grants NOTHING, where it used to grant
// full access.
func TestGrant(t *testing.T) {
	cases := []struct {
		scope   string
		want    Access
		wantErr bool
		why     string
	}{
		{"", AccessNone, true, "empty scope grants nothing (was: full access — the fail-open this closes)"},
		{"   ", AccessNone, true, "whitespace-only is the same as empty"},
		{"\t\n ", AccessNone, true, "any whitespace run, not just spaces"},
		{"api", AccessFull, false, "explicit full"},
		{"registry:push", AccessRegistryPush, false, "registry read-write"},
		{"registry:pull", AccessRegistryPull, false, "registry read-only"},
		{"caretaker", AccessCaretaker, false, "caretaker only"},
		{"peer", AccessPeer, false, "inter-replica forwarding only"},
		{"caretaker api", AccessFull, false, "api is a superset, so naming both is full"},
		{"peer api", AccessFull, false, "api remains the top-precedence superset"},
		{"api caretaker", AccessFull, false, "order-independent"},
		{"caretaker registry:pull", AccessRegistryPull, false, "registry pull outranks caretaker"},
		{"registry:pull registry:push", AccessRegistryPush, false, "registry push outranks pull"},
		{"registry:push api", AccessFull, false, "api outranks registry push"},
		{"read caretaker", AccessCaretaker, false, "unknown names are ignored beside a known one"},
		{"read peer", AccessPeer, false, "unknown names are ignored beside peer"},
		{"caretaker read", AccessCaretaker, false, "unknown names are ignored beside a known one"},
		{"read api write", AccessFull, false, "unknown names are ignored beside api"},
		{"something", AccessNone, true, "an unknown name alone grants nothing"},
		{"openid profile email", AccessNone, true, "a foreign issuer's scopes grant no cornus access"},
		{"API", AccessNone, true, "scope names are case-sensitive"},
		{"Caretaker", AccessNone, true, "scope names are case-sensitive"},
		{"api:full", AccessNone, true, "no prefix/suffix matching — the name must be exact"},
		{"caretakerx", AccessNone, true, "no prefix matching"},
	}
	for _, c := range cases {
		got, err := Grant(c.scope)
		if got != c.want || (err != nil) != c.wantErr {
			t.Fatalf("Grant(%q) = (%v, err=%v), want (%v, err=%v) — %s", c.scope, got, err, c.want, c.wantErr, c.why)
		}
		if err != nil && got != AccessNone {
			t.Fatalf("Grant(%q) returned an error with access %v; an error must always mean AccessNone", c.scope, got)
		}
		// A denial must explain itself: the message names the offending scope (or
		// says there was none) and the accepted values.
		if err != nil && !strings.Contains(err.Error(), ScopeAPI) {
			t.Fatalf("Grant(%q) error %q does not mention the accepted scope %q", c.scope, err, ScopeAPI)
		}
	}

	// The no-scope case is distinguishable from an unknown-scope case.
	if _, err := Grant(""); !errors.Is(err, ErrScopeMissing) {
		t.Fatalf("Grant(\"\") error = %v, want ErrScopeMissing", err)
	}
	if _, err := Grant("   "); !errors.Is(err, ErrScopeMissing) {
		t.Fatalf("Grant(whitespace) error = %v, want ErrScopeMissing", err)
	}
	if _, err := Grant("bogus"); errors.Is(err, ErrScopeMissing) {
		t.Fatal("an unknown-but-present scope must not report as a missing scope")
	}

	// The zero value denies, so a forgotten Grant call cannot open a door.
	var zero Access
	if zero != AccessNone {
		t.Fatal("the zero Access must be AccessNone")
	}
}

// TestIssueRequiresGrantingScope proves the issuer cannot mint a credential that
// grants nothing: the fail-closed contract is enforced at creation time, where
// the error is actionable, not only at verification time.
func TestIssueRequiresGrantingScope(t *testing.T) {
	secret := []byte("this-is-a-32-byte-minimum-secret!!")
	for _, scope := range []string{"", "   ", "openid profile"} {
		_, err := Issue(IssueOptions{Subject: "x", Scope: scope, TTL: time.Hour, HS256Secret: secret})
		if err == nil {
			t.Fatalf("Issue with scope %q succeeded; want a refusal", scope)
		}
		if !strings.Contains(err.Error(), ScopeAPI) {
			t.Fatalf("Issue(%q) error %q must name the accepted scope %q", scope, err, ScopeAPI)
		}
	}
	if _, err := Issue(IssueOptions{Subject: "x", Scope: ScopeAPI, TTL: time.Hour, HS256Secret: secret}); err != nil {
		t.Fatalf("Issue with an api scope: %v", err)
	}
	if _, err := Issue(IssueOptions{Subject: "x", Scope: ScopeCaretaker, TTL: time.Hour, HS256Secret: secret}); err != nil {
		t.Fatalf("Issue with a caretaker scope: %v", err)
	}
	if _, err := Issue(IssueOptions{Subject: "x", Scope: ScopePeer, TTL: time.Hour, HS256Secret: secret}); err != nil {
		t.Fatalf("Issue with a peer scope: %v", err)
	}
	if _, err := Issue(IssueOptions{Subject: "x", Scope: ScopeRegistryPull, TTL: time.Hour, HS256Secret: secret}); err != nil {
		t.Fatalf("Issue with a registry pull scope: %v", err)
	}
	if _, err := Issue(IssueOptions{Subject: "x", Scope: ScopeRegistryPush, TTL: time.Hour, HS256Secret: secret}); err != nil {
		t.Fatalf("Issue with a registry push scope: %v", err)
	}
}

func TestAllows(t *testing.T) {
	cases := []struct {
		access   Access
		endpoint Endpoint
		want     bool
	}{
		{AccessFull, EndpointAPI, true},
		{AccessFull, EndpointRegistryPull, true},
		{AccessFull, EndpointRegistryPush, true},
		{AccessFull, EndpointCaretaker, true},
		{AccessFull, EndpointPeerForward, true},
		{AccessRegistryPush, EndpointRegistryPull, true},
		{AccessRegistryPush, EndpointRegistryPush, true},
		{AccessRegistryPush, EndpointAPI, false},
		{AccessRegistryPush, EndpointCaretaker, false},
		{AccessRegistryPush, EndpointPeerForward, false},
		{AccessRegistryPull, EndpointRegistryPull, true},
		{AccessRegistryPull, EndpointRegistryPush, false},
		{AccessRegistryPull, EndpointAPI, false},
		{AccessRegistryPull, EndpointCaretaker, false},
		{AccessRegistryPull, EndpointPeerForward, false},
		{AccessCaretaker, EndpointCaretaker, true},
		{AccessCaretaker, EndpointAPI, false},
		{AccessCaretaker, EndpointRegistryPull, false},
		{AccessCaretaker, EndpointRegistryPush, false},
		{AccessCaretaker, EndpointPeerForward, false},
		{AccessPeer, EndpointPeerForward, true},
		{AccessPeer, EndpointAPI, false},
		{AccessPeer, EndpointRegistryPull, false},
		{AccessPeer, EndpointRegistryPush, false},
		{AccessPeer, EndpointCaretaker, false},
		{AccessNone, EndpointAPI, false},
		{AccessFull, EndpointUnknown, false},
		{Access(99), EndpointAPI, false},
		{AccessFull, Endpoint(99), false},
	}
	for _, c := range cases {
		if got := Allows(c.access, c.endpoint); got != c.want {
			t.Errorf("Allows(%v, %v) = %v, want %v", c.access, c.endpoint, got, c.want)
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

	tok, err := Issue(IssueOptions{Subject: "svc", Scope: ScopeAPI, TTL: time.Hour, PrivateKeyPEM: pemKey})
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
	tok, err := Issue(IssueOptions{Subject: "x", Scope: ScopeAPI, TTL: time.Hour, KeyID: "key-1", HS256Secret: secret})
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
	plain, _ := Issue(IssueOptions{Subject: "x", Scope: ScopeAPI, TTL: time.Hour, HS256Secret: secret})
	p2, _ := jwt.ParseSigned(plain, []jose.SignatureAlgorithm{jose.HS256})
	if len(p2.Headers) > 0 && p2.Headers[0].KeyID != "" {
		t.Fatalf("unexpected kid: %q", p2.Headers[0].KeyID)
	}
}

func TestIssueErrors(t *testing.T) {
	// No key.
	if _, err := Issue(IssueOptions{Subject: "x", Scope: ScopeAPI, TTL: time.Hour}); err == nil {
		t.Fatal("want error with no signing key")
	}
	// Both keys.
	if _, err := Issue(IssueOptions{Scope: ScopeAPI, TTL: time.Hour, HS256Secret: []byte("0123456789012345678901234567890123"), PrivateKeyPEM: []byte("x")}); err == nil {
		t.Fatal("want error with two signing keys")
	}
	// Non-positive TTL.
	if _, err := Issue(IssueOptions{Scope: ScopeAPI, HS256Secret: []byte("0123456789012345678901234567890123")}); err == nil {
		t.Fatal("want error with non-positive ttl")
	}
}
