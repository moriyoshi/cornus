package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/authtoken"
	"cornus/pkg/wire"
)

func mintPeerToken(t *testing.T, key *peerKeypair, signerID, headerID string) string {
	t.Helper()
	token, err := authtoken.Issue(authtoken.IssueOptions{
		Subject:       signerID,
		Scope:         authtoken.ScopePeer,
		Issuer:        peerIssuer,
		Audience:      peerAudience,
		TTL:           peerTokenTTL,
		KeyID:         headerID,
		PrivateKeyPEM: key.PrivatePEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestPeerJWTAuthenticatesOnlyForwardEndpoints(t *testing.T) {
	key, err := loadPeerKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &authenticator{
		staticToken: []byte("operator-enables-auth"),
		peerKeyLookup: func(replicaID string) ([]byte, bool, error) {
			if replicaID != "replica-a" {
				return nil, false, nil
			}
			return key.PublicPEM, true, nil
		},
	}
	h := a.wrap(okHandler())
	token := mintPeerToken(t, key, "replica-a", "replica-a")

	for _, path := range []string{
		"/.cornus/v1/hub/forward",
		"/.cornus/v1/mount/forward",
		"/.cornus/v1/cred/forward",
	} {
		if rec := doReq(t, h, http.MethodGet, path, token); rec.Code != http.StatusOK {
			t.Fatalf("peer JWT on %s: code = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/.cornus/v1/deploy",
		"/.cornus/v1/caretaker/attach",
		"/v2/example/manifests/latest",
	} {
		if rec := doReq(t, h, http.MethodGet, path, token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("peer JWT on %s: code = %d, want 401", path, rec.Code)
		}
	}
}

func TestPeerJWTRejectsForgedKeyAndSubject(t *testing.T) {
	keyA, err := loadPeerKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := loadPeerKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &authenticator{
		staticToken: []byte("operator-enables-auth"),
		peerKeyLookup: func(replicaID string) ([]byte, bool, error) {
			if replicaID == "replica-a" {
				return keyA.PublicPEM, true, nil
			}
			return nil, false, nil
		},
	}
	h := a.wrap(okHandler())
	path := "/.cornus/v1/hub/forward"

	// A different private key cannot impersonate the kid selected from the store.
	forged := mintPeerToken(t, keyB, "replica-a", "replica-a")
	if rec := doReq(t, h, http.MethodGet, path, forged); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged peer key: code = %d, want 401", rec.Code)
	}

	// The signed subject is bound to kid even when the signature itself is valid.
	wrongSubject := mintPeerToken(t, keyA, "replica-b", "replica-a")
	if rec := doReq(t, h, http.MethodGet, path, wrongSubject); rec.Code != http.StatusUnauthorized {
		t.Fatalf("peer JWT with sub != kid: code = %d, want 401", rec.Code)
	}
}

func TestPeerJWTRejectsMissingKeyStoreFailureAndAlgorithmConfusion(t *testing.T) {
	key, err := loadPeerKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := "/.cornus/v1/hub/forward"
	token := mintPeerToken(t, key, "replica-a", "replica-a")
	for _, tc := range []struct {
		name   string
		lookup func(string) ([]byte, bool, error)
	}{
		{name: "missing", lookup: func(string) ([]byte, bool, error) { return nil, false, nil }},
		{name: "store failure", lookup: func(string) ([]byte, bool, error) { return nil, false, errors.New("store unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &authenticator{staticToken: []byte("operator-enables-auth"), peerKeyLookup: tc.lookup}
			if rec := doReq(t, a.wrap(okHandler()), http.MethodGet, path, token); rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d, want 401", rec.Code)
			}
		})
	}

	// Public-key bytes are never usable as an HMAC secret: the peer verifier
	// allows ES256 only and rejects this token before key lookup/signature use.
	hsToken, err := authtoken.Issue(authtoken.IssueOptions{
		Subject: "replica-a", Scope: authtoken.ScopePeer, Issuer: peerIssuer,
		Audience: peerAudience, TTL: time.Minute, KeyID: "replica-a", HS256Secret: key.PublicPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &authenticator{
		staticToken: []byte("operator-enables-auth"),
		peerKeyLookup: func(string) ([]byte, bool, error) {
			return key.PublicPEM, true, nil
		},
	}
	if rec := doReq(t, a.wrap(okHandler()), http.MethodGet, path, hsToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HS256/public-key confusion token: code = %d, want 401", rec.Code)
	}
}

func TestPeerForwardTokenCacheAndRefresh(t *testing.T) {
	key, err := loadPeerKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{peerKey: key, replicaID: "replica-a"}
	first, err := s.peerForwardToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.peerForwardToken()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("peer token was re-signed while safely outside the refresh window")
	}
	a := &authenticator{peerKeyLookup: func(replicaID string) ([]byte, bool, error) {
		return key.PublicPEM, replicaID == "replica-a", nil
	}}
	sub, scope, lookupErr, ok := a.verifyPeerJWT(first)
	if lookupErr != nil || !ok {
		t.Fatalf("minted token did not verify: ok = %v, error = %v", ok, lookupErr)
	}
	if sub != "replica-a" || scope != authtoken.ScopePeer {
		t.Fatalf("minted claims = (%q, %q), want (replica-a, peer)", sub, scope)
	}
	s.peerToken.mu.Lock()
	s.peerToken.expires = time.Now().Add(peerTokenRefreshBefore / 2)
	s.peerToken.mu.Unlock()
	refreshed, err := s.peerForwardToken()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == first {
		t.Fatal("peer token was not refreshed inside the refresh window")
	}
}

func TestDialForwardStaticTokenHasAbsolutePrecedence(t *testing.T) {
	headerCh := make(chan string, 1)
	lineCh := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerCh <- r.Header.Get("Authorization")
		conn, err := wire.AcceptConn(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := wire.ReadLine(conn)
		if err == nil {
			lineCh <- line
		}
	}))
	defer upstream.Close()

	// The malformed peer key makes any fallback mint fail. A successful dial
	// therefore also proves that the peer branch was never evaluated.
	s := &Server{
		forwardToken: "legacy-token",
		peerKey:      &peerKeypair{PrivatePEM: []byte("not-a-key")},
		replicaID:    "replica-a",
	}
	forwardAddr := "ws" + strings.TrimPrefix(upstream.URL, "http")
	conn, err := s.dialForward(context.Background(), forwardAddr, "/forward", "payload")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	select {
	case got := <-headerCh:
		if got != "Bearer legacy-token" {
			t.Fatalf("Authorization = %q, want byte-exact legacy bearer", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forward Authorization header")
	}
	select {
	case got := <-lineCh:
		if got != "payload" {
			t.Fatalf("forward payload = %q, want payload", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forward payload")
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("drain forward connection: %v", err)
	}
}
