package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"cornus/pkg/authtoken"
	"cornus/pkg/sshkeyauth"
)

const (
	sshChallengeTTL = 5 * time.Minute
	sshTokenTTL     = time.Hour
	sshTokenMaxTTL  = 24 * time.Hour
)

type sshProofRequest struct {
	PublicKey string            `json:"publicKey"`
	Challenge string            `json:"challenge,omitempty"`
	Proof     *sshkeyauth.Proof `json:"proof,omitempty"`
}

type sshEnrollRequest struct {
	sshProofRequest
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
}

type sshTokenRequest struct {
	sshProofRequest
	Scope string `json:"scope,omitempty"`
	TTL   string `json:"ttl,omitempty"`
}

type sshKeyResponse struct {
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name,omitempty"`
	Subject     string    `json:"subject"`
	Enrolled    time.Time `json:"enrolled,omitempty"`
	PublicKey   string    `json:"publicKey"`
}

func (s *Server) registerSSHAuthRoutes() {
	if s.auth == nil || s.auth.sshKeys == nil {
		return
	}
	s.mux.HandleFunc("/.cornus/v1/auth/enroll", s.handleSSHEnroll)
	s.mux.HandleFunc("/.cornus/v1/auth/token", s.handleSSHToken)
	s.mux.HandleFunc("/.cornus/v1/auth/keys", s.handleSSHKeys)
}

func (s *Server) handleSSHEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.auth.sshKeys.writable() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errSSHKeyStoreReadOnly.Error()})
		return
	}
	var request sshEnrollRequest
	if err := decodeSSHAuthRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(request.Name)
	if name != request.Name || utf8.RuneCountInString(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be at most 128 characters without leading/trailing whitespace or control separators"})
		return
	}
	binding := sshkeyauth.EnrollmentBinding(request.PublicKey, request.Code, request.Name)
	if request.proofPending() {
		s.issueSSHChallenge(w, sshkeyauth.PurposeEnroll, binding)
		return
	}
	if err := request.validateProofFields(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	publicKey, err := verifySSHProof(s.auth.internalSecret, request.sshProofRequest, sshkeyauth.PurposeEnroll, binding)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	entry := sshAuthorizedKey{PublicKey: publicKey, Name: name, Enrolled: time.Now().UTC().Truncate(time.Second)}
	if err := s.auth.sshKeys.enroll(entry, request.Code); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errSSHKeyStoreReadOnly) || strings.Contains(err.Error(), "already authorized") {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, sshKeyInfo(entry))
}

func (s *Server) handleSSHToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var request sshTokenRequest
	if err := decodeSSHAuthRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	scope := strings.TrimSpace(request.Scope)
	if scope == "" {
		scope = authtoken.ScopeAPI
	}
	if _, err := authtoken.Grant(scope); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ttl := sshTokenTTL
	if request.TTL != "" {
		parsedTTL, err := time.ParseDuration(request.TTL)
		if err != nil || parsedTTL <= 0 || parsedTTL > sshTokenMaxTTL {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ttl must be a positive Go duration no greater than 24h"})
			return
		}
		ttl = parsedTTL
	}
	binding := sshkeyauth.SessionBinding(request.PublicKey, request.Scope, request.TTL)
	if request.proofPending() {
		s.issueSSHChallenge(w, sshkeyauth.PurposeSession, binding)
		return
	}
	if err := request.validateProofFields(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	publicKey, err := verifySSHProof(s.auth.internalSecret, request.sshProofRequest, sshkeyauth.PurposeSession, binding)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid SSH key proof"})
		return
	}
	fingerprint := ssh.FingerprintSHA256(publicKey)
	entry, ok, err := s.auth.sshKeys.find(fingerprint)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "SSH key is not authorized"})
		return
	}
	token, err := authtoken.Issue(authtoken.IssueOptions{
		Subject: entry.subject(), Scope: scope, Issuer: sshKeyIssuer, Audience: sshKeyAudience,
		TTL: ttl, HS256Secret: s.auth.internalSecret,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expiresAt": time.Now().Add(ttl).UTC()})
}

func (s *Server) handleSSHKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := s.auth.sshKeys.keys()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		response := make([]sshKeyResponse, 0, len(keys))
		for _, key := range keys {
			response = append(response, sshKeyInfo(key))
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodDelete:
		fingerprint := strings.TrimSpace(r.URL.Query().Get("fingerprint"))
		if fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing fingerprint query parameter"})
			return
		}
		if err := s.auth.sshKeys.delete(fingerprint); err != nil {
			switch {
			case os.IsNotExist(err):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "SSH key not found"})
			case errors.Is(err, errSSHKeyStoreReadOnly), strings.Contains(err.Error(), authorizedKeysEnv):
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (r sshProofRequest) proofPending() bool {
	return r.Challenge == "" && r.Proof == nil
}

func (r sshProofRequest) validateProofFields() error {
	if r.Challenge == "" || r.Proof == nil {
		return errors.New("challenge and proof must either both be omitted or both be present")
	}
	return nil
}

// issueSSHChallenge starts the authentication handshake on the endpoint that
// will consume the proof. The challenge is bound to the endpoint purpose and
// canonical request fields; changing any of them makes both its MAC and the
// subsequent SSH signature invalid.
func (s *Server) issueSSHChallenge(w http.ResponseWriter, purpose string, binding []byte) {
	challenge, err := sshkeyauth.IssueChallenge(s.auth.internalSecret, purpose, binding, time.Now(), sshChallengeTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("WWW-Authenticate", `CornusSSH challenge="`+challenge+`"`)
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error":     "SSH key proof required",
		"challenge": challenge,
	})
}

func decodeSSHAuthRequest(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func verifySSHProof(secret []byte, request sshProofRequest, purpose string, binding []byte) (ssh.PublicKey, error) {
	if err := sshkeyauth.VerifyChallenge(secret, request.Challenge, purpose, binding, time.Now()); err != nil {
		return nil, err
	}
	publicKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid SSH public key")
	}
	if err := sshkeyauth.Verify(publicKey, purpose, request.Challenge, binding, *request.Proof); err != nil {
		return nil, err
	}
	return publicKey, nil
}

func sshKeyInfo(key sshAuthorizedKey) sshKeyResponse {
	return sshKeyResponse{
		Fingerprint: ssh.FingerprintSHA256(key.PublicKey), Name: key.Name, Subject: key.subject(),
		Enrolled: key.Enrolled, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey))),
	}
}
