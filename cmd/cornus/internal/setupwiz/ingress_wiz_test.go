package setupwiz

import (
	"context"
	"testing"

	"cornus/pkg/api"
)

func TestBuildContextIngressNative(t *testing.T) {
	ctx := BuildContext(Answers{
		Scenario:                   ScenarioKubePortForward,
		Server:                     "http://x:5000",
		IngressMode:                "native",
		IngressControllerNamespace: "ingress-nginx",
		IngressControllerService:   "ctrl",
		IngressControllerHTTPPort:  80,
		IngressControllerHTTPSPort: 443,
	})
	if ctx.Conduit == nil || ctx.Conduit.Mode != "socks5" {
		t.Fatalf("conduit = %+v, want socks5", ctx.Conduit)
	}
	in := ctx.Conduit.Ingress
	if in == nil || in.Mode != "native" {
		t.Fatalf("ingress = %+v", in)
	}
	if in.Controller == nil || in.Controller.Service != "ctrl" || in.Controller.Namespace != "ingress-nginx" {
		t.Fatalf("controller = %+v", in.Controller)
	}
}

func TestBuildContextIngressEmulate(t *testing.T) {
	ctx := BuildContext(Answers{IngressMode: "emulate"})
	if ctx.Conduit == nil || ctx.Conduit.Mode != "socks5" || ctx.Conduit.Ingress == nil {
		t.Fatalf("conduit = %+v", ctx.Conduit)
	}
	if ctx.Conduit.Ingress.Mode != "emulate" || ctx.Conduit.Ingress.Controller != nil {
		t.Fatalf("ingress = %+v", ctx.Conduit.Ingress)
	}
}

func TestBuildContextNoIngressNoConduit(t *testing.T) {
	ctx := BuildContext(Answers{Server: "http://x:5000"})
	if ctx.Conduit != nil {
		t.Fatalf("no ingress should leave Conduit nil, got %+v", ctx.Conduit)
	}
}

func TestSetContextArgsIngress(t *testing.T) {
	ctx := BuildContext(Answers{
		IngressMode:                "native",
		IngressControllerNamespace: "ns",
		IngressControllerService:   "svc",
		IngressControllerHTTPPort:  80,
		IngressControllerHTTPSPort: 443,
	})
	args := SetContextArgs("k", ctx)
	wantPairs := map[string]string{
		"--conduit-mode":       "socks5",
		"--ingress-conduit":    "native",
		"--ingress-controller": "ns/svc:80/443",
	}
	for flag, val := range wantPairs {
		if !hasArgPair(args, flag, val) {
			t.Errorf("SetContextArgs missing %s %s in %v", flag, val, args)
		}
	}
}

func hasArgPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

// defUI returns the default index/value for every prompt, so a test exercises the
// wizard's own computed defaults.
type defUI struct{ notes []string }

func (u *defUI) Select(_, _ string, _ []Option, def int) (int, error) { return def, nil }
func (u *defUI) Input(q Question) (string, error)                     { return q.Default, nil }
func (u *defUI) Confirm(_ string, def bool) (bool, error)             { return def, nil }
func (u *defUI) Note(format string, _ ...any)                         { u.notes = append(u.notes, format) }

func TestIngressStepDefaults(t *testing.T) {
	cases := []struct {
		name     string
		facts    IngressFacts
		wantMode string
		wantSvc  string
	}{
		{"controller -> native", IngressFacts{Reachable: true, Controller: &api.IngressController{Namespace: "ingress-nginx", Service: "ctrl", HTTPPort: 80, HTTPSPort: 443}}, "native", "ctrl"},
		{"domain only -> emulate", IngressFacts{Reachable: true, Domain: "example.com"}, "emulate", ""},
		{"nothing -> off", IngressFacts{Reachable: true}, "", ""},
		{"unreachable -> off", IngressFacts{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &Wizard{ui: &defUI{}, Ingress: func(context.Context, *Answers) IngressFacts { return tc.facts }}
			a := &Answers{}
			if err := w.ingressStep(context.Background(), a).ask(); err != nil {
				t.Fatal(err)
			}
			if a.IngressMode != tc.wantMode {
				t.Errorf("mode = %q, want %q", a.IngressMode, tc.wantMode)
			}
			if a.IngressControllerService != tc.wantSvc {
				t.Errorf("controller service = %q, want %q", a.IngressControllerService, tc.wantSvc)
			}
		})
	}
}
