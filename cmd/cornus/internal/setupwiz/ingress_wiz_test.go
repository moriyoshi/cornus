package setupwiz

import (
	"context"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/clientconfig"
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
			// The probe only runs against a server the user says exists; these
			// cases are about what it makes of the facts it gets back.
			a := &Answers{ServerReady: true}
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

// Against a server that is not set up yet the probe is not merely useless, it
// is slow: each call resolves the transport and waits out a 15-second timeout,
// and the two ingress steps would spend that twice before asking anything.
func TestIngressStepsSkipTheProbeWhenNoServerExists(t *testing.T) {
	for _, name := range []string{"ingress", "tunnel"} {
		t.Run(name, func(t *testing.T) {
			probed := false
			ui := &defUI{}
			w := &Wizard{ui: ui, Ingress: func(context.Context, *Answers) IngressFacts {
				probed = true
				return IngressFacts{Reachable: true}
			}}
			a := &Answers{Scenario: ScenarioKubeURL}
			s := w.ingressStep(context.Background(), a)
			if name == "tunnel" {
				s = w.ingressTunnelStep(context.Background(), a)
			}
			if err := s.ask(); err != nil {
				t.Fatal(err)
			}
			if probed {
				t.Error("the ingress probe ran against a server the user says is not set up")
			}
			if !containsString(ui.notes, "the server is not set up yet, so its ingress cannot be probed; choose manually") &&
				!containsString(ui.notes, "the server is not set up yet, so its tunnel support cannot be probed; choose manually") {
				t.Errorf("the user was not told why nothing was probed: %v", ui.notes)
			}
			// Falling through to the manual choice must not enable anything by
			// accident: the defaults are Off / Not now.
			if a.IngressMode != "" || a.TunnelIngressHostMode != "" {
				t.Errorf("unprobed defaults enabled something: mode=%q tunnel=%q", a.IngressMode, a.TunnelIngressHostMode)
			}
		})
	}
}

// TestIngressTunnelStepStoresDefaults proves the wizard's tunnel answers land in
// the saved profile — and that only a credential PATH is stored, never a
// credential.
func TestIngressTunnelStepStoresDefaults(t *testing.T) {
	a := Answers{TunnelIngressHostMode: "alias", TunnelAuthTokenFile: "/tmp/token"}
	ctx := BuildContext(a)
	if ctx.Tunnel == nil {
		t.Fatal("tunnel defaults should be stored on the context")
	}
	if ctx.Tunnel.IngressHostMode != "alias" || ctx.Tunnel.AuthTokenFile != "/tmp/token" {
		t.Fatalf("tunnel block = %+v, want the answered defaults", ctx.Tunnel)
	}

	// No answers, no block: a profile should not carry an empty stanza.
	if got := BuildContext(Answers{}); got.Tunnel != nil {
		t.Errorf("tunnel block = %+v, want none when nothing was answered", got.Tunnel)
	}
}

// TestTunnelBlockMergeKeepsExplicitOverride pins the precedence a caller relies
// on: a later layer's non-empty field wins, an empty one leaves the base alone.
func TestTunnelBlockMergeKeepsExplicitOverride(t *testing.T) {
	base := &clientconfig.Tunnel{AuthTokenFile: "/base/token", IngressHostMode: "auto"}
	got := base.Merge(&clientconfig.Tunnel{IngressHostMode: "rewrite"})
	if got.IngressHostMode != "rewrite" {
		t.Errorf("host mode = %q, want the override", got.IngressHostMode)
	}
	if got.AuthTokenFile != "/base/token" {
		t.Errorf("authtoken file = %q, want the base kept", got.AuthTokenFile)
	}
	if base.IngressHostMode != "auto" {
		t.Error("Merge must not mutate its receiver")
	}
}
