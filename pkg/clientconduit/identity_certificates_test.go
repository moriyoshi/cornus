package clientconduit

import (
	"testing"

	"cornus/pkg/ingressemu"
)

func TestIdentityIncludesIngressCertificates(t *testing.T) {
	a := Config{Mode: ModeSocks5, Ingress: &IngressConfig{Mode: IngressEmulate, Certificates: []ingressemu.CertificateSource{{Pattern: "*.example.com", CertFile: "one.pem", KeyFile: "one.key"}}}}
	b := Config{Mode: ModeSocks5, Ingress: &IngressConfig{Mode: IngressEmulate, Certificates: []ingressemu.CertificateSource{{Pattern: "*.example.com", CertFile: "two.pem", KeyFile: "one.key"}}}}
	if a.Identity("s") == b.Identity("s") {
		t.Fatal("certificate path changes must change conduit identity")
	}
}
