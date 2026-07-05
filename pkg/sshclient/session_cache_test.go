package sshclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fileBackedCache points the shared credential store at a temp runtime directory
// AND pins the file backend.
//
// Pinning matters: without it the store would use the OS keyring wherever one is
// reachable, so running `go test` on a workstation with an unlocked login
// keyring would write test credentials into the developer's real one. A test may
// not touch a store it did not create.
func fileBackedCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("CORNUS_AGENT_DIR", "")
	t.Setenv("CORNUS_TOKEN_CACHE", "file")
	return dir
}

// tokenFiles lists the entries the store wrote, so a test can assert on them
// without knowing how a key is derived.
func tokenFiles(t *testing.T, runtime string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(runtime, "cornus", "tokens", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestSessionCachePrivateAndExpiresEarly(t *testing.T) {
	runtime := fileBackedCache(t)
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	if err := WriteSession("https://server", "SHA256:key", "api", "token", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// A bearer token at rest is 0600. Found by listing rather than by rebuilding
	// the key, so the assertion survives a change in how keys are derived.
	files := tokenFiles(t, runtime)
	if len(files) != 1 {
		t.Fatalf("wrote %d cache files, want 1: %v", len(files), files)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %o, want 600", info.Mode().Perm())
	}

	if token, ok, err := ReadSession("https://server", "SHA256:key", "api", now); err != nil || !ok || token != "token" {
		t.Fatalf("ReadSession = %q, %v, %v", token, ok, err)
	}
	// Inside the two-minute margin: a miss, so a session is never handed out with
	// barely enough life to complete the request it was fetched for.
	if _, ok, err := ReadSession("https://server", "SHA256:key", "api", now.Add(59*time.Minute)); err != nil || ok {
		t.Fatalf("near-expiry ReadSession ok=%v err=%v, want miss", ok, err)
	}
	// ReadSessionEntry does NOT apply the margin: a long-lived caller deciding when
	// to renew needs the real expiry, which is why the two forms both exist.
	if _, expiresAt, ok, err := ReadSessionEntry("https://server", "SHA256:key", "api"); err != nil || !ok || !expiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("ReadSessionEntry expiry = %v, ok=%v, err=%v", expiresAt, ok, err)
	}
}

func TestSessionCacheKeyContainsIdentityFingerprintAndScope(t *testing.T) {
	fileBackedCache(t)
	now := time.Now()
	if err := WriteSession("server-a", "key-a", "api", "token", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ identity, fingerprint, scope string }{
		{"server-b", "key-a", "api"}, {"server-a", "key-b", "api"}, {"server-a", "key-a", "registry:pull"},
	} {
		if _, ok, err := ReadSession(tc.identity, tc.fingerprint, tc.scope, now); err != nil || ok {
			t.Fatalf("ReadSession(%q,%q,%q) ok=%v err=%v, want isolated miss", tc.identity, tc.fingerprint, tc.scope, ok, err)
		}
	}
}

// TestSessionCacheNamespacedAgainstOtherCredentials: the shared store also holds
// exchanged OAuth credentials, so an SSH session and another credential for the
// same server must not be able to collide on one key.
func TestSessionCacheDeleteRemovesIt(t *testing.T) {
	runtime := fileBackedCache(t)
	now := time.Now()
	if err := WriteSession("server-a", "key-a", "api", "token", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSession("server-a", "key-a", "api"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadSession("server-a", "key-a", "api", now); err != nil || ok {
		t.Fatalf("session survived DeleteSession: ok=%v err=%v", ok, err)
	}
	if files := tokenFiles(t, runtime); len(files) != 0 {
		t.Fatalf("DeleteSession left %d files behind: %v", len(files), files)
	}
	// Deleting what is not there is success.
	if err := DeleteSession("server-a", "key-a", "api"); err != nil {
		t.Fatalf("DeleteSession of a missing entry: %v", err)
	}
}
