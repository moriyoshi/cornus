package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLoadSignerIdentityFile(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, cleanup, err := LoadSigner(path, "", nil)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	defer cleanup()
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("key type = %q, want %q", signer.PublicKey().Type(), ssh.KeyAlgoED25519)
	}
}

func TestLoadSignerFingerprintPinsIdentity(t *testing.T) {
	_, keyPath := writeKey(t, "")
	signer, cleanup, err := LoadSigner(keyPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	cleanup()
	if _, cleanup, err := LoadSigner(keyPath, fingerprint, nil); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	} else {
		cleanup()
	}
	if _, _, err := LoadSigner(keyPath, "SHA256:not-this-key", nil); err == nil {
		t.Fatal("mismatched identity fingerprint accepted")
	}
}
