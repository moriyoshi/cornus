package ingressemu

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CA is a certificate authority the emulated ingress uses to mint per-host leaf
// certificates it serves over TLS. It is either generated and persisted (so a user
// trusts one CA for every emulated host), loaded from user-supplied PEM files, or
// mkcert's locally-trusted root (see LoadMkcertCA). The signing key is a crypto.Signer
// so an RSA CA (mkcert's default) works as well as our generated ECDSA one.
type CA struct {
	cert     *x509.Certificate
	key      crypto.Signer
	certPEM  []byte
	certPath string // path to the CA cert PEM, for the user to trust (may be empty)
}

// CertPath returns the filesystem path of the CA certificate PEM, so the caller can
// tell the user which CA to trust. It is empty for an in-memory CA.
func (ca *CA) CertPath() string { return ca.certPath }

// CertPEM returns the CA certificate in PEM form.
func (ca *CA) CertPEM() []byte { return ca.certPEM }

// DefaultCAPaths returns the default persisted CA cert/key paths under the user's
// XDG data dir (or ~/.local/share), creating the parent directory.
func DefaultCAPaths() (certPath, keyPath string, err error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", herr
		}
		dir = filepath.Join(home, ".local", "share")
	}
	dir = filepath.Join(dir, "cornus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "ingress-ca.pem"), filepath.Join(dir, "ingress-ca.key"), nil
}

// LoadOrCreateCA loads the CA from certPath/keyPath when both parse, else generates a
// fresh CA and persists it (cert 0644, key 0600). When certPath is empty it resolves
// the default persisted location.
func LoadOrCreateCA(certPath, keyPath string) (*CA, error) {
	if certPath == "" || keyPath == "" {
		p, k, err := DefaultCAPaths()
		if err != nil {
			return nil, err
		}
		certPath, keyPath = p, k
	}
	if ca, err := LoadCA(certPath, keyPath); err == nil {
		return ca, nil
	}
	ca, err := generateCA()
	if err != nil {
		return nil, err
	}
	// generateCA always produces an ECDSA key, so this assertion holds; it is only the
	// generated CA that we persist (a loaded mkcert/RSA CA is never re-marshaled here).
	ecKey, ok := ca.key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ingressemu: generated CA key is not ECDSA")
	}
	keyDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Truncating writes (os.WriteFile) sidestep the shell's NO_CLOBBER; the key is
	// mode 0600 because it can sign certs the browser will trust.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, ca.certPEM, 0o644); err != nil {
		return nil, err
	}
	ca.certPath = certPath
	return ca, nil
}

// LoadCA loads an existing CA from PEM cert/key files.
func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("ingressemu: %s is not a PEM certificate", certPath)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("ingressemu: %s is not a PEM key", keyPath)
	}
	key, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ingressemu: parse %s: %w", keyPath, err)
	}
	return &CA{cert: cert, key: key, certPEM: certPEM, certPath: certPath}, nil
}

// parsePrivateKey parses a DER private key of any common encoding (PKCS#8, SEC1 EC,
// or PKCS#1 RSA) into a crypto.Signer, so a loaded CA key may be RSA (mkcert's
// default) or ECDSA. The PEM block type is not trusted — each parser is tried.
func parsePrivateKey(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, fmt.Errorf("unsupported PKCS#8 key type %T", k)
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("unrecognized private key format (want PKCS#8, SEC1, or PKCS#1)")
}

// generateCA builds a fresh self-signed ECDSA CA valid for ten years.
func generateCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cornus ingress emulation CA", Organization: []string{"cornus"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &CA{cert: cert, key: key, certPEM: certPEM}, nil
}

// Leaf mints a leaf certificate for hosts (as SANs), signed by the CA. The returned
// tls.Certificate chains the leaf and the CA so a client trusting only the CA
// verifies it.
func (ca *CA) Leaf(hosts []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	cn := ""
	if len(hosts) > 0 {
		cn = hosts[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: [][]byte{der, ca.cert.Raw},
		PrivateKey:  key,
	}, nil
}

// randSerial returns a random 128-bit positive certificate serial number.
func randSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
