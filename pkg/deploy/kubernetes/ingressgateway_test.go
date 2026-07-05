package kubernetes

import (
	"context"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

func TestBackendImplementsIngressGateway(t *testing.T) {
	var _ deploy.IngressGateway = NewWithClient(fake.NewSimpleClientset(), "default")
}

func TestIngressHostsReadsLiveObject(t *testing.T) {
	cs := fake.NewSimpleClientset(&networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{Host: "App.Example.COM"},
				{Host: "alt.example.com"},
				{Host: "app.example.com"}, // duplicate after canonicalization
			},
		},
	})
	b := NewWithClient(cs, "default")

	hosts, err := b.IngressHosts(context.Background(), "web")
	if err != nil {
		t.Fatalf("IngressHosts: %v", err)
	}
	// Canonicalized and de-duplicated: a tunnel advertises what the controller
	// really routes, in the same form the routing table keys on.
	if len(hosts) != 2 || hosts[0] != "app.example.com" || hosts[1] != "alt.example.com" {
		t.Fatalf("hosts = %v, want [app.example.com alt.example.com]", hosts)
	}
}

// TestIngressHostsMissingIngressIsNotAnError proves a deployment with no Ingress
// reports "no hosts" rather than failing: the caller uses this to decide whether
// a tunnel has anything to front, and a 404 is a legitimate answer.
func TestIngressHostsMissingIngressIsNotAnError(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	hosts, err := b.IngressHosts(context.Background(), "web")
	if err != nil {
		t.Fatalf("IngressHosts on a deployment with no Ingress: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("hosts = %v, want none", hosts)
	}
}

// TestDialIngressWithoutControllerExplainsItself proves the no-controller case
// is a clear, actionable error rather than a nil connection — the server uses it
// to fall back to the emulated mux, and an operator needs to know which knob
// fixes it.
func TestDialIngressWithoutControllerExplainsItself(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "")
	b := NewWithClient(fake.NewSimpleClientset(), "default")

	conn, err := b.DialIngress(context.Background(), false)
	if err == nil {
		conn.Close()
		t.Fatal("DialIngress with no discoverable controller should error")
	}
	if !strings.Contains(err.Error(), "CORNUS_INGRESS_CONTROLLER") {
		t.Errorf("error %q should name the knob that configures a controller", err)
	}
}

// TestDialIngressOutOfClusterNeedsRestConfig proves a backend built over a fake
// clientset (no rest.Config) refuses rather than panicking — the same guard
// exec and attach already apply.
func TestDialIngressOutOfClusterNeedsRestConfig(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "ingress-nginx/ingress-nginx-controller")
	b := NewWithClient(fake.NewSimpleClientset(), "default")

	if inCluster() {
		t.Skip("running inside a cluster; the ClusterIP dial path applies instead")
	}
	conn, err := b.DialIngress(context.Background(), false)
	if err == nil {
		conn.Close()
		t.Fatal("DialIngress without a rest.Config should error")
	}
	if !strings.Contains(err.Error(), "rest.Config") {
		t.Errorf("error %q should explain that a real cluster connection is required", err)
	}
}

// TestIngressControllerOverrideParsed proves CORNUS_INGRESS_CONTROLLER selects
// the Service the gateway dials, so an operator can point at a controller the
// well-known-name discovery does not know.
func TestIngressControllerOverrideParsed(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "traefik/traefik:8000/8443")
	b := NewWithClient(fake.NewSimpleClientset(), "default")

	c := b.ingressController(context.Background())
	if c == nil {
		t.Fatal("the override should resolve a controller")
	}
	want := api.IngressController{Namespace: "traefik", Service: "traefik", HTTPPort: 8000, HTTPSPort: 8443}
	if *c != want {
		t.Errorf("controller = %+v, want %+v", *c, want)
	}
}
