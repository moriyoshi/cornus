// Package clientconduit is the one seam every client session surface uses to
// expose a deployment's ports to the caller. It has two modes behind a single
// interface: the default per-port automatic forwarding (pkg/portfwd, one local
// listener per published port) and an opt-in SOCKS5 split-tunnel proxy
// (pkg/socks5, one listener reaching every deployment by name). Collapsing both
// behind Conduit keeps the mode branch in one place — the deploy, compose
// foreground, and compose daemon surfaces just call Add.
package clientconduit

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"cornus/pkg/api"
	"cornus/pkg/ingressemu"
	"cornus/pkg/ingressnative"
	"cornus/pkg/logging"
	"cornus/pkg/portfwd"
	"cornus/pkg/socks5"
)

// Conduit modes.
const (
	ModePortForward = "port-forward" // default: one local listener per published port
	ModeSocks5      = "socks5"       // one SOCKS5 split-tunnel proxy for the session
	ModeNone        = "none"         // expose nothing (the --no-forward-ports case)
)

// Config is the resolved conduit configuration for a session.
type Config struct {
	// Mode is ModePortForward (default when ""), ModeSocks5, or ModeNone.
	Mode string
	// Socks5Listen is the SOCKS5 proxy's local bind address (default when empty:
	// socks5.DefaultListen).
	Socks5Listen string
	// Socks5Suffix builds the everyday default resolution rule (a host bearing the
	// suffix is tunneled in, stripped to a service name); ignored when Socks5Resolve
	// is set. Empty uses socks5.DefaultSuffix.
	Socks5Suffix string
	// Socks5Resolve, when non-empty, is the advanced ordered rule list, replacing
	// the suffix default.
	Socks5Resolve []socks5.Rule
	// Socks5BareServiceNames toggles whether a bare, single-label CONNECT host that
	// names a live service is routed inward (in addition to the "<name><suffix>"
	// form). nil keeps the default (enabled); set false to disable it when a service
	// name would shadow a real single-label host the caller reaches directly.
	Socks5BareServiceNames *bool
	// Socks5SessionLocal marks this conduit as a private, session-scoped proxy: the
	// client agent gives it its own listener + alias table instead of joining the
	// proxy shared per tunnel config, so it can coexist with a shared proxy and other
	// session-local ones. Direct (non-agent) sessions always own their proxy, so it
	// only influences the address (an ephemeral Socks5Listen) for them.
	Socks5SessionLocal bool
	// Socks5AllowNonLoopback permits binding the proxy to a non-loopback address.
	// The proxy is refused a non-loopback bind by default because it has no
	// authentication and dials arbitrary destinations from its host, so off-host it
	// is an open proxy. Set this only when that exposure is intended.
	Socks5AllowNonLoopback bool
	// Ingress, when non-nil and Mode is socks5, reaches a workload's declared ingress
	// host(s) through the proxy (see Conduit.AddIngress). nil means off.
	Ingress *IngressConfig
}

// IngressConfig is the resolved ingress-via-conduit configuration for a session.
type IngressConfig struct {
	// Mode is "native" (transparent tunnel to the real cluster ingress controller) or
	// "emulate" (client-side HTTP(S) reverse proxy). Empty disables registration.
	Mode string
	// Controller is the native-passthrough target (native mode). A nil or serviceless
	// controller makes native registration fail loudly rather than silently no-op.
	Controller *ingressnative.Controller
	// SuffixDomain backs emulate host auto-derivation; empty uses the conduit's
	// service-host suffix stem (then ingressemu.DefaultSuffixDomain).
	SuffixDomain string
	// CAFile / CAKeyFile override the emulate CA; empty uses a generated, persisted
	// session CA.
	CAFile    string
	CAKeyFile string
}

// Forward describes one bound local listener (port-forward mode). SOCKS5 mode
// exposes no per-deployment listeners, so Add returns none.
type Forward struct {
	Local     string // bound local address
	Name      string // deployment name
	Container int    // container port
	Protocol  string // "tcp" or "udp"
}

// Conduit exposes deployments to the caller for one session's lifetime.
type Conduit interface {
	// Banner is the one-time, session-level description to show after Start
	// (SOCKS5: the proxy listen line; port-forward / none: nil).
	Banner() []string
	// Add exposes a deployment's ports. In port-forward mode it binds one listener
	// per port (tied to ctx, so passing a per-service context withdraws just that
	// deployment when the context ends) and returns them. In SOCKS5 mode it binds no
	// listeners — the single proxy already reaches every deployment by name — but it
	// does register each alias (an unqualified service label, e.g. the compose
	// service name "web") as a name for the deployment, withdrawn when ctx ends, so
	// the caller can reach the deployment by that short name too. Non-compose callers
	// pass no aliases.
	Add(ctx context.Context, name string, ports []api.PortMapping, aliases ...string) ([]Forward, error)
	// AddLocal publishes an in-process listener under host:port, so a caller reaching
	// that exact name through the conduit is handed straight to d — no address is
	// bound and nothing is dialed. It is how a client-side surface (today the web
	// UI's BFF) joins the same name space as the workloads, so one browser proxy
	// setting reaches both. The name is withdrawn when ctx ends.
	//
	// It reports whether the conduit published it. Only SOCKS5 mode resolves names,
	// so port-forward and none publish nothing and return false — letting the caller
	// say so rather than promise a URL that will never resolve.
	AddLocal(ctx context.Context, host string, port int, d socks5.LocalDialer) (bool, error)
	// AddIngress registers the workload's declared ingress host(s) in the conduit so a
	// caller reaching them through the proxy is routed to the workload: native mode
	// tunnels to the real cluster ingress controller (which does Host/path routing and
	// TLS), emulate mode runs a client-side HTTP(S) reverse proxy. It returns the
	// published hostnames. Only SOCKS5 mode with an ingress config resolves names;
	// port-forward and none publish nothing and return nil. The registration is
	// withdrawn when ctx ends.
	AddIngress(ctx context.Context, name string, in *api.IngressSpec, ports []api.PortMapping) ([]string, error)
	// Close tears the conduit down (all port-forward groups, or the SOCKS5 proxy).
	Close()
}

// Option configures Start.
type Option func(*options)

type options struct {
	logf func(format string, args ...any)
}

// WithLogf routes non-fatal warnings (default: slog warnings).
func WithLogf(logf func(format string, args ...any)) Option {
	return func(o *options) {
		if logf != nil {
			o.logf = logf
		}
	}
}

// Start builds the Conduit for cfg.Mode. The SOCKS5 proxy (if selected) is bound
// immediately and tied to ctx.
func Start(ctx context.Context, d portfwd.Dialer, cfg Config, opts ...Option) (Conduit, error) {
	log := logging.FromContext(ctx)
	o := options{logf: func(format string, args ...any) { log.WarnContext(ctx, fmt.Sprintf(format, args...)) }}
	for _, opt := range opts {
		opt(&o)
	}
	switch cfg.Mode {
	case ModeNone:
		return noopConduit{}, nil
	case ModeSocks5:
		router, err := Router(cfg)
		if err != nil {
			return nil, err
		}
		p, err := socks5.Start(ctx, d, router, cfg.Socks5Listen, socks5.WithLogf(o.logf), socks5.WithAllowNonLoopback(cfg.Socks5AllowNonLoopback))
		if err != nil {
			return nil, err
		}
		return &socks5Conduit{proxy: p, router: router, banner: socks5Banner(cfg, p.Addr()), d: d, ingress: cfg.Ingress}, nil
	case ModePortForward, "":
		return &portForwardConduit{d: d, logf: o.logf}, nil
	default:
		return nil, fmt.Errorf("unknown conduit mode %q (want %q, %q, or %q)", cfg.Mode, ModePortForward, ModeSocks5, ModeNone)
	}
}

// Router builds the SOCKS5 resolution router for cfg: the explicit rule list when
// set, else the suffix default. Exported so the compose daemon can validate the
// configuration up front too.
func Router(cfg Config) (*socks5.Router, error) {
	var (
		router *socks5.Router
		err    error
	)
	if len(cfg.Socks5Resolve) > 0 {
		router, err = socks5.NewRouter(cfg.Socks5Resolve)
	} else {
		router, err = socks5.NewSuffixRouter(cfg.Socks5Suffix)
	}
	if err != nil {
		return nil, err
	}
	if cfg.Socks5BareServiceNames != nil {
		router.SetBareServiceNames(*cfg.Socks5BareServiceNames)
	}
	return router, nil
}

func socks5Banner(cfg Config, addr string) []string {
	if len(cfg.Socks5Resolve) > 0 {
		return []string{fmt.Sprintf("SOCKS5 proxy listening on %s (custom resolution rules)", addr)}
	}
	suffix := cfg.Socks5Suffix
	if suffix == "" {
		suffix = socks5.DefaultSuffix
	}
	return []string{fmt.Sprintf("SOCKS5 proxy listening on %s (reach a service as <name>%s:<port>)", addr, suffix)}
}

// portForwardConduit binds per-port local listeners, one portfwd.Group per Add.
type portForwardConduit struct {
	d    portfwd.Dialer
	logf func(format string, args ...any)

	mu     sync.Mutex
	groups []*portfwd.Group
}

func (e *portForwardConduit) Banner() []string { return nil }

func (e *portForwardConduit) Add(ctx context.Context, name string, ports []api.PortMapping, _ ...string) ([]Forward, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	g, err := portfwd.Start(ctx, e.d, name, ports, portfwd.WithLogf(e.logf))
	if err != nil {
		return nil, err
	}
	fs := g.Forwards()
	if len(fs) == 0 { // every mapping skipped (already bound, UDP unsupported, ...)
		g.Close()
		return nil, nil
	}
	e.mu.Lock()
	e.groups = append(e.groups, g)
	e.mu.Unlock()
	// Drop the group from the tracking slice when its ctx is cancelled (service
	// down / container exit), so a long-lived shared conduit in the agent does not
	// accumulate dead groups. The group itself self-closes on the same ctx.
	go func() {
		<-ctx.Done()
		e.removeGroup(g)
	}()
	out := make([]Forward, 0, len(fs))
	for _, f := range fs {
		out = append(out, Forward{Local: f.Local, Name: name, Container: f.Mapping.Container, Protocol: f.Mapping.Protocol})
	}
	return out, nil
}

// AddLocal publishes nothing: port-forward mode resolves no names, it binds one
// listener per port. Reporting false lets the caller surface that rather than
// promise an unreachable name.
func (e *portForwardConduit) AddLocal(context.Context, string, int, socks5.LocalDialer) (bool, error) {
	return false, nil
}

// AddIngress publishes nothing: port-forward mode resolves no names.
func (e *portForwardConduit) AddIngress(context.Context, string, *api.IngressSpec, []api.PortMapping) ([]string, error) {
	return nil, nil
}

func (e *portForwardConduit) Close() {
	e.mu.Lock()
	groups := e.groups
	e.groups = nil
	e.mu.Unlock()
	for _, g := range groups {
		g.Close()
	}
}

// removeGroup drops g from the tracking slice once its ctx-cancel has closed it.
func (e *portForwardConduit) removeGroup(g *portfwd.Group) {
	e.mu.Lock()
	for i, x := range e.groups {
		if x == g {
			e.groups = append(e.groups[:i], e.groups[i+1:]...)
			break
		}
	}
	e.mu.Unlock()
}

// socks5Conduit owns the single session-wide SOCKS5 proxy and the alias table its
// router resolves against.
type socks5Conduit struct {
	proxy  *socks5.Proxy
	router *socks5.Router
	banner []string
	// d is the port-forward transport, reused by an emulated ingress to reach the
	// workload's container port; nil-safe (only AddIngress emulate mode uses it).
	d portfwd.Dialer
	// ingress is the resolved ingress-via-conduit config; nil means off.
	ingress *IngressConfig
	// caOnce lazily builds the emulate CA once, shared by every emulated ingress.
	caOnce sync.Once
	ca     *ingressemu.CA
	caErr  error
}

func (e *socks5Conduit) Banner() []string { return e.banner }

// Add binds no per-port listeners (the proxy already reaches every deployment by
// name) but registers each alias as a short name for the deployment, withdrawn when
// ctx ends so the alias is pure session state that dies with the service.
func (e *socks5Conduit) Add(ctx context.Context, name string, _ []api.PortMapping, aliases ...string) ([]Forward, error) {
	for _, alias := range aliases {
		if alias == "" || alias == name {
			continue // nothing to add beyond the deployment name the proxy already serves
		}
		e.router.RegisterAlias(alias, name)
		go func(alias string) {
			<-ctx.Done()
			e.router.UnregisterAlias(alias, name)
		}(alias)
	}
	return nil, nil
}

// AddLocal publishes d under host:port in the proxy's router, withdrawn when ctx
// ends — the same pure-session-state discipline Add applies to aliases.
func (e *socks5Conduit) AddLocal(ctx context.Context, host string, port int, d socks5.LocalDialer) (bool, error) {
	if host == "" || port < 1 || port > 65535 || d == nil {
		return false, fmt.Errorf("clientconduit: publish %s:%d: need a non-empty host, a port in 1-65535, and a dialer", host, port)
	}
	e.router.RegisterLocal(host, port, d)
	go func() {
		<-ctx.Done()
		e.router.UnregisterLocal(host, port)
	}()
	return true, nil
}

// AddIngress registers the workload's ingress host(s) for the configured mode,
// withdrawn when ctx ends. It no-ops (returns nil) when ingress is off or the spec
// requests none.
func (e *socks5Conduit) AddIngress(ctx context.Context, name string, in *api.IngressSpec, ports []api.PortMapping) ([]string, error) {
	if e.ingress == nil || !ingressemu.Enabled(in) {
		return nil, nil
	}
	switch e.ingress.Mode {
	case "native":
		return e.addNativeIngress(ctx, name, in, ports)
	case "emulate":
		return e.addEmulatedIngress(ctx, name, in, ports)
	default:
		return nil, nil
	}
}

// addNativeIngress registers each resolved host at :80 and :443 pointing at a
// transparent tunnel to the real ingress controller Service, so the browser's
// SNI/Host reach the controller and it does the routing and TLS.
func (e *socks5Conduit) addNativeIngress(ctx context.Context, name string, in *api.IngressSpec, ports []api.PortMapping) ([]string, error) {
	if e.ingress.Controller == nil || e.ingress.Controller.Service == "" {
		return nil, fmt.Errorf("clientconduit: native ingress needs a controller service (none resolved); set conduit.ingress.controller / CORNUS_INGRESS_CONTROLLER, or use emulate mode")
	}
	hosts, _, err := ingressemu.Resolve(in, ports, name, e.ingress.SuffixDomain)
	if err != nil {
		return nil, err
	}
	nd := ingressnative.New(*e.ingress.Controller)
	regs := map[int]socks5.LocalDialer{
		80:  nativeDialer{d: nd, servicePort: nd.HTTPPort()},
		443: nativeDialer{d: nd, servicePort: nd.HTTPSPort()},
	}
	for _, h := range hosts {
		for port, ld := range regs {
			e.router.RegisterLocal(h, port, ld)
		}
	}
	go func() {
		<-ctx.Done()
		for _, h := range hosts {
			for port := range regs {
				e.router.UnregisterLocal(h, port)
			}
		}
	}()
	return hosts, nil
}

// addEmulatedIngress serves a client-side reverse proxy for the ingress and registers
// each host at every port it listens on (:80, plus :443 when the spec requests TLS).
func (e *socks5Conduit) addEmulatedIngress(ctx context.Context, name string, in *api.IngressSpec, ports []api.PortMapping) ([]string, error) {
	var ca *ingressemu.CA
	if in.TLS != nil {
		var err error
		if ca, err = e.emulateCA(); err != nil {
			return nil, err
		}
	}
	em, err := ingressemu.Serve(ingressemu.Config{
		Dialer:       e.d,
		Workload:     name,
		Spec:         in,
		Ports:        ports,
		SuffixDomain: e.ingress.SuffixDomain,
		CA:           ca,
	})
	if err != nil {
		return nil, err
	}
	for _, h := range em.Hosts {
		for port, lis := range em.Listeners {
			e.router.RegisterLocal(h, port, lis)
		}
	}
	go func() {
		<-ctx.Done()
		for _, h := range em.Hosts {
			for port := range em.Listeners {
				e.router.UnregisterLocal(h, port)
			}
		}
		em.Close()
	}()
	return em.Hosts, nil
}

// emulateCA lazily builds (once) the CA that signs emulated-ingress leaf certs:
//   - an explicit caller-supplied CA when both files are set;
//   - else, out of the box, mkcert's locally-trusted root (after `mkcert -install`),
//     so the browser trusts the emulated ingress with no manual step;
//   - else a generated, persisted self-signed CA the user trusts once.
func (e *socks5Conduit) emulateCA() (*ingressemu.CA, error) {
	e.caOnce.Do(func() {
		if e.ingress.CAFile != "" && e.ingress.CAKeyFile != "" {
			e.ca, e.caErr = ingressemu.LoadCA(e.ingress.CAFile, e.ingress.CAKeyFile)
			return
		}
		if ca, err := ingressemu.LoadMkcertCA(); err == nil {
			e.ca = ca
			slog.Info("emulated ingress TLS: signing with mkcert's locally-trusted CA (no manual trust needed)")
			return
		}
		e.ca, e.caErr = ingressemu.LoadOrCreateCA("", "")
		if e.caErr == nil {
			slog.Info("emulated ingress TLS: signing with a self-signed CA; trust it once (or pass --cacert)", "ca", e.ca.CertPath())
		}
	})
	return e.ca, e.caErr
}

// nativeDialer adapts an ingressnative.Dialer + a controller Service port into a
// socks5.LocalDialer: each CONNECT opens a fresh tunnel to the controller.
type nativeDialer struct {
	d           *ingressnative.Dialer
	servicePort int
}

func (n nativeDialer) DialLocal(ctx context.Context) (net.Conn, error) {
	return n.d.DialService(ctx, n.servicePort)
}

// Close clears every alias and published name (deterministic session teardown)
// before tearing the proxy down, so neither outlives the session even if a
// per-service withdrawal was still in flight.
func (e *socks5Conduit) Close() {
	e.router.ClearAliases()
	e.router.ClearLocals()
	e.proxy.Close()
}

// noopConduit exposes nothing (--no-forward-ports).
type noopConduit struct{}

func (noopConduit) Banner() []string { return nil }
func (noopConduit) Add(context.Context, string, []api.PortMapping, ...string) ([]Forward, error) {
	return nil, nil
}

// AddLocal publishes nothing: this conduit deliberately exposes nothing at all.
func (noopConduit) AddLocal(context.Context, string, int, socks5.LocalDialer) (bool, error) {
	return false, nil
}

// AddIngress publishes nothing: this conduit exposes nothing at all.
func (noopConduit) AddIngress(context.Context, string, *api.IngressSpec, []api.PortMapping) ([]string, error) {
	return nil, nil
}
func (noopConduit) Close() {}
