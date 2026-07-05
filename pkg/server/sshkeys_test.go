package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSSHPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSSHKeyStoreDefaultIsOff(t *testing.T) {
	t.Setenv(authKeyStoreEnv, "")
	t.Setenv(authorizedKeysEnv, "")
	dir := t.TempDir()
	store, err := newSSHKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Fatal("default store is enabled")
	}
	if _, err := os.Stat(filepath.Join(dir, authDirName)); !os.IsNotExist(err) {
		t.Fatalf("auth directory stat error = %v", err)
	}
}

func TestSSHKeyStoreFileModeCreatesDurableEnrollmentSecret(t *testing.T) {
	t.Setenv(authKeyStoreEnv, "file")
	t.Setenv(authorizedKeysEnv, "")
	dir := t.TempDir()
	store, err := newSSHKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.enrollmentSecret == "" {
		t.Fatal("empty enrollment secret")
	}
	info, err := os.Stat(store.enrollmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o", info.Mode().Perm())
	}
	again, err := newSSHKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.enrollmentSecret != store.enrollmentSecret {
		t.Fatal("enrollment secret changed across restart")
	}
}

func TestSSHAuthorizedKeyMetadataRoundTrip(t *testing.T) {
	t.Parallel()
	key := sshAuthorizedKey{PublicKey: testSSHPublicKey(t), Name: "Alice Example", Enrolled: time.Date(2026, 7, 27, 1, 2, 3, 0, time.UTC)}
	raw := marshalSSHAuthorizedKeys([]sshAuthorizedKey{key})
	if !strings.Contains(string(raw), `cornus-name="Alice Example"`) || !strings.Contains(string(raw), `cornus-enrolled="2026-07-27T01:02:03Z"`) {
		t.Fatalf("authorized_keys = %q", raw)
	}
	parsed, err := parseSSHAuthorizedKeys(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0].Name != key.Name || !parsed[0].Enrolled.Equal(key.Enrolled) {
		t.Fatalf("parsed = %#v", parsed)
	}
	if parsed[0].subject() != "Alice Example" {
		t.Fatalf("subject = %q", parsed[0].subject())
	}
}

func TestSSHAuthorizedKeyFingerprintSubject(t *testing.T) {
	t.Parallel()
	key := testSSHPublicKey(t)
	entry := sshAuthorizedKey{PublicKey: key}
	if got, want := entry.subject(), ssh.FingerprintSHA256(key); got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
}

func TestSSHKeyStoreNoneUsesConfiguredKeysWithoutFiles(t *testing.T) {
	key := testSSHPublicKey(t)
	t.Setenv(authKeyStoreEnv, "none")
	t.Setenv(authorizedKeysEnv, string(ssh.MarshalAuthorizedKey(key)))
	dir := t.TempDir()
	store, err := newSSHKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.keys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || ssh.FingerprintSHA256(keys[0].PublicKey) != ssh.FingerprintSHA256(key) {
		t.Fatalf("keys = %#v", keys)
	}
	if store.writable() {
		t.Fatal("none store is writable")
	}
	if _, err := os.Stat(filepath.Join(dir, authDirName)); !os.IsNotExist(err) {
		t.Fatalf("auth directory stat error = %v", err)
	}
}
