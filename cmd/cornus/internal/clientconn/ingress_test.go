package clientconn

import (
	"context"
	"reflect"
	"testing"

	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

func TestIngressModePrecedence(t *testing.T) {
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")
	prof := &clientconfig.Conduit{Ingress: &clientconfig.Ingress{Mode: "emulate"}}
	cn := connWithProfileAndEnv(t, prof)

	if got := cn.IngressMode("native"); got != "native" {
		t.Errorf("flag override = %q, want native", got)
	}
	t.Setenv("CORNUS_INGRESS_CONDUIT", "native")
	cn = connWithProfileAndEnv(t, prof)
	if got := cn.IngressMode(""); got != "native" {
		t.Errorf("env over profile = %q, want native", got)
	}
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")
	cn = connWithProfileAndEnv(t, prof)
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
	cn := connWithProfileAndEnv(t, prof)
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

func TestConfigSourcesMergeEveryIngressField(t *testing.T) {
	profileConduit := &clientconfig.Conduit{
		Mode: "socks5",
		Socks5: &clientconfig.Socks5{
			Listen:            "127.0.0.1:1080",
			ServiceHostSuffix: ".profile.internal",
			Resolve:           []clientconfig.ResolveRule{{Pattern: "profile", Replace: "service"}},
		},
		Ingress: &clientconfig.Ingress{
			Mode:       "emulate",
			Controller: &clientconfig.IngressController{Namespace: "profile-ns", Service: "profile-controller", HTTPPort: 80, HTTPSPort: 443},
			CAFile:     "profile-ca.pem",
			CAKeyFile:  "profile-ca.key",
		},
	}
	wantProfile := profileConduit.Clone()
	t.Setenv("CORNUS_VIA_SERVER", "true")
	t.Setenv("CORNUS_CONDUIT", "socks5://127.0.0.1:1088")
	t.Setenv("CORNUS_INGRESS_CONDUIT", "native")
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "env-ns/env-controller:8080/8443")
	t.Setenv("CORNUS_INGRESS_EMULATED_CA", "env-ca.pem")
	t.Setenv("CORNUS_INGRESS_EMULATED_CA_KEY", "")
	env, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	cli, err := ConfigFromOptions(ptr(false), "socks5", "emulate", "cli-ns/cli-controller:9080/9443", "", "cli-ca.key")
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	resolved := resolveConfig(configFromContext(&clientconfig.Context{Conduit: profileConduit}), env, cli)
	if resolved.ViaServer == nil || *resolved.ViaServer {
		t.Errorf("ViaServer = %v, want explicit CLI false", resolved.ViaServer)
	}
	cn := &Conn{Config: resolved, KubeCluster: &KubeCluster{KubeContext: "dev-cluster"}}
	cfg := cn.ConduitConfigFor()
	if cfg.Mode != clientconduit.ModeSocks5 || cfg.Socks5Listen != "127.0.0.1:1088" || cfg.Socks5Suffix != ".profile.internal" {
		t.Errorf("runtime conduit = %+v", cfg)
	}
	if len(cfg.Socks5Resolve) != 1 || cfg.Socks5Resolve[0].Pattern != "profile" {
		t.Errorf("runtime resolve rules = %+v", cfg.Socks5Resolve)
	}
	cn.ApplyIngressConfig(context.Background(), &cfg)
	if cfg.Ingress == nil {
		t.Fatal("merged ingress is nil")
	}
	if cfg.Ingress.Mode != "emulate" || cfg.Ingress.CAFile != "env-ca.pem" || cfg.Ingress.CAKeyFile != "cli-ca.key" {
		t.Errorf("merged ingress = %+v", cfg.Ingress)
	}
	controller := cfg.Ingress.Controller
	if controller == nil || controller.KubeContext != "dev-cluster" || controller.Namespace != "cli-ns" || controller.Service != "cli-controller" || controller.HTTPPort != 9080 || controller.HTTPSPort != 9443 {
		t.Errorf("merged controller = %+v", controller)
	}
	if !reflect.DeepEqual(profileConduit, wantProfile) {
		t.Errorf("profile mutated: got %+v, want %+v", profileConduit, wantProfile)
	}
}

func TestExplicitOffAndEmptyURLFieldsClearInheritedConfig(t *testing.T) {
	profile := configFromContext(&clientconfig.Context{Conduit: &clientconfig.Conduit{
		Mode: "socks5",
		Socks5: &clientconfig.Socks5{
			Listen:            "127.0.0.1:1080",
			ServiceHostSuffix: ".profile.internal",
		},
		Ingress: &clientconfig.Ingress{Mode: "native"},
	}})
	override, err := ConfigFromOptions(nil, "socks5://.shared?suffix=", "off", "", "", "")
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	cn := &Conn{Config: resolveConfig(profile, override)}
	cfg := cn.ConduitConfigFor()
	if cfg.Socks5SessionLocal || cfg.Socks5Listen != "" || cfg.Socks5Suffix != "" {
		t.Errorf("cleared SOCKS5 config = %+v", cfg)
	}
	cn.ApplyIngressConfig(context.Background(), &cfg)
	if cfg.Ingress != nil {
		t.Errorf("explicit off produced ingress %+v", cfg.Ingress)
	}
}

func TestConfigFromEnvRejectsInvalidIngressController(t *testing.T) {
	t.Setenv("CORNUS_CONDUIT", "")
	t.Setenv("CORNUS_INGRESS_CONDUIT", "")
	t.Setenv("CORNUS_INGRESS_CONTROLLER", "missing-namespace")
	t.Setenv("CORNUS_INGRESS_EMULATED_CA", "")
	t.Setenv("CORNUS_INGRESS_EMULATED_CA_KEY", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted an invalid ingress controller")
	}
}
