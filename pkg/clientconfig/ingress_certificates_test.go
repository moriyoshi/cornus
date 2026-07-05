package clientconfig

import "testing"

func TestIngressCertificateMergeReplacesAndClones(t *testing.T) {
	base := &Ingress{Certificates: []IngressCertificate{{Certificate: "base.pem", Key: "base.key"}}}
	override := &Ingress{Certificates: []IngressCertificate{{Pattern: "*.example.com", Certificate: "wild.pem", Key: "wild.key"}}}
	got := base.Merge(override)
	if len(got.Certificates) != 1 || got.Certificates[0].Certificate != "wild.pem" {
		t.Fatalf("merged certificates = %#v", got.Certificates)
	}
	got.Certificates[0].Certificate = "changed.pem"
	if override.Certificates[0].Certificate != "wild.pem" {
		t.Fatal("merge aliased the override certificate list")
	}
	if base.Certificates[0].Certificate != "base.pem" {
		t.Fatal("merge mutated the base certificate list")
	}
}
