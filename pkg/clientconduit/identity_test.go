package clientconduit

import (
	"testing"

	"cornus/pkg/ingressnative"
	"cornus/pkg/socks5"
)

func TestIdentitySessionLocalScope(t *testing.T) {
	cfg := Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0", Socks5SessionLocal: true}
	if got, want := cfg.Identity("one"), cfg.Identity("one"); got != want {
		t.Fatalf("same session identity differs: %+v != %+v", got, want)
	}
	if cfg.Identity("one") == cfg.Identity("two") {
		t.Fatal("session-local identities should differ across sessions")
	}
	cfg.Socks5SessionLocal = false
	if cfg.Identity("one") != cfg.Identity("two") {
		t.Fatal("shared identities should not include session name")
	}
}

func TestIdentityNilAndEmptyRulesAreStable(t *testing.T) {
	a := Config{Mode: ModeSocks5}
	b := Config{Mode: ModeSocks5, Socks5Resolve: []socks5.Rule{}}
	if a.Identity("session") != b.Identity("session") {
		t.Fatal("nil and empty rule lists should have the same identity")
	}
	// A nil bare-name option means unspecified; an explicit false is distinct.
	falseValue := false
	if a.Identity("session") == (Config{Mode: ModeSocks5, Socks5BareServiceNames: &falseValue}).Identity("session") {
		t.Fatal("explicit bare-name false should differ from unspecified")
	}
}

func TestIdentityIncludesNativeController(t *testing.T) {
	base := Config{Mode: ModeSocks5, Ingress: &IngressConfig{Mode: IngressNative, Controller: &ingressnative.Controller{
		KubeContext: "ctx", Namespace: "ns", Service: "gateway", HTTPPort: 80, HTTPSPort: 443,
	}}}
	variants := []Config{base, Config{Mode: ModeSocks5, Ingress: &IngressConfig{Mode: IngressNative, Controller: &ingressnative.Controller{KubeContext: "ctx", Namespace: "ns", Service: "gateway", HTTPPort: 80, HTTPSPort: 443}}}}
	variants[1].Ingress.Controller = &ingressnative.Controller{KubeContext: "ctx2", Namespace: "ns", Service: "gateway", HTTPPort: 80, HTTPSPort: 443}
	if variants[0].Identity("s") == variants[1].Identity("s") {
		t.Fatal("controller context changes must change identity")
	}
	variants[1] = Config{Mode: ModeSocks5, Ingress: &IngressConfig{Mode: IngressNative, Controller: &ingressnative.Controller{KubeContext: "ctx", Namespace: "ns2", Service: "gateway", HTTPPort: 80, HTTPSPort: 443}}}
	variants[1].Ingress.Controller = &ingressnative.Controller{KubeContext: "ctx", Namespace: "ns2", Service: "gateway", HTTPPort: 80, HTTPSPort: 443}
	if variants[0].Identity("s") == variants[1].Identity("s") {
		t.Fatal("controller namespace changes must change identity")
	}
}
