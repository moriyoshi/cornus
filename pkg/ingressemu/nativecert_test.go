package ingressemu

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestMaterializeNativeCertificatesGroupsHostsAndUsesSelectorPrecedence(t *testing.T) {
	wildCert, wildKey := writeSelectorCertificate(t, "*.example.com")
	exactCert, exactKey := writeSelectorCertificate(t, "api.example.com", "admin.example.com")
	got, err := MaterializeNativeCertificates("Demo_Web", []string{
		"WWW.EXAMPLE.COM.", "api.example.com", "admin.example.com", "www.example.com",
	}, []CertificateSource{
		{CertFile: wildCert, KeyFile: wildKey},
		{CertFile: exactCert, KeyFile: exactKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d managed certificates", len(got))
	}
	if want := []string{"www.example.com"}; !equalStrings(got[0].Hosts, want) {
		t.Fatalf("wildcard hosts = %v", got[0].Hosts)
	}
	if want := []string{"api.example.com", "admin.example.com"}; !equalStrings(got[1].Hosts, want) {
		t.Fatalf("exact hosts = %v", got[1].Hosts)
	}
	if !strings.HasPrefix(got[0].SecretName, "demo-web-tls-") || got[0].SecretName == got[1].SecretName {
		t.Fatal("unexpected secret names")
	}
	if !bytes.Equal(got[0].CertificatePEM, mustReadFile(t, wildCert)) || !bytes.Equal(got[0].PrivateKeyPEM, mustReadFile(t, wildKey)) {
		t.Fatal("wrong key pair payload")
	}
}

func TestMaterializeNativeCertificatesRejectsUnmatchedHost(t *testing.T) {
	certFile, keyFile := writeSelectorCertificate(t, "api.example.com")
	_, err := MaterializeNativeCertificates("web", []string{"other.example.com"}, []CertificateSource{{CertFile: certFile, KeyFile: keyFile}})
	if err == nil || !strings.Contains(err.Error(), "no certificate matches host") {
		t.Fatalf("error = %v", err)
	}
}

func TestManagedCertificateSecretNameStableAcrossRotation(t *testing.T) {
	a := ManagedCertificateSecretName("Very_Long_Project_Name_With_Invalid_Characters_And_A_Service", []string{"API.EXAMPLE.COM.", "*.example.com"})
	b := ManagedCertificateSecretName("Very_Long_Project_Name_With_Invalid_Characters_And_A_Service", []string{"*.example.com", "api.example.com"})
	if a != b {
		t.Fatalf("names differ: %q != %q", a, b)
	}
	if len(a) > 63 || strings.ContainsAny(a, "_ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("not a DNS label: %q", a)
	}
	if a == ManagedCertificateSecretName("Very_Long_Project_Name_With_Invalid_Characters_And_A_Service", []string{"other.example.com"}) {
		t.Fatal("different patterns produced same name")
	}
}

func TestMaterializeNativeCertificatesNoSources(t *testing.T) {
	got, err := MaterializeNativeCertificates("web", []string{"app.example.com"}, nil)
	if err != nil || got != nil {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
