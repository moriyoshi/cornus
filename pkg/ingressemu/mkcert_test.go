package ingressemu

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFakeMkcertCA writes a mkcert-shaped root CA (an RSA key in PKCS#8, like
// mkcert's own default) as rootCA.pem + rootCA-key.pem under dir.
func writeFakeMkcertCA(t *testing.T, dir string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mkcert test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "rootCA.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rootCA-key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMkcertCARSAAndLeaf(t *testing.T) {
	root := t.TempDir()
	writeFakeMkcertCA(t, root)
	t.Setenv("CAROOT", root)

	ca, err := LoadMkcertCA()
	if err != nil {
		t.Fatalf("LoadMkcertCA: %v", err)
	}
	// An ECDSA leaf minted by the RSA CA must chain to it and carry the host SAN.
	leaf, err := ca.Leaf([]string{"app.example.com"})
	if err != nil {
		t.Fatalf("Leaf: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("append CA")
	}
	lc, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lc.Verify(x509.VerifyOptions{DNSName: "app.example.com", Roots: pool}); err != nil {
		t.Fatalf("leaf does not verify against the mkcert CA: %v", err)
	}
}

func TestLoadMkcertCAAbsent(t *testing.T) {
	t.Setenv("CAROOT", t.TempDir()) // exists but has no rootCA.pem
	if _, err := LoadMkcertCA(); err == nil {
		t.Fatal("LoadMkcertCA should error when rootCA.pem is absent")
	}
}

func TestMkcertCAROOTEnvWins(t *testing.T) {
	root := t.TempDir()
	writeFakeMkcertCA(t, root)
	t.Setenv("CAROOT", root)
	if got := MkcertCAROOT(); got != root {
		t.Fatalf("MkcertCAROOT = %q, want %q", got, root)
	}
}
