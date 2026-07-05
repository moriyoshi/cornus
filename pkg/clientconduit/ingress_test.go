package clientconduit

import (
	"context"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/ingressemu"
	"cornus/pkg/ingressnative"
	"cornus/pkg/socks5"
)

// newIngressConduit builds a socks5Conduit over a real router and the package's
// fakeDialer, for exercising AddIngress registration without a live proxy.
func newIngressConduit(t *testing.T, ic *IngressConfig) *socks5Conduit {
	t.Helper()
	r, err := socks5.NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	return &socks5Conduit{router: r, d: fakeDialer{}, ingress: ic}
}

func kindOf(t *testing.T, r *socks5.Router, host string, port int) socks5.Kind {
	t.Helper()
	res, err := r.Resolve(host, port)
	if err != nil {
		t.Fatalf("Resolve(%s,%d): %v", host, port, err)
	}
	return res.Kind
}

func TestAddIngressEmulateRegistersAndWithdraws(t *testing.T) {
	e := newIngressConduit(t, &IngressConfig{Mode: "emulate"})
	ctx, cancel := context.WithCancel(context.Background())

	// No TLS in the spec, so only :80 is published (no CA is needed).
	hosts, err := e.AddIngress(ctx, "web", &api.IngressSpec{Hosts: []string{"app.example.com"}}, []api.PortMapping{{Container: 8080}})
	if err != nil {
		t.Fatalf("AddIngress: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "app.example.com" {
		t.Fatalf("hosts = %v, want [app.example.com]", hosts)
	}
	if k := kindOf(t, e.router, "app.example.com", 80); k != socks5.KindLocal {
		t.Fatalf(":80 Kind = %v, want KindLocal", k)
	}
	// :443 is not published without a TLS spec.
	if k := kindOf(t, e.router, "app.example.com", 443); k != socks5.KindDirect {
		t.Fatalf(":443 Kind = %v, want KindDirect (no TLS)", k)
	}

	cancel()
	// Withdrawal is asynchronous; poll until the name is gone.
	waitDirect(t, e.router, "app.example.com", 80)
}

func TestAddIngressNativeRegistersBothPorts(t *testing.T) {
	e := newIngressConduit(t, &IngressConfig{
		Mode:       "native",
		Controller: &ingressnative.Controller{Namespace: "ingress-nginx", Service: "ingress-nginx-controller"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts, err := e.AddIngress(ctx, "web", &api.IngressSpec{Hosts: []string{"app.example.com"}}, []api.PortMapping{{Container: 8080}})
	if err != nil {
		t.Fatalf("AddIngress: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("hosts = %v", hosts)
	}
	for _, port := range []int{80, 443} {
		if k := kindOf(t, e.router, "app.example.com", port); k != socks5.KindLocal {
			t.Fatalf("native :%d Kind = %v, want KindLocal", port, k)
		}
	}
	cancel()
	waitDirect(t, e.router, "app.example.com", 80)
	waitDirect(t, e.router, "app.example.com", 443)
}

func TestAddIngressEmulateTLSRegistersBothPorts(t *testing.T) {
	// An explicit CA (created under a temp dir) avoids touching the real XDG path and
	// mkcert; a TLS spec publishes :80 and :443.
	dir := t.TempDir()
	caFile := dir + "/ca.pem"
	keyFile := dir + "/ca.key"
	if _, err := ingressemu.LoadOrCreateCA(caFile, keyFile); err != nil {
		t.Fatalf("create CA: %v", err)
	}
	e := newIngressConduit(t, &IngressConfig{Mode: "emulate", CAFile: caFile, CAKeyFile: keyFile})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts, err := e.AddIngress(ctx, "web", &api.IngressSpec{Hosts: []string{"secure.example.com"}, TLS: &api.IngressTLS{}}, []api.PortMapping{{Container: 8080}})
	if err != nil {
		t.Fatalf("AddIngress: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("hosts = %v", hosts)
	}
	for _, port := range []int{80, 443} {
		if k := kindOf(t, e.router, "secure.example.com", port); k != socks5.KindLocal {
			t.Fatalf("emulate TLS :%d Kind = %v, want KindLocal", port, k)
		}
	}
	cancel()
	waitDirect(t, e.router, "secure.example.com", 80)
	waitDirect(t, e.router, "secure.example.com", 443)
}

func TestAddIngressNativeWithoutControllerErrors(t *testing.T) {
	e := newIngressConduit(t, &IngressConfig{Mode: "native"})
	if _, err := e.AddIngress(context.Background(), "web", &api.IngressSpec{Hosts: []string{"a.example.com"}}, []api.PortMapping{{Container: 80}}); err == nil {
		t.Fatal("native ingress with no controller should error")
	}
}

func TestAddIngressOffAndDisabled(t *testing.T) {
	// Ingress off (nil config): no-op even for an enabled spec.
	off := newIngressConduit(t, nil)
	if hosts, err := off.AddIngress(context.Background(), "web", &api.IngressSpec{Hosts: []string{"a.example.com"}}, []api.PortMapping{{Container: 80}}); err != nil || hosts != nil {
		t.Fatalf("ingress off: hosts=%v err=%v, want nil,nil", hosts, err)
	}
	// Enabled config but a spec that requests no ingress: no-op.
	on := newIngressConduit(t, &IngressConfig{Mode: "emulate"})
	if hosts, err := on.AddIngress(context.Background(), "web", &api.IngressSpec{}, []api.PortMapping{{Container: 80}}); err != nil || hosts != nil {
		t.Fatalf("disabled spec: hosts=%v err=%v, want nil,nil", hosts, err)
	}
}

func TestPortForwardConduitAddIngressNoop(t *testing.T) {
	pf := &portForwardConduit{}
	if hosts, err := pf.AddIngress(context.Background(), "web", &api.IngressSpec{Hosts: []string{"a.example.com"}}, []api.PortMapping{{Container: 80}}); err != nil || hosts != nil {
		t.Fatalf("port-forward AddIngress = %v,%v, want nil,nil", hosts, err)
	}
	if hosts, err := (noopConduit{}).AddIngress(context.Background(), "web", &api.IngressSpec{Enabled: true}, nil); err != nil || hosts != nil {
		t.Fatalf("noop AddIngress = %v,%v, want nil,nil", hosts, err)
	}
}

// waitDirect polls until host:port resolves KindDirect (the async ctx-withdrawal ran).
func waitDirect(t *testing.T, r *socks5.Router, host string, port int) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if res, _ := r.Resolve(host, port); res.Kind == socks5.KindDirect {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s:%d still not withdrawn (want KindDirect)", host, port)
}
