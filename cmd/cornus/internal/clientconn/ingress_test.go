package clientconn

import (
	"context"
	"testing"

	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

func TestIngressModePrecedence(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")
	prof := &clientconfig.Conduit{Ingress: &clientconfig.Ingress{Mode: "emulate"}}
	cn := &Conn{ProfileConduit: prof}

	if got := cn.IngressMode("native"); got != "native" {
		t.Errorf("flag override = %q, want native", got)
	}
	t.Setenv("CORNUS_INGRESS_CONDUIT", "native")
	if got := cn.IngressMode(""); got != "native" {
		t.Errorf("env over profile = %q, want native", got)
	}
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")
	if got := cn.IngressMode(""); got != "emulate" {
		t.Errorf("profile = %q, want emulate", got)
	}
	if got := cn.IngressMode("off"); got != "" {
		t.Errorf("off flag = %q, want empty", got)
	}
	if got := (&Conn{}).IngressMode(""); got != "" {
		t.Errorf("unset = %q, want empty", got)
	}
}

func TestApplyIngressNativeFromProfileController(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")
	prof := &clientconfig.Conduit{Ingress: &clientconfig.Ingress{
		Mode:       "native",
		Controller: &clientconfig.IngressController{Namespace: "ingress-nginx", Service: "ctrl", HTTPPort: 80, HTTPSPort: 443},
		CAFile:     "ca.pem",
		CAKeyFile:  "ca.key",
	}}
	cn := &Conn{ProfileConduit: prof}
	cfg := clientconduit.Config{Socks5Suffix: ".demo.internal"}
	cn.ApplyIngress(context.Background(), &cfg, "")

	if cfg.Ingress == nil {
		t.Fatal("cfg.Ingress is nil")
	}
	if cfg.Ingress.Mode != "native" {
		t.Errorf("mode = %q, want native", cfg.Ingress.Mode)
	}
	if cfg.Ingress.SuffixDomain != "demo.internal" {
		t.Errorf("suffix domain = %q, want demo.internal", cfg.Ingress.SuffixDomain)
	}
	if c := cfg.Ingress.Controller; c == nil || c.Service != "ctrl" || c.Namespace != "ingress-nginx" {
		t.Errorf("controller = %+v", c)
	}
	if cfg.Ingress.CAFile != "ca.pem" || cfg.Ingress.CAKeyFile != "ca.key" {
		t.Errorf("CA = %q/%q", cfg.Ingress.CAFile, cfg.Ingress.CAKeyFile)
	}
}

func TestApplyIngressEmulateAndOff(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")

	cn := &Conn{}
	cfg := clientconduit.Config{}
	cn.ApplyIngress(context.Background(), &cfg, "emulate")
	if cfg.Ingress == nil || cfg.Ingress.Mode != "emulate" || cfg.Ingress.Controller != nil {
		t.Fatalf("emulate: %+v", cfg.Ingress)
	}

	cfg2 := clientconduit.Config{}
	cn.ApplyIngress(context.Background(), &cfg2, "off")
	if cfg2.Ingress != nil {
		t.Fatalf("off: want nil, got %+v", cfg2.Ingress)
	}
}
