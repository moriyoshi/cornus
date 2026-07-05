package kubernetes

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
)

func TestApplyCreatesIngressExplicitHost(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"app.example.com"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ing, err := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "app.example.com" {
		t.Fatalf("rules = %+v", ing.Spec.Rules)
	}
	p := ing.Spec.Rules[0].HTTP.Paths[0]
	if p.Path != "/" || *p.PathType != networkingv1.PathTypePrefix {
		t.Fatalf("path = %q type = %v", p.Path, *p.PathType)
	}
	if p.Backend.Service.Name != "web" || p.Backend.Service.Port.Number != 80 {
		t.Fatalf("backend = %+v", p.Backend.Service)
	}
	// Owner reference wires GC to the Deployment (the fake runs no GC controller
	// and leaves UIDs empty, so assert the wiring, not the cascade).
	if len(ing.OwnerReferences) != 1 {
		t.Fatalf("owner refs = %+v", ing.OwnerReferences)
	}
	or := ing.OwnerReferences[0]
	if or.Kind != "Deployment" || or.Name != "web" || or.Controller == nil || !*or.Controller {
		t.Fatalf("owner ref = %+v", or)
	}
}

func TestApplyIngressMultipleHosts(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{
			Hosts: []string{"app.example.com", "www.example.com"},
			TLS:   &api.IngressTLS{SecretName: "web-cert"},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	// One rule per host, each pointing at the same backend Service.
	if len(ing.Spec.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %+v", ing.Spec.Rules)
	}
	if ing.Spec.Rules[0].Host != "app.example.com" || ing.Spec.Rules[1].Host != "www.example.com" {
		t.Fatalf("rule hosts = %q, %q", ing.Spec.Rules[0].Host, ing.Spec.Rules[1].Host)
	}
	for i, r := range ing.Spec.Rules {
		if r.HTTP.Paths[0].Backend.Service.Name != "web" {
			t.Fatalf("rule %d backend = %+v", i, r.HTTP.Paths[0].Backend.Service)
		}
	}
	// A single TLS entry covers every host.
	if len(ing.Spec.TLS) != 1 || len(ing.Spec.TLS[0].Hosts) != 2 {
		t.Fatalf("tls = %+v (one entry covering both hosts)", ing.Spec.TLS)
	}
}

func TestApplyIngressApexHost(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "example.com")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// "@" maps to the apex (the base domain itself); "www" is a normal subdomain.
	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"@", "www.example.com"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if ing.Spec.Rules[0].Host != "example.com" {
		t.Fatalf("apex host = %q (\"@\" must map to the base domain)", ing.Spec.Rules[0].Host)
	}
	if ing.Spec.Rules[1].Host != "www.example.com" {
		t.Fatalf("second host = %q", ing.Spec.Rules[1].Host)
	}
}

func TestApplyIngressApexWithClientDomain(t *testing.T) {
	// No server domain; the client supplies the domain and "@" resolves against it.
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"@"}, Domain: "shop.test"},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if ing.Spec.Rules[0].Host != "shop.test" {
		t.Fatalf("apex host = %q (should resolve to the client domain)", ing.Spec.Rules[0].Host)
	}
}

func TestApplyIngressApexWithoutDomainErrors(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default") // no CORNUS_INGRESS_DOMAIN
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"@"}},
	}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatalf(`expected an error for "@" with no base domain`)
	}
}

func TestApplyIngressAutoDerivesHost(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "app-pr-123",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Enabled: true}, // no explicit host -> derived
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, err := cs.NetworkingV1().Ingresses("default").Get(ctx, "app-pr-123", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if got := ing.Spec.Rules[0].Host; got != "app-pr-123.preview.example.com" {
		t.Fatalf("derived host = %q", got)
	}
}

func TestApplyIngressSubdomainDerivation(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// The compose translator sets "<service>.<project>" as the subdomain so projects
	// do not collide; the backend prefixes it to the base domain.
	spec := api.DeploySpec{
		Name:    "pr-123-web", // the flattened resource name (unused for the host here)
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Enabled: true, Subdomain: "web.pr-123"},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "pr-123-web", metav1.GetOptions{})
	if got := ing.Spec.Rules[0].Host; got != "web.pr-123.preview.example.com" {
		t.Fatalf("host = %q (subdomain must prefix the base domain)", got)
	}
}

func TestApplyIngressSubdomainSanitized(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "example.com")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// Raw compose names (underscores, mixed case) are sanitized per label.
	spec := api.DeploySpec{
		Name:    "svc",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Enabled: true, Subdomain: "Web_1.My_Proj"},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "svc", metav1.GetOptions{})
	if got := ing.Spec.Rules[0].Host; got != "web-1.my-proj.example.com" {
		t.Fatalf("sanitized host = %q", got)
	}
}

func TestApplyIngressExplicitHostOverridesDomain(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "app-pr-123",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"custom.example.org"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "app-pr-123", metav1.GetOptions{})
	if got := ing.Spec.Rules[0].Host; got != "custom.example.org" {
		t.Fatalf("host = %q, explicit host should override the base domain", got)
	}
}

func TestApplyIngressClientDomainOverridesServerDefault(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "app-pr-123",
		Image: "localhost:5000/web:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}},
		// Client overrides the base domain; the server default must NOT win.
		Ingress: &api.IngressSpec{Enabled: true, Domain: "staging.example.org"},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "app-pr-123", metav1.GetOptions{})
	if got := ing.Spec.Rules[0].Host; got != "app-pr-123.staging.example.org" {
		t.Fatalf("host = %q, client domain override should win over the server default", got)
	}
}

func TestApplyIngressEnforceDomainAllowsSubdomain(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	t.Setenv("CORNUS_INGRESS_ENFORCE_DOMAIN", "true")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// An explicit host WITHIN the pinned domain is allowed.
	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"custom.preview.example.com"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply (in-domain host): %v", err)
	}
	if _, err := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
}

func TestApplyIngressEnforceDomainRejectsOutside(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	t.Setenv("CORNUS_INGRESS_ENFORCE_DOMAIN", "true")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// An explicit host OUTSIDE the pinned domain is rejected by policy.
	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"evil.attacker.test"}},
	}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatalf("expected the domain-enforcement policy to reject an out-of-domain host")
	}
	// A client domain override outside the pinned domain is likewise rejected.
	spec.Ingress = &api.IngressSpec{Enabled: true, Domain: "attacker.test"}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatalf("expected the policy to reject an out-of-domain client domain override")
	}
}

func TestApplyIngressDefaultClassAndIssuer(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_DOMAIN", "preview.example.com")
	t.Setenv("CORNUS_INGRESS_CLASS", "nginx")
	t.Setenv("CORNUS_INGRESS_TLS_ISSUER", "letsencrypt-prod")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}},
		// TLS block present but empty: issuer resolves from the server default.
		Ingress: &api.IngressSpec{Enabled: true, TLS: &api.IngressTLS{}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Fatalf("class = %v", ing.Spec.IngressClassName)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "web-tls" {
		t.Fatalf("tls = %+v", ing.Spec.TLS)
	}
	if ing.Spec.TLS[0].Hosts[0] != "web.preview.example.com" {
		t.Fatalf("tls host = %v", ing.Spec.TLS[0].Hosts)
	}
	if got := ing.Annotations["cert-manager.io/cluster-issuer"]; got != "letsencrypt-prod" {
		t.Fatalf("issuer annotation = %q", got)
	}
}

func TestApplyIngressExplicitTLSSecret(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{
			Hosts: []string{"app.example.com"},
			TLS:   &api.IngressTLS{SecretName: "my-cert"},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if ing.Spec.TLS[0].SecretName != "my-cert" {
		t.Fatalf("secret = %q", ing.Spec.TLS[0].SecretName)
	}
	// No issuer configured and secret supplied directly: no cert-manager annotation.
	if _, ok := ing.Annotations["cert-manager.io/cluster-issuer"]; ok {
		t.Fatalf("unexpected cluster-issuer annotation: %v", ing.Annotations)
	}
}

func TestApplyIngressPortSelection(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}, {Host: 9000, Container: 9090}},
		Ingress: &api.IngressSpec{
			Hosts: []string{"app.example.com"},
			Port:  9090,
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if got := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number; got != 9090 {
		t.Fatalf("target port = %d", got)
	}
}

func TestApplyIngressIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"app.example.com"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Change the routed path and re-apply: it must update in place, not error.
	spec.Ingress.Path = "/api"
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	ing, _ := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{})
	if got := ing.Spec.Rules[0].HTTP.Paths[0].Path; got != "/api" {
		t.Fatalf("path after re-apply = %q", got)
	}
}

func TestApplyNoIngressByDefault(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := cs.NetworkingV1().Ingresses("default").Get(ctx, "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no ingress, got err = %v", err)
	}
}

func TestApplyIngressWithoutPortsErrors(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ingress: &api.IngressSpec{Hosts: []string{"app.example.com"}},
	}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatalf("expected error for ingress without published ports")
	}
}

func TestApplyIngressNoHostNoDomainErrors(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default") // no CORNUS_INGRESS_DOMAIN
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Enabled: true},
	}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatalf("expected error when neither host nor base domain is set")
	}
}

func TestApplyIngressTLSWithoutSecretOrIssuerErrors(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default") // no CORNUS_INGRESS_TLS_ISSUER
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/web:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
		Ingress: &api.IngressSpec{Hosts: []string{"app.example.com"}, TLS: &api.IngressTLS{}},
	}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatalf("expected error for tls with neither secret nor issuer")
	}
}
