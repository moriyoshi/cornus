// Package sshkeyauth implements the cryptographic core of Cornus SSH-key
// authentication. It deliberately contains no HTTP or filesystem policy.
package sshkeyauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	PurposeEnroll  = "enroll"
	PurposeSession = "session"

	proofMagic          = "cornus-ssh-key-proof-v2"
	challengeDomain     = "cornus-ssh-key-challenge-v2\x00"
	challengeNonceSize  = 16
	challengeExpirySize = 8
	challengeMACSize    = sha256.Size
	challengeSize       = challengeNonceSize + challengeExpirySize + challengeMACSize
)

// Proof is an SSH signature plus the separately declared signature algorithm.
// The declaration is checked against Signature.Format before cryptographic
// verification so an attacker cannot steer verification through another
// algorithm parser.
type Proof struct {
	Algorithm string        `json:"algorithm"`
	Signature ssh.Signature `json:"signature"`
}

type proofPayload struct {
	Magic     string
	Purpose   string
	Challenge string
	PublicKey []byte
	Binding   []byte
}

// Sign creates a proof bound to purpose, challenge, and the signer's public
// key, plus a digest of the request being authorized. RSA always uses SHA-2;
// legacy ssh-rsa (RSA/SHA-1) is never emitted.
func Sign(signer ssh.Signer, purpose, challenge string, binding []byte) (Proof, error) {
	if err := validatePurpose(purpose); err != nil {
		return Proof{}, err
	}
	message := marshalPayload(signer.PublicKey(), purpose, challenge, binding)
	var (
		signature *ssh.Signature
		err       error
	)
	if signer.PublicKey().Type() == ssh.KeyAlgoRSA {
		algorithmSigner, ok := signer.(ssh.AlgorithmSigner)
		if !ok {
			return Proof{}, errors.New("ssh key auth: RSA signer does not support SHA-2 signatures")
		}
		signature, err = algorithmSigner.SignWithAlgorithm(rand.Reader, message, ssh.KeyAlgoRSASHA512)
	} else {
		signature, err = signer.Sign(rand.Reader, message)
	}
	if err != nil {
		return Proof{}, fmt.Errorf("ssh key auth: sign proof: %w", err)
	}
	proof := Proof{Algorithm: signature.Format, Signature: *signature}
	if err := validateAlgorithm(signer.PublicKey(), proof); err != nil {
		return Proof{}, err
	}
	return proof, nil
}

// Verify checks the proof's purpose binding, declared algorithm, key-specific
// allowlist, and signature, in that order.
func Verify(publicKey ssh.PublicKey, purpose, challenge string, binding []byte, proof Proof) error {
	if err := validatePurpose(purpose); err != nil {
		return err
	}
	if err := validateAlgorithm(publicKey, proof); err != nil {
		return err
	}
	message := marshalPayload(publicKey, purpose, challenge, binding)
	if err := publicKey.Verify(message, &proof.Signature); err != nil {
		return fmt.Errorf("ssh key auth: verify proof: %w", err)
	}
	return nil
}

func marshalPayload(publicKey ssh.PublicKey, purpose, challenge string, binding []byte) []byte {
	return ssh.Marshal(proofPayload{
		Magic:     proofMagic,
		Purpose:   purpose,
		Challenge: challenge,
		PublicKey: publicKey.Marshal(),
		Binding:   bindingHash(binding),
	})
}

// EnrollmentBinding returns the canonical SSH-wire encoding of every mutable
// enrollment field. Its digest is covered by both the challenge MAC and the SSH
// signature, so a proof cannot be replayed with another key, code, or name.
func EnrollmentBinding(publicKey, code, name string) []byte {
	return ssh.Marshal(struct {
		PublicKey string
		Code      string
		Name      string
	}{publicKey, code, name})
}

// SessionBinding returns the canonical SSH-wire encoding of every mutable token
// request field. Scope and TTL are strings deliberately: the server validates
// their semantics separately while the proof covers the exact request sent.
func SessionBinding(publicKey, scope, ttl string) []byte {
	return ssh.Marshal(struct {
		PublicKey string
		Scope     string
		TTL       string
	}{publicKey, scope, ttl})
}

func bindingHash(binding []byte) []byte {
	digest := sha256.Sum256(binding)
	return digest[:]
}

func validatePurpose(purpose string) error {
	switch purpose {
	case PurposeEnroll, PurposeSession:
		return nil
	default:
		return fmt.Errorf("ssh key auth: unsupported proof purpose %q", purpose)
	}
}

func validateAlgorithm(publicKey ssh.PublicKey, proof Proof) error {
	if proof.Algorithm == "" || proof.Algorithm != proof.Signature.Format {
		return fmt.Errorf("ssh key auth: declared algorithm %q does not match signature format %q", proof.Algorithm, proof.Signature.Format)
	}
	// ssh-rsa is RSA/SHA-1, not the name of the RSA public-key family here.
	// ssh-dss is DSA/SHA-1. Refuse both before PublicKey.Verify.
	if proof.Algorithm == ssh.KeyAlgoRSA || proof.Algorithm == ssh.KeyAlgoDSA {
		return fmt.Errorf("ssh key auth: legacy signature algorithm %q is forbidden", proof.Algorithm)
	}
	switch publicKey.Type() {
	case ssh.KeyAlgoRSA:
		if proof.Algorithm == ssh.KeyAlgoRSASHA256 || proof.Algorithm == ssh.KeyAlgoRSASHA512 {
			return nil
		}
	case ssh.KeyAlgoED25519:
		if proof.Algorithm == ssh.KeyAlgoED25519 {
			return nil
		}
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		if proof.Algorithm == publicKey.Type() {
			return nil
		}
	}
	return fmt.Errorf("ssh key auth: signature algorithm %q is not allowed for key type %q", proof.Algorithm, publicKey.Type())
}

// IssueChallenge returns a stateless, URL-safe challenge whose expiry and MAC
// are checked only by the server. The MAC is bound to the proof purpose and a
// digest of the request being authorized. Client clock skew is therefore irrelevant.
func IssueChallenge(secret []byte, purpose string, binding []byte, now time.Time, ttl time.Duration) (string, error) {
	return issueChallenge(secret, purpose, binding, now, ttl, rand.Reader)
}

func issueChallenge(secret []byte, purpose string, binding []byte, now time.Time, ttl time.Duration, random io.Reader) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("ssh key auth: empty challenge secret")
	}
	if err := validatePurpose(purpose); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", errors.New("ssh key auth: challenge TTL must be positive")
	}
	raw := make([]byte, challengeSize)
	if _, err := io.ReadFull(random, raw[:challengeNonceSize]); err != nil {
		return "", fmt.Errorf("ssh key auth: generate challenge nonce: %w", err)
	}
	binary.BigEndian.PutUint64(raw[challengeNonceSize:challengeNonceSize+challengeExpirySize], uint64(now.Add(ttl).Unix()))
	mac := challengeMAC(secret, purpose, binding, raw[:challengeNonceSize+challengeExpirySize])
	copy(raw[challengeNonceSize+challengeExpirySize:], mac)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// VerifyChallenge checks shape, purpose/request binding, MAC, and server-clock expiry.
func VerifyChallenge(secret []byte, challenge, purpose string, binding []byte, now time.Time) error {
	if len(secret) == 0 {
		return errors.New("ssh key auth: empty challenge secret")
	}
	if err := validatePurpose(purpose); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(raw) != challengeSize {
		return errors.New("ssh key auth: malformed challenge")
	}
	message := raw[:challengeNonceSize+challengeExpirySize]
	want := challengeMAC(secret, purpose, binding, message)
	if !hmac.Equal(raw[challengeNonceSize+challengeExpirySize:], want) {
		return errors.New("ssh key auth: invalid challenge MAC")
	}
	expires := int64(binary.BigEndian.Uint64(raw[challengeNonceSize : challengeNonceSize+challengeExpirySize]))
	if now.Unix() > expires {
		return errors.New("ssh key auth: challenge expired")
	}
	return nil
}

func challengeMAC(secret []byte, purpose string, binding, message []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(challengeDomain))
	mac.Write([]byte(purpose))
	mac.Write([]byte{0})
	mac.Write(bindingHash(binding))
	mac.Write(message)
	return mac.Sum(nil)
}
