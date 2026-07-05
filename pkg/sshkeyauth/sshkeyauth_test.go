package sshkeyauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestProofPurposeBinding(t *testing.T) {
	t.Parallel()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := []byte("enrollment-request")
	proof, err := Sign(signer, PurposeEnroll, "challenge", binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signer.PublicKey(), PurposeEnroll, "challenge", binding, proof); err != nil {
		t.Fatal(err)
	}
	if err := Verify(signer.PublicKey(), PurposeSession, "challenge", binding, proof); err == nil {
		t.Fatal("enrollment proof verified for session purpose")
	}
	if err := Verify(signer.PublicKey(), PurposeEnroll, "other", binding, proof); err == nil {
		t.Fatal("proof verified for another challenge")
	}
	if err := Verify(signer.PublicKey(), PurposeEnroll, "challenge", []byte("changed-request"), proof); err == nil {
		t.Fatal("proof verified for another request binding")
	}
}

func TestProofRejectsDeclaredAlgorithmMismatch(t *testing.T) {
	t.Parallel()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := []byte("token-request")
	proof, err := Sign(signer, PurposeSession, "challenge", binding)
	if err != nil {
		t.Fatal(err)
	}
	proof.Algorithm = ssh.KeyAlgoRSASHA512
	err = Verify(signer.PublicKey(), PurposeSession, "challenge", binding, proof)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestProofRejectsRSASHA1(t *testing.T) {
	t.Parallel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := []byte("token-request")
	message := marshalPayload(signer.PublicKey(), PurposeSession, "challenge", binding)
	legacy, err := signer.Sign(rand.Reader, message)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Format != ssh.KeyAlgoRSA {
		t.Fatalf("test signer emitted %q, want ssh-rsa", legacy.Format)
	}
	err = Verify(signer.PublicKey(), PurposeSession, "challenge", binding, Proof{Algorithm: legacy.Format, Signature: *legacy})
	if err == nil || !strings.Contains(err.Error(), "legacy signature algorithm") {
		t.Fatalf("error = %v", err)
	}
}

func TestSignRSAUsesSHA2(t *testing.T) {
	t.Parallel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := []byte("token-request")
	proof, err := Sign(signer, PurposeSession, "challenge", binding)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Algorithm != ssh.KeyAlgoRSASHA512 {
		t.Fatalf("algorithm = %q", proof.Algorithm)
	}
	if err := Verify(signer.PublicKey(), PurposeSession, "challenge", binding, proof); err != nil {
		t.Fatal(err)
	}
}

func TestChallenge(t *testing.T) {
	t.Parallel()
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	binding := SessionBinding("ssh-ed25519 AAAA", "api", "1h")
	challenge, err := issueChallenge(secret, PurposeSession, binding, now, time.Minute, bytes.NewReader(make([]byte, challengeNonceSize)))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChallenge(secret, challenge, PurposeSession, binding, now.Add(time.Minute)); err != nil {
		t.Fatalf("at expiry: %v", err)
	}
	if err := VerifyChallenge(secret, challenge, PurposeSession, binding, now.Add(time.Minute+time.Second)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired error = %v", err)
	}
	if err := VerifyChallenge([]byte("different-secret"), challenge, PurposeSession, binding, now); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Fatalf("wrong-secret error = %v", err)
	}
	if err := VerifyChallenge(secret, challenge, PurposeEnroll, binding, now); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Fatalf("wrong-purpose error = %v", err)
	}
	if err := VerifyChallenge(secret, challenge, PurposeSession, []byte("changed request"), now); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Fatalf("wrong-binding error = %v", err)
	}
	raw := []byte(challenge)
	if raw[len(raw)-1] == 'A' {
		raw[len(raw)-1] = 'B'
	} else {
		raw[len(raw)-1] = 'A'
	}
	if err := VerifyChallenge(secret, string(raw), PurposeSession, binding, now); err == nil {
		t.Fatal("tampered challenge verified")
	}
}
