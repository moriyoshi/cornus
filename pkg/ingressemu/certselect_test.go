package ingressemu

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func writeSelectorCertificate(t *testing.T, hosts ...string) (string, string) {
	t.Helper()
	ca, err := generateCA()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := ca.Leaf(hosts)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(pair.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestCertificateSelectorDerivesDNSNamesAndPrefersExact(t *testing.T) {
	wildCert, wildKey := writeSelectorCertificate(t, "*.example.com")
	exactCert, exactKey := writeSelectorCertificate(t, "api.example.com")
	selector, err := loadCertificateSelector([]CertificateSource{
		{CertFile: wildCert, KeyFile: wildKey},
		{CertFile: exactCert, KeyFile: exactKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := selector.certificate("API.EXAMPLE.COM."); got == nil || got.Leaf.DNSNames[0] != "api.example.com" {
		t.Fatalf("exact selection = %#v", got)
	}
	if got := selector.certificate("web.example.com"); got == nil || got.Leaf.DNSNames[0] != "*.example.com" {
		t.Fatalf("wildcard selection = %#v", got)
	}
	if got := selector.certificate("deep.web.example.com"); got != nil {
		t.Fatal("single-label wildcard matched multiple labels")
	}
}

func TestCertificateSelectorRejectsPatternOutsideSAN(t *testing.T) {
	certFile, keyFile := writeSelectorCertificate(t, "api.example.com")
	if _, err := loadCertificateSelector([]CertificateSource{{Pattern: "other.example.com", CertFile: certFile, KeyFile: keyFile}}); err == nil {
		t.Fatal("pattern outside certificate SAN was accepted")
	}
}
