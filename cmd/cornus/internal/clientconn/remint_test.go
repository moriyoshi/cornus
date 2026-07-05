package clientconn

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"cornus/pkg/client"
	"cornus/pkg/sshclient"
	"cornus/pkg/sshkeyauth"
)

// writeTestIdentity generates an ed25519 key, writes it as an unencrypted OpenSSH
// private key, and returns the path plus its SHA256 fingerprint.
func writeTestIdentity(t *testing.T) (path, fingerprint string, pub ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, ssh.FingerprintSHA256(signer.PublicKey()), signer.PublicKey()
}

// tokenEndpoint serves the two-step SSH-proof exchange for /.cornus/v1/auth/token:
// an unproven POST gets a 401 carrying a challenge, and the signed retry gets a
// token. It VERIFIES the proof rather than waving it through, so the test proves
// the provider signed with the configured identity and not merely that it posted.
func tokenEndpoint(t *testing.T, want ssh.PublicKey, token string, expiresAt time.Time, calls *int) http.Handler {
	t.Helper()
	secret := []byte("test-challenge-secret")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var req struct {
			PublicKey string            `json:"publicKey"`
			Challenge string            `json:"challenge"`
			Proof     *sshkeyauth.Proof `json:"proof"`
			Scope     string            `json:"scope"`
			TTL       string            `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode token request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		binding := sshkeyauth.SessionBinding(req.PublicKey, req.Scope, req.TTL)
		if req.Proof == nil {
			challenge, err := sshkeyauth.IssueChallenge(secret, sshkeyauth.PurposeSession, binding, time.Now(), time.Minute)
			if err != nil {
				t.Errorf("issue challenge: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("WWW-Authenticate", `CornusSSH challenge="`+challenge+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
			return
		}
		if err := sshkeyauth.VerifyChallenge(secret, req.Challenge, sshkeyauth.PurposeSession, binding, time.Now()); err != nil {
			t.Errorf("challenge did not verify: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := sshkeyauth.Verify(want, sshkeyauth.PurposeSession, req.Challenge, binding, *req.Proof); err != nil {
			t.Errorf("proof did not verify against the enrolled key: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "expiresAt": expiresAt})
	})
}

// TestProviderMintsAReplacementWhenTheSessionExpired closes the one path
// sshKeyTokenProvider had no direct coverage for: the held session is expired,
// the shared cache has nothing fresher, so it must LOAD THE KEY, SIGN, and mint a
// replacement. The two existing tests both assert the provider does NOT mint (a
// fresher shared session is adopted; a live session is reused) and install a
// newClient that panics — so nothing exercised the signing path itself.
func TestProviderMintsAReplacementWhenTheSessionExpired(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// Pin the file backend: without it the shared credential store uses the OS
	// keyring where one is reachable, and this test would write to the developer's.
	t.Setenv("CORNUS_TOKEN_CACHE", "file")

	identityFile, fingerprint, pub := writeTestIdentity(t)
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	calls := 0
	srv := httptest.NewServer(tokenEndpoint(t, pub, "minted-token", expiresAt, &calls))
	defer srv.Close()

	p := &sshKeyTokenProvider{
		identity:     "server|" + srv.URL,
		fingerprint:  fingerprint,
		scope:        "api",
		identityFile: identityFile,
		ttl:          "1h",
		newClient:    func() *client.Client { return client.New(srv.URL, client.WithToken("")) },
	}
	p.seed("expired-token", time.Now().Add(-time.Minute))

	got, err := p.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "minted-token" {
		t.Fatalf("Token = %q, want minted-token (the expired one was returned instead of a replacement)", got)
	}
	if calls != 2 {
		t.Errorf("token endpoint called %d times, want 2 (challenge then signed retry)", calls)
	}

	// The replacement is memoised, so the next call costs no signature.
	p.newClient = func() *client.Client { panic("must not re-mint: the freshly minted session is live") }
	if again, err := p.Token(); err != nil || again != "minted-token" {
		t.Fatalf("second Token = %q, %v; want the memoised minted-token", again, err)
	}

	// ...and shared, so a sibling process reuses it rather than signing again.
	cached, cachedExpiry, ok, err := sshclient.ReadSessionEntry(p.identity, fingerprint, "api")
	if err != nil || !ok {
		t.Fatalf("renewed session was not written to the shared cache (ok=%v, err=%v)", ok, err)
	}
	if cached != "minted-token" || !cachedExpiry.Equal(expiresAt) {
		t.Errorf("cached session = %q exp %v, want minted-token exp %v", cached, cachedExpiry, expiresAt)
	}
}

// TestProviderNonInteractiveFailsRatherThanPrompting pins the non-interactive
// contract. With interactive=false the provider passes a nil passphrase prompt to
// LoadSigner, so an ENCRYPTED identity must fail cleanly and promptly. The
// failure mode this guards against is the opposite of a wrong answer: a headless
// run (CI, a daemon, a script) blocking forever on a terminal prompt nobody can
// answer.
func TestProviderNonInteractiveFailsRatherThanPrompting(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CORNUS_TOKEN_CACHE", "file")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_locked")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &sshKeyTokenProvider{
		identity:     "server|http://127.0.0.1:5000",
		scope:        "api",
		identityFile: path,
		interactive:  false,
		newClient:    func() *client.Client { panic("must not reach the server without a usable signer") },
	}
	p.seed("expired-token", time.Now().Add(-time.Minute))

	done := make(chan struct{})
	var got string
	var gotErr error
	go func() {
		got, gotErr = p.Token()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Token blocked on an encrypted identity in non-interactive mode; it must fail instead of prompting")
	}
	if gotErr == nil {
		t.Fatal("expected an error for an encrypted identity with no prompt")
	}
	// The last known token comes back with the error on purpose: presenting an
	// expired credential yields a readable 401, which beats sending no header at
	// all. See sshKeyTokenProvider.Token.
	if got != "expired-token" {
		t.Errorf("Token = %q, want the last known token alongside the error", got)
	}
}
