package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"cornus/pkg/authtoken"
)

// ecKey is a test signing key with an associated kid.
type ecKey struct {
	priv *ecdsa.PrivateKey
	kid  string
}

func genEC(t *testing.T, kid string) ecKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return ecKey{priv: priv, kid: kid}
}

// jwksOf marshals a JWKS document over the public halves of the given keys.
func jwksOf(t *testing.T, keys ...ecKey) []byte {
	t.Helper()
	set := jose.JSONWebKeySet{}
	for _, k := range keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{
			Key:       &k.priv.PublicKey,
			KeyID:     k.kid,
			Algorithm: string(jose.ES256),
			Use:       "sig",
		})
	}
	b, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mintKid signs a token with headerKid stamped, using signer's private key. Passing
// a headerKid that differs from signer.kid models a kid/key mismatch.
func mintKid(t *testing.T, signer ecKey, headerKid, sub string) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(signer.priv)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	tok, err := authtoken.Issue(authtoken.IssueOptions{
		Subject: sub, Scope: authtoken.ScopeAPI, TTL: time.Hour, KeyID: headerKid, PrivateKeyPEM: pemKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestJWKSFileVerifyAndRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwks.json")
	ka := genEC(t, "k1")
	if err := os.WriteFile(path, jwksOf(t, ka), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &authenticator{jwks: &jwksResolver{src: &jwksFileSource{path: path}}}
	h := a.wrap(okHandler())

	// A token whose kid is in the set authenticates.
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", mintKid(t, ka, "k1", "svc")); rec.Code != http.StatusOK {
		t.Fatalf("k1 token: code = %d, want 200", rec.Code)
	}

	// A token whose kid is NOT in the set is rejected.
	kb := genEC(t, "k2")
	tokB := mintKid(t, kb, "k2", "svc")
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tokB); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown kid: code = %d, want 401", rec.Code)
	}

	// Rotate the file to include k2; the reloader picks it up (advance mtime to avoid
	// coarse-mtime flakiness) and the k2 token now authenticates.
	if err := os.WriteFile(path, jwksOf(t, ka, kb), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", tokB); rec.Code != http.StatusOK {
		t.Fatalf("k2 token after rotation: code = %d, want 200 (reload should pick up the new key)", rec.Code)
	}
}

func TestJWKSRejectsHS256AndMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwks.json")
	ka := genEC(t, "k1")
	if err := os.WriteFile(path, jwksOf(t, ka), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &authenticator{jwks: &jwksResolver{src: &jwksFileSource{path: path}}}
	h := a.wrap(okHandler())

	// An HS256 token must never verify against a JWKS (no HMAC alg allowed).
	hs, err := authtoken.Issue(authtoken.IssueOptions{Subject: "x", TTL: time.Hour, HS256Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", hs); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HS256 token vs JWKS: code = %d, want 401", rec.Code)
	}

	// A token claiming kid k1 but signed by a DIFFERENT key fails the signature check.
	other := genEC(t, "other")
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", mintKid(t, other, "k1", "x")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("kid/key mismatch: code = %d, want 401", rec.Code)
	}
}

func TestJWKSURLRefetchOnUnknownKid(t *testing.T) {
	ka := genEC(t, "k1")
	kb := genEC(t, "k2")

	var mu sync.Mutex
	body := jwksOf(t, ka) // initially only k1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b := body
		mu.Unlock()
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	src := &jwksURLSource{url: srv.URL, client: srv.Client(), ttl: time.Hour, minRefetch: 0}
	a := &authenticator{jwks: &jwksResolver{src: src}}
	h := a.wrap(okHandler())

	// First verify fetches the set (k1 only).
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", mintKid(t, ka, "k1", "svc")); rec.Code != http.StatusOK {
		t.Fatalf("k1 token: code = %d, want 200", rec.Code)
	}

	// Rotate at the source to include k2. The TTL has not expired, so a k2 token is
	// unknown in the cache; find() forces one refresh and picks it up.
	mu.Lock()
	body = jwksOf(t, ka, kb)
	mu.Unlock()
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", mintKid(t, kb, "k2", "svc")); rec.Code != http.StatusOK {
		t.Fatalf("k2 token after rotation: code = %d, want 200 (unknown-kid should trigger a refetch)", rec.Code)
	}
}

// TestJWKSURLNegativeCacheOnFailure proves a failed fetch is negatively cached:
// once the cache is older than ttl and the issuer is down, repeated get() calls
// must NOT poll the issuer once per call (which would serialize a 10s HTTP GET on
// the lock and collapse throughput) — the failed attempt is recorded so the next
// fetch is gated by minRefetch, and the last good set keeps being served.
func TestJWKSURLNegativeCacheOnFailure(t *testing.T) {
	ka := genEC(t, "k1")
	set, err := parseJWKS(jwksOf(t, ka))
	if err != nil {
		t.Fatal(err)
	}

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError) // issuer is "down"
	}))
	defer srv.Close()

	src := &jwksURLSource{
		url:        srv.URL,
		client:     srv.Client(),
		ttl:        5 * time.Minute,
		minRefetch: time.Minute,
		cached:     set,
		fetchedAt:  time.Now().Add(-10 * time.Minute), // older than ttl -> get() would re-fetch
	}

	for i := 0; i < 20; i++ {
		got, err := src.get()
		if err != nil || got != set {
			t.Fatalf("get %d = (%v, %v), want the last good set served through the outage", i, got, err)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("issuer polled %d times, want exactly 1 (a failed fetch must be negatively cached)", n)
	}
}
