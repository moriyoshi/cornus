package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func clearIngressEnv(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "")
	t.Setenv("CORNUS_INGRESS_CLASS", "")
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "")
}

func TestAdvertisedIngressDiscoversController(t *testing.T) {
	clearIngressEnv(t)
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx-controller", Namespace: "ingress-nginx"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "http", Port: 80}, {Name: "https", Port: 443},
		}},
	})
	b := NewWithClient(cs, "default")
	info, err := b.AdvertisedIngress(context.Background())
	if err != nil {
		t.Fatalf("AdvertisedIngress: %v", err)
	}
	if info == nil || info.Controller == nil {
		t.Fatalf("expected a discovered controller, got %+v", info)
	}
	c := info.Controller
	if c.Namespace != "ingress-nginx" || c.Service != "ingress-nginx-controller" || c.HTTPPort != 80 || c.HTTPSPort != 443 {
		t.Fatalf("controller = %+v", c)
	}
}

func TestAdvertisedIngressNoneWhenAbsent(t *testing.T) {
	clearIngressEnv(t)
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	info, err := b.AdvertisedIngress(context.Background())
	if err != nil {
		t.Fatalf("AdvertisedIngress: %v", err)
	}
	if info != nil {
		t.Fatalf("want nil (no controller, no domain/class), got %+v", info)
	}
}

func TestAdvertisedIngressReportsDomainAndClass(t *testing.T) {
	clearIngressEnv(t)
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	t.Setenv("CORNUS_INGRESS_CLASS", "nginx")
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	info, err := b.AdvertisedIngress(context.Background())
	if err != nil {
		t.Fatalf("AdvertisedIngress: %v", err)
	}
	if info == nil || info.Domain != "preview.example.com" || info.Class != "nginx" {
		t.Fatalf("info = %+v", info)
	}
	if info.Controller != nil {
		t.Fatalf("no controller Service exists; want nil controller, got %+v", info.Controller)
	}
}

func TestAdvertisedIngressControllerEnvOverride(t *testing.T) {
	clearIngressEnv(t)
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "custom-ns/my-ctrl:8080/8443")
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	info, err := b.AdvertisedIngress(context.Background())
	if err != nil {
		t.Fatalf("AdvertisedIngress: %v", err)
	}
	if info == nil || info.Controller == nil {
		t.Fatalf("expected controller from env override, got %+v", info)
	}
	c := info.Controller
	if c.Namespace != "custom-ns" || c.Service != "my-ctrl" || c.HTTPPort != 8080 || c.HTTPSPort != 8443 {
		t.Fatalf("controller = %+v", c)
	}
}

func TestParseIngressControllerHelper(t *testing.T) {
	cases := []struct {
		in                  string
		wantNil             bool
		ns, svc             string
		httpPort, httpsPort int
	}{
		{in: "ingress-nginx/ingress-nginx-controller", ns: "ingress-nginx", svc: "ingress-nginx-controller", httpPort: 80, httpsPort: 443},
		{in: "ns/svc:8080/8443", ns: "ns", svc: "svc", httpPort: 8080, httpsPort: 8443},
		{in: "ns/svc:8080", ns: "ns", svc: "svc", httpPort: 8080, httpsPort: 443},
		{in: "no-slash", wantNil: true},
		{in: "/svc", wantNil: true},
		{in: "ns/", wantNil: true},
	}
	for _, tc := range cases {
		got := parseIngressController(tc.in)
		if tc.wantNil {
			if got != nil {
				t.Errorf("parseIngressController(%q) = %+v, want nil", tc.in, got)
			}
			continue
		}
		if got == nil || got.Namespace != tc.ns || got.Service != tc.svc || got.HTTPPort != tc.httpPort || got.HTTPSPort != tc.httpsPort {
			t.Errorf("parseIngressController(%q) = %+v", tc.in, got)
		}
	}
}
