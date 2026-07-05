package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"cornus/pkg/authtoken"
)

const (
	peerKeyFile            = "peer.key"
	peerIssuer             = "cornus:peer"
	peerAudience           = "cornus:peer"
	peerTokenTTL           = 5 * time.Minute
	peerTokenRefreshBefore = time.Minute
)

// peerKeypair is this replica's inter-replica signing identity. Only PublicPEM
// is published through the hub store; PrivatePEM never leaves this replica's
// DataDir. A store reader therefore cannot mint peer credentials, while a store
// writer adds no new trust dependency because it can already rewrite routing.
type peerKeypair struct {
	PrivatePEM []byte
	PublicPEM  []byte
}

// peerTokenCache serializes ES256 minting and reuses a token until shortly before
// expiry. Forwarding can be high-frequency; signing every hop adds no security.
type peerTokenCache struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

// loadPeerKeyIfNeeded preserves the zero-side-effect default: a replica creates
// a key only when operator auth is enabled and a distributed hub store is
// configured. Single-replica and auth-off servers never create peer.key.
func loadPeerKeyIfNeeded(dataDir string, authEnabled, distributed bool) (*peerKeypair, error) {
	if !authEnabled || !distributed {
		return nil, nil
	}
	return loadPeerKey(dataDir)
}

func loadPeerKey(dataDir string) (*peerKeypair, error) {
	if dataDir == "" {
		return nil, errors.New("peer key: DataDir is empty")
	}
	path := filepath.Join(dataDir, peerKeyFile)
	privatePEM, err := readPeerPrivateKey(path)
	if err == nil {
		return peerKeypairFromPrivatePEM(privatePEM)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create peer key directory %s: %w", dataDir, err)
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate peer key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("marshal peer key: %w", err)
	}
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	tmp, err := os.CreateTemp(dataDir, ".peer.key-*")
	if err != nil {
		return nil, fmt.Errorf("create peer key temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("chmod peer key temporary file: %w", err)
	}
	if _, err := tmp.Write(privatePEM); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write peer key temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close peer key temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, fmt.Errorf("install peer key %s: %w", path, err)
	}
	// Rename can replace an existing inode, and a mode passed at creation does
	// not tighten one. Enforce the final private-key contract explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("chmod peer key %s: %w", path, err)
	}
	return peerKeypairFromPrivatePEM(privatePEM)
}

func readPeerPrivateKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("peer key %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("peer key %s has insecure permissions %04o; want 0600 or stricter", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read peer key %s: %w", path, err)
	}
	return raw, nil
}

func peerKeypairFromPrivatePEM(privatePEM []byte) (*peerKeypair, error) {
	block, rest := pem.Decode(privatePEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return nil, errors.New("peer key must contain exactly one PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse peer key: %w", err)
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || private.Curve != elliptic.P256() {
		return nil, fmt.Errorf("peer key must be ECDSA P-256, got %T", parsed)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal peer public key: %w", err)
	}
	return &peerKeypair{
		PrivatePEM: append([]byte(nil), privatePEM...),
		PublicPEM:  pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}),
	}, nil
}

func (s *Server) peerForwardToken() (string, error) {
	if s.peerKey == nil || s.replicaID == "" {
		return "", errors.New("peer credential is not configured")
	}
	s.peerToken.mu.Lock()
	defer s.peerToken.mu.Unlock()
	now := time.Now()
	if s.peerToken.token != "" && now.Before(s.peerToken.expires.Add(-peerTokenRefreshBefore)) {
		return s.peerToken.token, nil
	}
	token, err := authtoken.Issue(authtoken.IssueOptions{
		Subject:       s.replicaID,
		Scope:         authtoken.ScopePeer,
		Issuer:        peerIssuer,
		Audience:      peerAudience,
		TTL:           peerTokenTTL,
		Now:           now,
		KeyID:         s.replicaID,
		PrivateKeyPEM: s.peerKey.PrivatePEM,
	})
	if err != nil {
		return "", fmt.Errorf("mint peer credential: %w", err)
	}
	s.peerToken.token = token
	s.peerToken.expires = now.Add(peerTokenTTL)
	return token, nil
}
