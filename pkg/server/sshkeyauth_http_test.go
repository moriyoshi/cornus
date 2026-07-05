package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"cornus/pkg/sshkeyauth"
)

func newSSHAuthTestServer(t *testing.T, mode, configured string) (*Server, http.Handler) {
	t.Helper()
	clearAuthEnv(t)
	t.Setenv(authKeyStoreEnv, mode)
	t.Setenv(authorizedKeysEnv, configured)
	t.Setenv(installationSecretEnv, "0123456789abcdef0123456789abcdef")
	auth, err := newAuthenticator(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: auth, mux: http.NewServeMux()}
	s.registerSSHAuthRoutes()
	s.mux.HandleFunc("/.cornus/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"subject": Subject(r), "internal": Internal(r)})
	})
	return s, auth.wrap(s.mux)
}

func newSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func sshChallenge(t *testing.T, handler http.Handler, path string, body any) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, path, body, ""))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("challenge status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	challenge := response["challenge"]
	if challenge == "" {
		t.Fatal("empty challenge")
	}
	if got, want := recorder.Header().Get("WWW-Authenticate"), `CornusSSH challenge="`+challenge+`"`; got != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
	}
	return challenge
}

func attachSSHProof(t *testing.T, request *sshProofRequest, signer ssh.Signer, purpose, challenge string, binding []byte) {
	t.Helper()
	proof, err := sshkeyauth.Sign(signer, purpose, challenge, binding)
	if err != nil {
		t.Fatal(err)
	}
	request.Challenge = challenge
	request.Proof = &proof
}

func jsonRequest(t *testing.T, method, path string, body any, token string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func TestSSHAuthEnrollmentTokenAndProtectedKeys(t *testing.T) {
	s, handler := newSSHAuthTestServer(t, "file", "")
	signer := newSSHSigner(t)
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	oldCode := s.auth.sshKeys.enrollmentSecret

	enroll := sshEnrollRequest{
		sshProofRequest: sshProofRequest{PublicKey: publicKey},
		Code:            oldCode,
		Name:            "alice",
	}
	enrollBinding := sshkeyauth.EnrollmentBinding(publicKey, oldCode, "alice")
	enrollChallenge := sshChallenge(t, handler, "/.cornus/v1/auth/enroll", enroll)
	attachSSHProof(t, &enroll.sshProofRequest, signer, sshkeyauth.PurposeEnroll, enrollChallenge, enrollBinding)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/enroll", enroll, ""))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enroll status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if s.auth.sshKeys.enrollmentSecret == oldCode {
		t.Fatal("enrollment code did not rotate")
	}

	// The consumed code cannot enroll a second key.
	secondSigner := newSSHSigner(t)
	secondPublicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(secondSigner.PublicKey())))
	second := sshEnrollRequest{sshProofRequest: sshProofRequest{PublicKey: secondPublicKey}, Code: oldCode, Name: "mallory"}
	secondBinding := sshkeyauth.EnrollmentBinding(secondPublicKey, oldCode, "mallory")
	challenge := sshChallenge(t, handler, "/.cornus/v1/auth/enroll", second)
	attachSSHProof(t, &second.sshProofRequest, secondSigner, sshkeyauth.PurposeEnroll, challenge, secondBinding)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/enroll", second, ""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("reused code status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	tokenRequest := sshTokenRequest{sshProofRequest: sshProofRequest{PublicKey: publicKey}}
	tokenBinding := sshkeyauth.SessionBinding(publicKey, "", "")
	tokenChallenge := sshChallenge(t, handler, "/.cornus/v1/auth/token", tokenRequest)
	attachSSHProof(t, &tokenRequest.sshProofRequest, signer, sshkeyauth.PurposeSession, tokenChallenge, tokenBinding)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/token", tokenRequest, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("token status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var tokenResponse struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatal(err)
	}
	if tokenResponse.Token == "" {
		t.Fatal("empty token")
	}

	// /.cornus/v1/auth/keys is not exempt; the minted full credential reaches it and the
	// normal API with the enrolled name, without the internal bypass marker.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.cornus/v1/auth/keys", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous keys status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/.cornus/v1/whoami", nil)
	request.Header.Set("Authorization", "Bearer "+tokenResponse.Token)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"subject":"alice"`) || !strings.Contains(recorder.Body.String(), `"internal":false`) {
		t.Fatalf("whoami status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSSHAuthRoutesAbsentWhenUnconfigured(t *testing.T) {
	clearAuthEnv(t)
	auth, err := newAuthenticator(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: auth, mux: http.NewServeMux()}
	s.registerSSHAuthRoutes()
	recorder := httptest.NewRecorder()
	auth.wrap(s.mux).ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/token", map[string]string{}, ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestSSHEnrollmentNoneModeReturnsConflict(t *testing.T) {
	signer := newSSHSigner(t)
	configured := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	_, handler := newSSHAuthTestServer(t, "none", configured)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/enroll", map[string]string{}, ""))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), authorizedKeysEnv) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSSHTokenRejectsRSASHA1EndToEnd(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	configured := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	publicKey := strings.TrimSpace(configured)
	_, handler := newSSHAuthTestServer(t, "none", configured)
	request := sshTokenRequest{sshProofRequest: sshProofRequest{PublicKey: publicKey}}
	binding := sshkeyauth.SessionBinding(publicKey, "", "")
	challenge := sshChallenge(t, handler, "/.cornus/v1/auth/token", request)
	bindingDigest := sha256.Sum256(binding)
	payload := ssh.Marshal(struct {
		Magic     string
		Purpose   string
		Challenge string
		PublicKey []byte
		Binding   []byte
	}{"cornus-ssh-key-proof-v2", sshkeyauth.PurposeSession, challenge, signer.PublicKey().Marshal(), bindingDigest[:]})
	legacy, err := signer.Sign(rand.Reader, payload)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Format != ssh.KeyAlgoRSA {
		t.Fatalf("legacy format = %q", legacy.Format)
	}
	request.Challenge = challenge
	request.Proof = &sshkeyauth.Proof{Algorithm: legacy.Format, Signature: *legacy}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/token", request, ""))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSSHTokenProofCannotAuthorizeChangedRequest(t *testing.T) {
	signer := newSSHSigner(t)
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	_, handler := newSSHAuthTestServer(t, "none", publicKey)
	request := sshTokenRequest{
		sshProofRequest: sshProofRequest{PublicKey: publicKey},
		Scope:           "registry:pull",
		TTL:             "15m",
	}
	binding := sshkeyauth.SessionBinding(publicKey, request.Scope, request.TTL)
	challenge := sshChallenge(t, handler, "/.cornus/v1/auth/token", request)
	attachSSHProof(t, &request.sshProofRequest, signer, sshkeyauth.PurposeSession, challenge, binding)

	// Simulate an intermediary widening the signed request after the proof was
	// produced. Both the challenge MAC and SSH signature are request-bound.
	request.Scope = "api"
	request.TTL = "24h"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, jsonRequest(t, http.MethodPost, "/.cornus/v1/auth/token", request, ""))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("mutated request status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
