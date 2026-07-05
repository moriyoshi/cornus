package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSigned writes a self-signed cert/key pair (with the given CommonName)
// to certPath/keyPath and returns the parsed leaf for identity assertions.
func writeSelfSigned(t *testing.T, certPath, keyPath, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// TestCertReloaderPicksUpRotation is the cert-manager rotation guarantee: after the
// cert/key files are rewritten in place (mtime advanced), the next GetCertificate
// serves the NEW certificate without any restart or re-wiring.
func TestCertReloaderPicksUpRotation(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")

	first := writeSelfSigned(t, certPath, keyPath, "first")
	r := &certReloader{certFile: certPath, keyFile: keyPath}
	if err := r.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	_ = first
	got, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if lf := leafCN(t, got); lf != "first" {
		t.Fatalf("initial cert CN = %q, want first", lf)
	}

	// Rotate: rewrite the files with a new cert and advance the mtime so the
	// reloader observes the change (avoids coarse-mtime flakiness).
	writeSelfSigned(t, certPath, keyPath, "second")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(certPath, future, future); err != nil {
		t.Fatal(err)
	}

	got2, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate after rotation: %v", err)
	}
	if lf := leafCN(t, got2); lf != "second" {
		t.Fatalf("rotated cert CN = %q, want second (reload did not pick up the new file)", lf)
	}
}

// leafCN parses the served certificate's leaf DER and returns its CommonName.
func leafCN(t *testing.T, c *tls.Certificate) string {
	t.Helper()
	if len(c.Certificate) == 0 {
		t.Fatal("served certificate has no DER")
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatalf("parse served leaf: %v", err)
	}
	return leaf.Subject.CommonName
}

func TestCertReloaderBadPathFailsFast(t *testing.T) {
	r := &certReloader{certFile: "/nonexistent/tls.crt", keyFile: "/nonexistent/tls.key"}
	if err := r.load(); err == nil {
		t.Fatal("load should fail fast on a missing cert file")
	}
}

func TestTLSConfigMTLSReloadsCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	caPath := filepath.Join(dir, "ca.crt")
	writeSelfSigned(t, certPath, keyPath, "server")
	writeSelfSigned(t, caPath, filepath.Join(dir, "ca.key"), "ca") // reuse as a CA bundle

	cfg, err := tlsConfig(certPath, keyPath, caPath)
	if err != nil {
		t.Fatalf("tlsConfig: %v", err)
	}
	if cfg.GetCertificate == nil {
		t.Fatal("GetCertificate not wired")
	}
	if cfg.GetConfigForClient == nil {
		t.Fatal("GetConfigForClient (CA reload) not wired for mTLS")
	}
	// The per-connection config carries a populated client-CA pool.
	perConn, err := cfg.GetConfigForClient(nil)
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if perConn.ClientCAs == nil {
		t.Fatal("per-connection config missing ClientCAs")
	}
}
