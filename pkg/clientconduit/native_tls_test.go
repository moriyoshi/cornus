package clientconduit

import (
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/ingressemu"
)

func nativeTLSFixture() ingressemu.CertificateSource {
	return ingressemu.CertificateSource{
		CertFile: filepath.Join("..", "..", "e2e", "scenarios", "certs", "ingress-byo.crt"),
		KeyFile:  filepath.Join("..", "..", "e2e", "scenarios", "certs", "ingress-byo.key"),
	}
}

func TestMaterializeNativeTLS(t *testing.T) {
	spec := &api.DeploySpec{
		Name: "web",
		Ingress: &api.IngressSpec{
			Enabled: true,
			Hosts:   []string{"APP.NATIVE-CERT.EXAMPLE.TEST."},
			TLS:     &api.IngressTLS{},
		},
	}
	cfg := Config{Ingress: &IngressConfig{Mode: IngressNative, Certificates: []ingressemu.CertificateSource{nativeTLSFixture()}}}
	if err := MaterializeNativeTLS(spec, cfg); err != nil {
		t.Fatal(err)
	}
	if len(spec.Ingress.TLS.ManagedCertificates) != 1 {
		t.Fatalf("managed certificates = %#v", spec.Ingress.TLS.ManagedCertificates)
	}
	managed := spec.Ingress.TLS.ManagedCertificates[0]
	if len(managed.Hosts) != 1 || managed.Hosts[0] != "app.native-cert.example.test" {
		t.Fatalf("managed hosts = %v", managed.Hosts)
	}
	if managed.SecretName == "" || len(managed.CertificatePEM) == 0 || len(managed.PrivateKeyPEM) == 0 {
		t.Fatalf("incomplete managed certificate = %#v", managed)
	}
}

func TestMaterializeNativeTLSRequiresConcreteHosts(t *testing.T) {
	cfg := Config{Ingress: &IngressConfig{Mode: IngressNative, Certificates: []ingressemu.CertificateSource{nativeTLSFixture()}}}
	for _, hosts := range [][]string{nil, {"@"}} {
		spec := &api.DeploySpec{Name: "web", Ingress: &api.IngressSpec{Enabled: true, Hosts: hosts, TLS: &api.IngressTLS{}}}
		err := MaterializeNativeTLS(spec, cfg)
		if err == nil || !strings.Contains(err.Error(), "explicit ingress hosts") && !strings.Contains(err.Error(), "concrete apex hostname") {
			t.Fatalf("hosts %v: error = %v", hosts, err)
		}
	}
}

func TestMaterializeNativeTLSNoopOutsideNativeMode(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{Ingress: &IngressConfig{Mode: IngressEmulate, Certificates: []ingressemu.CertificateSource{nativeTLSFixture()}}},
		{Ingress: &IngressConfig{Mode: IngressNative}},
	} {
		spec := &api.DeploySpec{Name: "web", Ingress: &api.IngressSpec{Enabled: true, Hosts: []string{"app.native-cert.example.test"}, TLS: &api.IngressTLS{}}}
		if err := MaterializeNativeTLS(spec, cfg); err != nil {
			t.Fatal(err)
		}
		if len(spec.Ingress.TLS.ManagedCertificates) != 0 {
			t.Fatalf("unexpected managed certificates for %#v", cfg)
		}
	}
}

// TestMarkClientEmulatedIngress: emulate mode flags the ingress client-emulated
// (so the server skips the cluster Ingress); every other mode leaves it unset (so a
// native/plain deploy still gets a real server Ingress).
func TestMarkClientEmulatedIngress(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"emulate -> flagged", Config{Ingress: &IngressConfig{Mode: IngressEmulate}}, true},
		{"native -> not flagged", Config{Ingress: &IngressConfig{Mode: IngressNative}}, false},
		{"off -> not flagged", Config{Ingress: &IngressConfig{Mode: IngressOff}}, false},
		{"no ingress cfg -> not flagged", Config{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &api.DeploySpec{Name: "web", Ingress: &api.IngressSpec{Enabled: true, Hosts: []string{"app.example.test"}}}
			MarkClientEmulatedIngress(spec, tc.cfg)
			if spec.Ingress.ClientEmulated != tc.want {
				t.Fatalf("ClientEmulated = %v, want %v", spec.Ingress.ClientEmulated, tc.want)
			}
		})
	}
	// Nil-safe: no ingress on the spec is a no-op, not a panic.
	MarkClientEmulatedIngress(&api.DeploySpec{Name: "web"}, Config{Ingress: &IngressConfig{Mode: IngressEmulate}})
}
