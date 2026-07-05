package setupwiz

import (
	"context"
	"os"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/clientconfig"
)

// IngressFacts is what an ingress probe learned from the server's advertised ingress
// (GET /.cornus/v1/info). Reachable reports whether the server answered at all.
type IngressFacts struct {
	Reachable  bool
	Domain     string
	Class      string
	Controller *api.IngressController // nil when the server advertises no controller
}

// probeIngress materializes the answers into a throwaway config, resolves the
// connection exactly as a real command would, and reads the server's advertised
// ingress facts. Best-effort: any failure yields empty facts (Reachable false).
func probeIngress(ctx context.Context, a *Answers) IngressFacts {
	built := BuildContext(*a)
	tmp, err := os.CreateTemp("", "cornus-setup-probe-*.yaml")
	if err != nil {
		return IngressFacts{}
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(name) }()

	f := &clientconfig.File{CurrentContext: "probe", Contexts: map[string]*clientconfig.Context{"probe": built}}
	if err := clientconfig.Save(name, f); err != nil {
		return IngressFacts{}
	}
	r := &clientconn.Resolver{ConfigFile: name, Context: "probe"}
	cn, err := r.Resolve("")
	if err != nil {
		return IngressFacts{}
	}
	defer cn.Cleanup()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := cn.Client().Info(cctx)
	if err != nil {
		return IngressFacts{}
	}
	facts := IngressFacts{Reachable: true}
	if info.Ingress != nil {
		facts.Domain = info.Ingress.Domain
		facts.Class = info.Ingress.Class
		facts.Controller = info.Ingress.Controller
	}
	return facts
}

// ingressStep asks whether (and how) to reach service ingress through the SOCKS5
// conduit. It probes the server's advertised ingress facts, proposes a default mode
// (a discovered controller -> native; else a configured domain/class -> emulate; else
// off), and stores the user's choice on a. It degrades gracefully when the probe
// cannot reach the server.
func (w *Wizard) ingressStep(ctx context.Context, a *Answers) step {
	return step{ask: func() error {
		facts := w.Ingress(ctx, a)
		hasController := facts.Controller != nil && facts.Controller.Service != ""
		def := 0 // Off
		switch {
		case hasController:
			def = 1 // Native
		case facts.Domain != "" || facts.Class != "":
			def = 2 // Emulate
		}
		switch {
		case !facts.Reachable:
			w.ui.Note("could not probe the server for ingress; choose manually")
		case hasController:
			w.ui.Note("server advertises ingress controller %s/%s — native recommended", facts.Controller.Namespace, facts.Controller.Service)
		case facts.Domain != "" || facts.Class != "":
			w.ui.Note("server has an ingress domain/class but no discoverable controller — emulation recommended")
		default:
			w.ui.Note("server advertises no ingress front door")
		}
		idx, err := w.ui.Select("Reach service ingress (x-cornus-ingress) through the SOCKS5 conduit?", "", []Option{
			{Label: "Off", Desc: "do not route ingress through the conduit"},
			{Label: "Native", Desc: "tunnel to the real cluster ingress controller (needs kube access)"},
			{Label: "Emulate", Desc: "client-side reverse proxy with a generated TLS cert (any backend)"},
		}, def)
		if err != nil {
			return err
		}
		// Clear controller fields on every path; native re-fills them from the probe.
		a.IngressControllerNamespace, a.IngressControllerService = "", ""
		a.IngressControllerHTTPPort, a.IngressControllerHTTPSPort = 0, 0
		switch idx {
		case 1:
			a.IngressMode = "native"
			if c := facts.Controller; c != nil {
				a.IngressControllerNamespace = c.Namespace
				a.IngressControllerService = c.Service
				a.IngressControllerHTTPPort = c.HTTPPort
				a.IngressControllerHTTPSPort = c.HTTPSPort
			}
		case 2:
			a.IngressMode = "emulate"
		default:
			a.IngressMode = ""
		}
		return nil
	}}
}
