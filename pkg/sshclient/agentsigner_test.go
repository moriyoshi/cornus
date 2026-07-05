package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"cornus/pkg/sshkeyauth"
)

// Selecting a key from SSH_AUTH_SOCK by fingerprint is a shipped, documented
// feature (docs/reference/connection-config.md: with no identity file, the
// fingerprint selects an agent key) and the path the background client agent
// takes when a profile carries no identity file — yet nothing exercised it.
// auth-ssh-key.star drives --identity-file throughout, so the agent branch of
// LoadSigner was never reached by any test, unit or E2E.
//
// These cover it hermetically: an in-process ssh-agent on a unix socket in the
// test's own temp dir, so nothing depends on the developer's real agent (or on
// having one at all), and `go test ./...` needs no daemon.

// serveAgent starts an in-process ssh-agent holding keys, and points
// SSH_AUTH_SOCK at it for the duration of the test.
func serveAgent(t *testing.T, keys ...any) agent.Agent {
	t.Helper()
	keyring := agent.NewKeyring()
	for _, k := range keys {
		if err := keyring.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatalf("add key to keyring: %v", err)
		}
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed at cleanup
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	return keyring
}

func ed25519Key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// writeEd25519 writes an unencrypted PKCS#8 private key, the shape LoadSigner's
// identity-file branch reads.
func writeEd25519(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fingerprintOf(t *testing.T, key any) string {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.FingerprintSHA256(signer.PublicKey())
}

func TestLoadSignerSelectsAgentKeyByFingerprint(t *testing.T) {
	wanted, other := ed25519Key(t), ed25519Key(t)
	serveAgent(t, wanted, other)

	want := fingerprintOf(t, wanted)
	signer, cleanup, err := LoadSigner("", want, nil)
	if err != nil {
		t.Fatalf("LoadSigner from agent: %v", err)
	}
	defer cleanup()
	if got := ssh.FingerprintSHA256(signer.PublicKey()); got != want {
		// Two keys are in the agent precisely so this can fail: a LoadSigner that
		// returned the first signer regardless would pass with only one.
		t.Fatalf("selected %s, want %s", got, want)
	}
}

func TestLoadSignerAgentErrors(t *testing.T) {
	t.Run("fingerprint not in agent", func(t *testing.T) {
		serveAgent(t, ed25519Key(t))
		absent := fingerprintOf(t, ed25519Key(t))
		_, _, err := LoadSigner("", absent, nil)
		if err == nil {
			t.Fatal("LoadSigner accepted a fingerprint the agent does not hold")
		}
		if !strings.Contains(err.Error(), "not present in ssh-agent") {
			t.Fatalf("err = %v, want it to name the agent", err)
		}
	})

	t.Run("no SSH_AUTH_SOCK", func(t *testing.T) {
		t.Setenv("SSH_AUTH_SOCK", "")
		_, _, err := LoadSigner("", "SHA256:whatever", nil)
		if err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
			t.Fatalf("err = %v, want it to name SSH_AUTH_SOCK", err)
		}
	})

	t.Run("unreachable agent socket", func(t *testing.T) {
		t.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "does-not-exist.sock"))
		_, _, err := LoadSigner("", "SHA256:whatever", nil)
		if err == nil || !strings.Contains(err.Error(), "connect to ssh-agent") {
			t.Fatalf("err = %v, want a connect failure", err)
		}
	})

	t.Run("neither identity file nor fingerprint", func(t *testing.T) {
		_, _, err := LoadSigner("", "", nil)
		if err == nil {
			t.Fatal("LoadSigner accepted an empty configuration")
		}
	})
}

// TestAgentSignerProducesRSASHA2 is the reason this file exists beyond key
// selection.
//
// sshkeyauth.Sign refuses legacy ssh-rsa and requires an RSA signer to implement
// ssh.AlgorithmSigner so it can ask for rsa-sha2-512. An agent-backed signer is a
// DIFFERENT implementation of that interface from a file-backed one — the
// signing happens in the agent process, over the wire — so "RSA works" proven
// against a file signer says nothing about it. Nothing else in the tree drives an
// agent-backed RSA signer, and a regression would surface as an unexplained
// authentication failure only for users whose key lives in an agent.
func TestAgentSignerProducesRSASHA2(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serveAgent(t, rsaKey)

	signer, cleanup, err := LoadSigner("", fingerprintOf(t, rsaKey), nil)
	if err != nil {
		t.Fatalf("LoadSigner from agent: %v", err)
	}
	defer cleanup()
	if _, ok := signer.(ssh.AlgorithmSigner); !ok {
		t.Fatal("the agent-backed RSA signer does not implement ssh.AlgorithmSigner, so sshkeyauth.Sign cannot request SHA-2")
	}

	const challenge = "chal"
	binding := []byte("binding")
	proof, err := sshkeyauth.Sign(signer, sshkeyauth.PurposeSession, challenge, binding)
	if err != nil {
		t.Fatalf("Sign through the agent: %v", err)
	}
	if proof.Algorithm != ssh.KeyAlgoRSASHA512 {
		t.Fatalf("algorithm = %q, want %q — an agent signer must not fall back to legacy ssh-rsa", proof.Algorithm, ssh.KeyAlgoRSASHA512)
	}
	if err := sshkeyauth.Verify(signer.PublicKey(), sshkeyauth.PurposeSession, challenge, binding, proof); err != nil {
		t.Fatalf("Verify of an agent-signed proof: %v", err)
	}
}

// TestAgentSignerEd25519RoundTrip: the common case, end to end through the same
// sign/verify pair the server uses, so agent selection is proven to yield a
// signer that actually authenticates rather than merely one with the right
// fingerprint.
func TestAgentSignerEd25519RoundTrip(t *testing.T) {
	key := ed25519Key(t)
	serveAgent(t, key)

	signer, cleanup, err := LoadSigner("", fingerprintOf(t, key), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	const challenge = "chal"
	binding := []byte("b")
	proof, err := sshkeyauth.Sign(signer, sshkeyauth.PurposeEnroll, challenge, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := sshkeyauth.Verify(signer.PublicKey(), sshkeyauth.PurposeEnroll, challenge, binding, proof); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// A proof is bound to its purpose: the same signature must not satisfy a
	// different one. Cheap to assert here, and the agent path is a plausible place
	// for a binding mistake to hide.
	if err := sshkeyauth.Verify(signer.PublicKey(), sshkeyauth.PurposeSession, challenge, binding, proof); err == nil {
		t.Fatal("an enroll proof verified as a session proof")
	}
}

// TestLoadSignerIdentityFileBeatsAgent pins the precedence documented for a
// profile that sets both: the file is used, and the fingerprint is a CHECK on it
// rather than an agent lookup. Without this, a profile with a stale fingerprint
// could silently authenticate as whatever the agent happened to hold.
func TestLoadSignerIdentityFileBeatsAgent(t *testing.T) {
	agentKey := ed25519Key(t)
	serveAgent(t, agentKey)

	fileKey := ed25519Key(t)
	path := writeEd25519(t, fileKey)

	// Matching fingerprint: the file signer is returned.
	signer, cleanup, err := LoadSigner(path, fingerprintOf(t, fileKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := ssh.FingerprintSHA256(signer.PublicKey()); got != fingerprintOf(t, fileKey) {
		t.Fatalf("selected %s, want the identity file's key", got)
	}

	// Mismatched fingerprint: refused, NOT silently satisfied from the agent even
	// though the agent holds a key with that fingerprint.
	_, _, err = LoadSigner(path, fingerprintOf(t, agentKey), nil)
	if err == nil {
		t.Fatal("a fingerprint that does not match the identity file was accepted")
	}
	if !strings.Contains(err.Error(), "does not match fingerprint") {
		t.Fatalf("err = %v, want a fingerprint mismatch", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("the identity file was not read at all")
	}
}
