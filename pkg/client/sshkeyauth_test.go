package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"cornus/pkg/sshkeyauth"
)

func TestSSHKeyAuthClientSignsPurposeBoundProofs(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789012345678901")
	var enrolled, enrollCalls, tokenCalls int
	mux := http.NewServeMux()
	issueChallenge := func(w http.ResponseWriter, purpose string, binding []byte) {
		challenge, err := sshkeyauth.IssueChallenge(secret, purpose, binding, time.Now(), time.Minute)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("WWW-Authenticate", `CornusSSH challenge="`+challenge+`"`)
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
	}
	verify := func(request sshProofRequest, purpose string, binding []byte) {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
		if err != nil {
			t.Errorf("parse public key: %v", err)
			return
		}
		if err := sshkeyauth.VerifyChallenge(secret, request.Challenge, purpose, binding, time.Now()); err != nil {
			t.Errorf("challenge: %v", err)
		}
		if request.Proof == nil {
			t.Error("missing proof")
			return
		}
		if err := sshkeyauth.Verify(key, purpose, request.Challenge, binding, *request.Proof); err != nil {
			t.Errorf("proof: %v", err)
		}
	}
	mux.HandleFunc("/.cornus/v1/auth/enroll", func(w http.ResponseWriter, r *http.Request) {
		enrollCalls++
		var request sshEnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode enrollment request: %v", err)
			return
		}
		binding := sshkeyauth.EnrollmentBinding(request.PublicKey, request.Code, request.Name)
		if request.Proof == nil {
			issueChallenge(w, sshkeyauth.PurposeEnroll, binding)
			return
		}
		verify(request.sshProofRequest, sshkeyauth.PurposeEnroll, binding)
		enrolled++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(SSHKeyInfo{Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), PublicKey: request.PublicKey})
	})
	mux.HandleFunc("/.cornus/v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		var request sshTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode token request: %v", err)
			return
		}
		binding := sshkeyauth.SessionBinding(request.PublicKey, request.Scope, request.TTL)
		if request.Proof == nil {
			issueChallenge(w, sshkeyauth.PurposeSession, binding)
			return
		}
		verify(request.sshProofRequest, sshkeyauth.PurposeSession, binding)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "session-token", "expiresAt": time.Now().Add(time.Hour)})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	c := New(server.URL, WithToken(""))
	if _, err := c.SSHEnroll(context.Background(), signer, "code", "laptop"); err != nil {
		t.Fatalf("SSHEnroll: %v", err)
	}
	if enrolled != 1 || enrollCalls != 2 {
		t.Fatalf("enrollment successes/calls = %d/%d, want 1/2", enrolled, enrollCalls)
	}
	token, expires, err := c.SSHKeyToken(context.Background(), signer, "api", "1h")
	if err != nil {
		t.Fatalf("SSHKeyToken: %v", err)
	}
	if token != "session-token" || expires.IsZero() {
		t.Fatalf("token = %q expires = %v", token, expires)
	}
	if tokenCalls != 2 {
		t.Fatalf("token calls = %d, want initial challenge plus proof retry", tokenCalls)
	}
}
