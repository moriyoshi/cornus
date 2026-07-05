package clientconn

import (
	"context"
	"testing"

	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

func TestApplyIngressConfigCarriesCertificateRules(t *testing.T) {
	cn := &Conn{Config: Config{Conduit: &clientconfig.Conduit{
		Mode: clientconduit.ModeSocks5,
		Ingress: &clientconfig.Ingress{
			Mode: "emulate",
			Certificates: []clientconfig.IngressCertificate{{
				Pattern: "*.example.com", Certificate: "/tmp/server.pem", Key: "/tmp/server.key",
			}},
		},
	}}}
	cfg := cn.ConduitConfigFor()
	cn.ApplyIngressConfig(context.Background(), &cfg)
	if cfg.Ingress == nil || len(cfg.Ingress.Certificates) != 1 {
		t.Fatalf("runtime ingress certificates = %#v", cfg.Ingress)
	}
	got := cfg.Ingress.Certificates[0]
	if got.Pattern != "*.example.com" || got.CertFile != "/tmp/server.pem" || got.KeyFile != "/tmp/server.key" {
		t.Fatalf("runtime certificate = %#v", got)
	}
}
