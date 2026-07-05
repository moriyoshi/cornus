package clientagent

import (
	"strings"
	"testing"

	"cornus/pkg/clientconduit"
	"cornus/pkg/ingressemu"
	"cornus/pkg/ingressnative"
)

// ingressWebServeReq publishes a web UI over a SOCKS5 conduit carrying ing. It is
// SOCKS5 because doWebServe needs a conduit that resolves names, and because only
// the SOCKS5 conduit answers CAInfo — a port-forward conduit returns nil, so it
// could never show that the Trust lines come from the LIVE conduit rather than
// from the configuration.
func ingressWebServeReq(server, name string, ing *clientconduit.IngressConfig) Request {
	cfg := socks5Conduit()
	cfg.Ingress = ing
	return Request{
		Action:  "web-serve",
		Web:     WebSpec{Name: name, Port: 80},
		Conn:    ConnSpec{Server: server},
		Conduit: ToWireConduit(cfg),
	}
}

// The client half of the web UI's Ingress section: how THIS client realizes
// ingress. The server's own /info says nothing about it — it is the conduit's
// setting — so the inventory is the only place it can come from.
func TestInventoryReportsEmulatedClientIngress(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	resp, fe := a.doWebServe(ingressWebServeReq("http://fake:5000", "cornus.internal", &clientconduit.IngressConfig{
		Mode:         clientconduit.IngressEmulate,
		SuffixDomain: "shop.test",
		Certificates: []ingressemu.CertificateSource{
			{Pattern: "*.shop.test", CertFile: "/certs/shop.pem", KeyFile: "/certs/shop-key.pem"},
		},
	}))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	inv := a.inventory()
	if len(inv.Ingress) != 1 {
		t.Fatalf("inventory ingress = %+v, want exactly one entry", inv.Ingress)
	}
	got := inv.Ingress[0]
	if got.Mode != "emulate" || got.Domain != "shop.test" {
		t.Errorf("mode/domain = %q/%q, want emulate/shop.test", got.Mode, got.Domain)
	}
	// Emulate mode has no controller to reach; reporting one would describe a
	// passthrough that does not happen.
	if got.Controller != nil {
		t.Errorf("controller = %+v, want none in emulate mode", got.Controller)
	}
	// The trust lines are what turn a browser's TLS error into an action, and they
	// come from the conduit itself: the fallback CA in the common case is one the
	// conduit generated and no configuration names.
	if len(got.Trust) != 1 || got.Trust[0] != "emulated ingress TLS certificate for *.shop.test: /certs/shop.pem" {
		t.Errorf("trust = %q, want the conduit's own certificate line", got.Trust)
	}
	// The private key's location is not part of the answer: CAInfo omits it, and the
	// auth-less web UI has no use for it. Asserted because the key file IS in the
	// config this entry was built from, so a wider mapping would leak it.
	for _, line := range got.Trust {
		if line == "" {
			continue
		}
		if strings.Contains(line, "shop-key.pem") {
			t.Errorf("trust line leaks the private key path: %q", line)
		}
	}
}

func TestInventoryReportsNativeClientIngressController(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	resp, fe := a.doWebServe(ingressWebServeReq("http://fake:5000", "cornus.internal", &clientconduit.IngressConfig{
		Mode:         clientconduit.IngressNative,
		SuffixDomain: "shop.test",
		Controller: &ingressnative.Controller{
			KubeContext: "kind-cornus",
			Namespace:   "ingress-nginx",
			Service:     "ingress-nginx-controller",
			HTTPPort:    80,
			HTTPSPort:   443,
		},
	}))
	if !resp.OK || fe == nil {
		t.Fatalf("web-serve = %+v", resp)
	}

	inv := a.inventory()
	if len(inv.Ingress) != 1 {
		t.Fatalf("inventory ingress = %+v, want exactly one entry", inv.Ingress)
	}
	got := inv.Ingress[0]
	if got.Mode != "native" {
		t.Errorf("mode = %q, want native", got.Mode)
	}
	want := AgentIngressController{
		KubeContext: "kind-cornus",
		Namespace:   "ingress-nginx",
		Service:     "ingress-nginx-controller",
		HTTPPort:    80,
		HTTPSPort:   443,
	}
	if got.Controller == nil || *got.Controller != want {
		t.Errorf("controller = %+v, want %+v", got.Controller, want)
	}
	// Native mode presents the real controller's certificate, so there is nothing
	// for a browser to be told to trust — and an empty list is what lets the UI omit
	// the row rather than render one implying otherwise.
	if len(got.Trust) != 0 {
		t.Errorf("trust = %q, want none in native mode", got.Trust)
	}
}

// A conduit that routes no ingress contributes no entry — not an entry with an
// empty mode, which no consumer could interpret. The banner assertion is the other
// half: ingress rides the same walk over the live conduits, so a mistake there
// could silently stop the conduit panel reporting anything.
func TestInventoryReportsNoIngressWhenTheConduitRoutesNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		ing  *clientconduit.IngressConfig
	}{
		{"unconfigured", nil},
		{"explicitly off", &clientconduit.IngressConfig{Mode: clientconduit.IngressOff, SuffixDomain: "shop.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAgent(t, fakeResolve(nil))
			resp, fe := a.doWebServe(ingressWebServeReq("http://fake:5000", "cornus.internal", tc.ing))
			if !resp.OK || fe == nil {
				t.Fatalf("web-serve = %+v", resp)
			}
			inv := a.inventory()
			if len(inv.Ingress) != 0 {
				t.Errorf("inventory ingress = %+v, want none", inv.Ingress)
			}
			if len(inv.Banners) == 0 {
				t.Errorf("banners = %v, want the SOCKS5 listen line: ingress shares this walk and must not displace it", inv.Banners)
			}
		})
	}
}

// Two conduits — two servers, so two connStates and two live conduits — running
// the SAME ingress settings are one answer, not two. The UI renders a block per
// entry, so a duplicate would show a reader the same setting twice and imply their
// session has two.
func TestInventoryDeduplicatesIdenticalClientIngress(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	ing := func() *clientconduit.IngressConfig {
		return &clientconduit.IngressConfig{Mode: clientconduit.IngressEmulate, SuffixDomain: "shop.test"}
	}
	for _, s := range []struct{ server, name string }{
		{"http://one:5000", "one.internal"},
		{"http://two:5000", "two.internal"},
	} {
		if resp, fe := a.doWebServe(ingressWebServeReq(s.server, s.name, ing())); !resp.OK || fe == nil {
			t.Fatalf("web-serve %s = %+v", s.name, resp)
		}
	}
	// Two conduits really are live, or the dedup below would be vacuous.
	a.mu.Lock()
	nConns := len(a.conns)
	a.mu.Unlock()
	if nConns != 2 {
		t.Fatalf("conns = %d, want 2 — the dedup assertion needs two live conduits to mean anything", nConns)
	}

	if inv := a.inventory(); len(inv.Ingress) != 1 {
		t.Fatalf("inventory ingress = %+v, want one deduplicated entry", inv.Ingress)
	}

	// But settings that DIFFER are two answers: a workload reached through one is
	// not reached through the other.
	other := ing()
	other.SuffixDomain = "other.test"
	if resp, fe := a.doWebServe(ingressWebServeReq("http://three:5000", "three.internal", other)); !resp.OK || fe == nil {
		t.Fatalf("web-serve three = %+v", resp)
	}
	inv := a.inventory()
	if len(inv.Ingress) != 2 {
		t.Fatalf("inventory ingress = %+v, want two entries", inv.Ingress)
	}
	// Sorted, so a status poll of an unchanged agent cannot reorder the UI.
	if inv.Ingress[0].Domain != "other.test" || inv.Ingress[1].Domain != "shop.test" {
		t.Errorf("domains = %q, %q; want them sorted", inv.Ingress[0].Domain, inv.Ingress[1].Domain)
	}
}
