package sshclient

import (
	"crypto/ed25519"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestHostKeyCallbackFailsClosed(t *testing.T) {
	// No configuration at all -> error (never trust silently).
	if _, err := HostKeyCallback("", "", false); err == nil {
		t.Fatal("HostKeyCallback with nothing configured = nil error, want error")
	}
	// Insecure opt-in -> a callback that accepts anything.
	if cb, err := HostKeyCallback("", "", true); err != nil || cb == nil {
		t.Fatalf("insecure HostKeyCallback = %v, %v", cb, err)
	}
	// A bad pinned host key -> error.
	if _, err := HostKeyCallback("", "not-a-key", false); err == nil {
		t.Fatal("HostKeyCallback with a bad pinned key = nil error, want error")
	}
}

func TestAuthMethodsIdentityFile(t *testing.T) {
	_, keyPath := writeKey(t, "")
	methods, err := AuthMethods([]string{keyPath}, nil, nil)
	if err != nil || len(methods) != 1 {
		t.Fatalf("AuthMethods(identity) = %d methods, %v", len(methods), err)
	}

	// No agent and no identity file -> error.
	if _, err := AuthMethods(nil, nil, nil); err == nil {
		t.Fatal("AuthMethods with nothing = nil error, want error")
	}
}

func TestAuthMethodsEncryptedKey(t *testing.T) {
	_, keyPath := writeKey(t, "s3cret")

	// prompt == nil -> fail closed (the reconnect case).
	if _, err := AuthMethods([]string{keyPath}, nil, nil); err == nil {
		t.Fatal("AuthMethods on an encrypted key with nil prompt = nil error, want fail-closed")
	}

	// Correct passphrase decrypts.
	good := func(string) ([]byte, error) { return []byte("s3cret"), nil }
	if methods, err := AuthMethods([]string{keyPath}, nil, good); err != nil || len(methods) != 1 {
		t.Fatalf("AuthMethods with correct passphrase = %d, %v", len(methods), err)
	}

	// Wrong passphrase errors.
	bad := func(string) ([]byte, error) { return []byte("nope"), nil }
	if _, err := AuthMethods([]string{keyPath}, nil, bad); err == nil {
		t.Fatal("AuthMethods with wrong passphrase = nil error, want error")
	}
}

func TestExpandTokens(t *testing.T) {
	got := expandTokens("%r@%h:%p (%%)", "host.example", "2222", "ops")
	want := "ops@host.example:2222 (%)"
	if got != want {
		t.Fatalf("expandTokens = %q, want %q", got, want)
	}
	// No tokens -> unchanged.
	if got := expandTokens("plain", "h", "p", "u"); got != "plain" {
		t.Fatalf("expandTokens(plain) = %q", got)
	}
}

// writeKey writes an ed25519 private key to disk, optionally passphrase-encrypted,
// and returns its public key and path.
func writeKey(t *testing.T, passphrase string) (ssh.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return sshPub, path
}

func TestNewInteractivePromptAskpass(t *testing.T) {
	// A fake SSH_ASKPASS program that prints a fixed passphrase.
	dir := t.TempDir()
	prog := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\necho from-askpass\n"
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatalf("write askpass: %v", err)
	}
	t.Setenv("SSH_ASKPASS", prog)
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")

	prompt := NewInteractivePrompt()
	if prompt == nil {
		t.Fatal("NewInteractivePrompt() = nil with SSH_ASKPASS_REQUIRE=force and SSH_ASKPASS set")
	}
	pass, err := prompt("/path/to/key")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if strings.TrimSpace(string(pass)) != "from-askpass" {
		t.Fatalf("askpass passphrase = %q, want from-askpass", pass)
	}

	// force with no program -> nil (cannot prompt).
	t.Setenv("SSH_ASKPASS", "")
	if NewInteractivePrompt() != nil {
		t.Fatal("NewInteractivePrompt() != nil with force and no SSH_ASKPASS")
	}
}
