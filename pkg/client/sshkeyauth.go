package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"cornus/pkg/sshkeyauth"
)

// SSHKeyInfo describes one SSH public key authorized by a Cornus server.
type SSHKeyInfo struct {
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name,omitempty"`
	Subject     string    `json:"subject"`
	Enrolled    time.Time `json:"enrolled,omitempty"`
	PublicKey   string    `json:"publicKey"`
}

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

// sshProofRoundTrip performs a challenge handshake on the endpoint that will
// consume the proof. The first unsigned POST receives a request-bound challenge;
// the second repeats the same request with the SSH signature attached.
func (c *Client) sshProofRoundTrip(ctx context.Context, path, purpose string, signer ssh.Signer, binding []byte, request any, attach func(string, sshkeyauth.Proof)) (*http.Response, error) {
	post := func() (*http.Response, error) {
		body, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		return c.do(ctx, http.MethodPost, path, bytes.NewReader(body))
	}
	response, err := post()
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	var challengeResponse struct {
		Challenge string `json:"challenge"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&challengeResponse)
	_ = response.Body.Close()
	if decodeErr != nil {
		return nil, fmt.Errorf("decode SSH key challenge: %w", decodeErr)
	}
	challenge := challengeResponse.Challenge
	if challenge == "" {
		return nil, fmt.Errorf("server returned an empty SSH key challenge from %s", path)
	}
	wantAuth := `CornusSSH challenge="` + challenge + `"`
	if got := response.Header.Get("WWW-Authenticate"); got != wantAuth {
		return nil, fmt.Errorf("server returned invalid SSH key challenge header %q", got)
	}
	proof, err := sshkeyauth.Sign(signer, purpose, challenge, binding)
	if err != nil {
		return nil, err
	}
	attach(challenge, proof)
	return post()
}

// SSHEnroll enrolls signer's public key with a one-time enrollment code.
func (c *Client) SSHEnroll(ctx context.Context, signer ssh.Signer, code, name string) (SSHKeyInfo, error) {
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	request := &sshEnrollRequest{sshProofRequest: sshProofRequest{PublicKey: publicKey}, Code: code, Name: name}
	binding := sshkeyauth.EnrollmentBinding(publicKey, code, name)
	resp, err := c.sshProofRoundTrip(ctx, "/.cornus/v1/auth/enroll", sshkeyauth.PurposeEnroll, signer, binding, request, func(challenge string, proof sshkeyauth.Proof) {
		request.Challenge, request.Proof = challenge, &proof
	})
	if err != nil {
		return SSHKeyInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return SSHKeyInfo{}, apiError(resp)
	}
	var info SSHKeyInfo
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

// SSHKeyToken proves possession of signer and returns a short-lived bearer token.
func (c *Client) SSHKeyToken(ctx context.Context, signer ssh.Signer, scope, ttl string) (string, time.Time, error) {
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	request := &sshTokenRequest{sshProofRequest: sshProofRequest{PublicKey: publicKey}, Scope: scope, TTL: ttl}
	binding := sshkeyauth.SessionBinding(publicKey, scope, ttl)
	resp, err := c.sshProofRoundTrip(ctx, "/.cornus/v1/auth/token", sshkeyauth.PurposeSession, signer, binding, request, func(challenge string, proof sshkeyauth.Proof) {
		request.Challenge, request.Proof = challenge, &proof
	})
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, apiError(resp)
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, err
	}
	return result.Token, result.ExpiresAt, nil
}

// SSHKeys lists the server's authorized SSH keys. It requires a full bearer
// credential and therefore uses the client's normal Authorization header.
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKeyInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/auth/keys", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	var keys []SSHKeyInfo
	return keys, json.NewDecoder(resp.Body).Decode(&keys)
}

// DeleteSSHKey removes one runtime-enrolled key by SHA256 fingerprint.
func (c *Client) DeleteSSHKey(ctx context.Context, fingerprint string) error {
	path := "/.cornus/v1/auth/keys?fingerprint=" + url.QueryEscape(fingerprint)
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiError(resp)
	}
	return nil
}
