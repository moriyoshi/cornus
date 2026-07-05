// Package ingressemu emulates a Kubernetes-style HTTP(S) ingress on the client side,
// for reaching a workload at its declared ingress host(s) through the SOCKS5 conduit
// on backends with no real ingress controller. It builds an in-process reverse proxy
// (the cornus web BFF pattern: an http.Server on a pkg/memlisten addressless
// listener) that routes by Host/path to the workload's container port through the
// conduit's port-forward dialer, terminating TLS with a generated certificate.
//
// It carries no conduit/router knowledge: pkg/clientconduit wires the listeners this
// package returns into the SOCKS5 router. See Serve.
package ingressemu

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"cornus/pkg/api"
	"cornus/pkg/ingressroute"
	"cornus/pkg/memlisten"
	"cornus/pkg/portfwd"
)

// DefaultSuffixDomain is the base domain used to auto-derive an ingress host when the
// spec names none and gives no domain — the conduit's own service-host suffix stem.
const DefaultSuffixDomain = "cornus.internal"

// httpPort and httpsPort are the listen ports the emulated ingress publishes each
// host under (a browser reaches http:// at :80 and https:// at :443).
const (
	httpPort  = 80
	httpsPort = 443
)

// Enabled reports whether the spec asks for an ingress (mirrors the kubernetes
// backend's ingressEnabled): a bare Enabled flag or any explicit host.
func Enabled(in *api.IngressSpec) bool {
	return in != nil && (in.Enabled || len(in.Hosts) > 0)
}

// Resolve derives the ingress host(s) and the workload container port the emulated
// ingress fronts, mirroring the kubernetes backend's semantics: explicit Hosts win
// (with "@" mapping to the base domain apex); with none, a single
// "<subdomain|name>.<domain>" is derived, where domain is the spec's Domain override
// else suffixDomain (default DefaultSuffixDomain). The target port is spec.Port when
// it names a published container port, else the first published port.
func Resolve(in *api.IngressSpec, ports []api.PortMapping, name, suffixDomain string) (hosts []string, targetPort int, err error) {
	if err := in.Validate(); err != nil {
		return nil, 0, err
	}
	if len(ports) == 0 {
		return nil, 0, fmt.Errorf("ingress requires the deployment to publish at least one port")
	}
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		domain = strings.TrimSpace(suffixDomain)
	}
	if domain == "" {
		domain = DefaultSuffixDomain
	}
	for _, h := range in.Hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h == "@" {
			hosts = append(hosts, domain)
			continue
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		sub := strings.TrimSpace(in.Subdomain)
		if sub == "" {
			sub = name
		}
		sub = ingressroute.SanitizeSubdomain(sub)
		if sub == "" {
			return nil, 0, fmt.Errorf("ingress: cannot derive a host label from subdomain/name %q", name)
		}
		hosts = []string{sub + "." + domain}
	}

	targetPort = ports[0].Container
	if in.Port != 0 {
		found := false
		for _, p := range ports {
			if p.Container == in.Port {
				found = true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf("ingress: port %d is not among the deployment's published container ports", in.Port)
		}
		targetPort = in.Port
	}
	return hosts, targetPort, nil
}

// newBridgeProxy builds the reverse proxy that fronts one workload port.
//
// Split out of Handler so the TRANSPORT is reachable from a test. Handler returns
// a closure over this value, so nothing outside could observe which transport it
// was built with — and the bound that transport carries is the whole point of
// ingressroute.BridgeTransport: every idle connection holds a live port-forward
// bridge open (a goroutine plus a real backend resource), so an unbounded pool
// accumulates one per workload port and never reclaims them. Reverting this call
// site to an inline &http.Transport{DialContext: ...} used to leave every test
// green; TestBridgeProxyUsesTheBoundedTransport now reads the fields back.
func newBridgeProxy(d portfwd.Dialer, workload string, targetPort int) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Scheme = "http"
			// A synthetic upstream authority: the Transport ignores the address and
			// dials the workload directly, but net/http still needs a URL host.
			pr.Out.URL.Host = fmt.Sprintf("%s:%d", workload, targetPort)
			// Preserve the ingress hostname the client used, so the app sees it.
			pr.Out.Host = pr.In.Host
		},
		// Bounded pool: every idle connection holds a live port-forward bridge open.
		// See ingressroute.BridgeTransport, shared with the server-side mux.
		Transport: ingressroute.BridgeTransport(func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.PortForward(ctx, workload, targetPort, "tcp")
		}),
		// The emulated ingress terminates HTTP(S), so the client here is always
		// HTTP-aware — when the upstream cannot be reached, answer with an
		// informative 502 rather than net/http's default empty-body 502 (which a
		// browser renders as a blank page and looks like the connection just
		// dropped). The common cause is the workload not being up/ready yet, so say
		// so and name the exact target the conduit tried to reach.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "502 Bad Gateway\n\ncornus emulated ingress could not reach workload %q on container port %d: %v\n\nThe service may not be running or ready yet.\n", workload, targetPort, err)
		},
	}
}

// Handler builds the reverse-proxy handler for one emulated ingress: it validates the
// request Host against the ingress hosts (defense in depth) and the path against the
// ingress path/pathType, then reverse-proxies to the workload's container port
// through d.PortForward. path defaults to "/" and pathType to "Prefix".
func Handler(d portfwd.Dialer, workload string, targetPort int, path, pathType string, hosts []string) http.Handler {
	allowed := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		allowed[strings.ToLower(h)] = true
	}
	proxy := newBridgeProxy(d, workload, targetPort)
	if path == "" {
		path = "/"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[strings.ToLower(strings.Trim(host, "[]"))] {
			http.Error(w, "unrecognized Host header", http.StatusMisdirectedRequest)
			return
		}
		if !ingressroute.PathMatches(path, pathType, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// Config describes one emulated ingress to serve.
type Config struct {
	// Dialer reaches the workload's container port (the conduit's port-forward dialer).
	Dialer portfwd.Dialer
	// Workload is the deployment name the ingress fronts.
	Workload string
	// Spec is the ingress request (hosts, path, port, TLS).
	Spec *api.IngressSpec
	// Ports are the workload's published ports (for target-port resolution).
	Ports []api.PortMapping
	// SuffixDomain backs host auto-derivation when the spec names no host/domain.
	SuffixDomain string
	// CA mints the TLS leaf certificate served on :443 when the spec requests TLS.
	// Required only when Spec.TLS != nil.
	CA *CA
	// Certificates are user-supplied server key pairs selected by TLS SNI.
	Certificates []CertificateSource
}

// Mux multiplexes emulated ingresses that share a host:port onto a single listener,
// dispatching each request to the backend whose path rule is the longest match — the
// way a real Kubernetes ingress resolves overlapping paths on one host. Without it,
// two ingresses on the same host would each publish their own listener and the second
// would take the subject from the first, so only one path ever worked and which one
// depended on registration order.
//
// The Mux owns the per-host:port listeners and TLS termination but stays ignorant of
// what routing means: on the first rule for a host:port it calls register with the
// freshly created listener, and register returns the closure that withdraws exactly
// that publication. Returning the undo — rather than taking a separate unregister
// keyed on (host, port) — is what lets the caller withdraw ITS OWN claim when several
// publishers share a subject, which a key alone cannot identify.
type Mux struct {
	register func(host string, port int, lis *memlisten.Listener) (withdraw func())

	mu      sync.RWMutex
	entries map[string]*muxEntry
}

// ingressRule is one path rule on a shared host:port listener: the backend reverse
// proxy (handler) reached when a request satisfies (path, pathType).
type ingressRule struct {
	path     string
	pathType string
	handler  http.Handler
}

// muxEntry is the shared state for one host:port — a single listener/server whose
// handler dispatches each request to the longest-matching rule — plus the live rule
// set. Keyed in Mux.entries by "host:port".
type muxEntry struct {
	host     string
	port     int
	listener *memlisten.Listener
	server   *http.Server
	rules    []*ingressRule
	// withdraw undoes this entry's publication, as returned by register. Held here so
	// teardown withdraws the exact claim this entry created rather than whatever is
	// currently published at the same host:port.
	withdraw func()
}

// NewMux returns a Mux that publishes its per-host:port listeners through register,
// which returns the closure withdrawing that publication. register may be nil for a
// standalone Mux, and may itself return nil when there is nothing to undo.
func NewMux(register func(host string, port int, lis *memlisten.Listener) (withdraw func())) *Mux {
	if register == nil {
		register = func(string, int, *memlisten.Listener) func() { return nil }
	}
	return &Mux{register: register, entries: map[string]*muxEntry{}}
}

// Add resolves cfg and registers its path rule on every host it fronts, at :80 and —
// when cfg.Spec.TLS is set — :443. It returns the resolved hosts and a cleanup that
// withdraws exactly the rules this call added, closing any listener whose last rule it
// removes. cleanup is idempotent. Safe to call concurrently.
//
// The first TLS ingress on a given host:443 establishes that listener's certificate;
// later ingresses sharing the host reuse it (a shared host has a single TLS identity,
// as it must on the wire).
func (m *Mux) Add(cfg Config) (hosts []string, cleanup func(), err error) {
	hosts, targetPort, err := Resolve(cfg.Spec, cfg.Ports, cfg.Workload, cfg.SuffixDomain)
	if err != nil {
		return nil, nil, err
	}
	handler := Handler(cfg.Dialer, cfg.Workload, targetPort, cfg.Spec.Path, cfg.Spec.PathType, hosts)

	// Ports this rule listens on, each with the TLS config its listener terminates
	// with (nil for plain HTTP). Build the TLS config up front so a cert error fails
	// Add before any listener is created.
	type portTLS struct {
		port      int
		tlsConfig *tls.Config
	}
	portsTLS := []portTLS{{httpPort, nil}}
	if cfg.Spec.TLS != nil {
		tlsConfig, err := buildTLSConfig(cfg, hosts)
		if err != nil {
			return nil, nil, err
		}
		portsTLS = append(portsTLS, portTLS{httpsPort, tlsConfig})
	}

	rule := &ingressRule{path: cfg.Spec.Path, pathType: cfg.Spec.PathType, handler: handler}

	type placement struct {
		host string
		port int
	}
	var added []placement
	m.mu.Lock()
	for _, h := range hosts {
		for _, pt := range portsTLS {
			m.attachLocked(h, pt.port, rule, pt.tlsConfig)
			added = append(added, placement{h, pt.port})
		}
	}
	m.mu.Unlock()

	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			m.mu.Lock()
			for _, pl := range added {
				m.detachLocked(pl.host, pl.port, rule)
			}
			m.mu.Unlock()
		})
	}
	return hosts, cleanup, nil
}

// attachLocked adds rule to the host:port entry, creating (and publishing) the
// listener/server on the first rule. Callers hold m.mu.
func (m *Mux) attachLocked(host string, port int, rule *ingressRule, tlsConfig *tls.Config) {
	key := entryKey(host, port)
	e := m.entries[key]
	if e == nil {
		lis := memlisten.New(fmt.Sprintf("%s:%d", host, port))
		e = &muxEntry{host: host, port: port, listener: lis}
		e.server = &http.Server{Handler: m.entryHandler(e)}
		m.entries[key] = e
		if tlsConfig != nil {
			e.server.TLSConfig = tlsConfig
			go func() { _ = e.server.ServeTLS(lis, "", "") }()
		} else {
			go func() { _ = e.server.Serve(lis) }()
		}
		e.withdraw = m.register(host, port, lis)
	}
	e.rules = append(e.rules, rule)
}

// detachLocked removes rule from the host:port entry, closing (and withdrawing) the
// listener/server once its last rule is gone. Callers hold m.mu.
func (m *Mux) detachLocked(host string, port int, rule *ingressRule) {
	key := entryKey(host, port)
	e := m.entries[key]
	if e == nil {
		return
	}
	kept := make([]*ingressRule, 0, len(e.rules))
	for _, r := range e.rules {
		if r != rule {
			kept = append(kept, r)
		}
	}
	e.rules = kept
	if len(e.rules) == 0 {
		delete(m.entries, key)
		_ = e.server.Close()
		_ = e.listener.Close()
		// Withdraw the exact publication this entry created. Keying the withdrawal on
		// (host, port) instead would remove whatever claim is currently serving the
		// subject, which when several publishers share it is somebody else's.
		if e.withdraw != nil {
			e.withdraw()
		}
	}
}

// entryHandler dispatches each request on a shared host:port listener to the
// longest-matching rule's backend, 404-ing when none matches.
func (m *Mux) entryHandler(e *muxEntry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		h := longestMatch(e.rules, r.URL.Path)
		m.mu.RUnlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// entryKey identifies a shared listener by host:port. Host is lowercased so
// case-variant spellings of the same name converge on one listener (DNS is
// case-insensitive), matching how Handler gates the Host header.
func entryKey(host string, port int) string {
	return fmt.Sprintf("%s:%d", strings.ToLower(host), port)
}

// longestMatch returns the handler of the rule that best matches reqPath, mirroring a
// Kubernetes ingress: among all rules whose path matches, the one with the longest
// path wins, and an Exact rule beats a Prefix rule of equal length. Returns nil when
// no rule matches.
func longestMatch(rules []*ingressRule, reqPath string) http.Handler {
	var best *ingressRule
	bestLen := -1
	for _, r := range rules {
		if !ingressroute.PathMatches(r.path, r.pathType, reqPath) {
			continue
		}
		l := ingressroute.MatchLen(r.path)
		switch {
		case l > bestLen:
			best, bestLen = r, l
		case l == bestLen && r.pathType == "Exact" && best.pathType != "Exact":
			best = r
		}
	}
	if best == nil {
		return nil
	}
	return best.handler
}

// buildTLSConfig assembles the TLS config that terminates HTTPS for hosts: a leaf
// minted from cfg.CA, overridable per SNI by any user-supplied certificate.
func buildTLSConfig(cfg Config, hosts []string) (*tls.Config, error) {
	selector, err := loadCertificateSelector(cfg.Certificates)
	if err != nil {
		return nil, err
	}
	if cfg.CA == nil && len(cfg.Certificates) == 0 {
		return nil, fmt.Errorf("ingressemu: ingress requests TLS but no CA or certificate was provided")
	}
	var leaf tls.Certificate
	if cfg.CA != nil {
		if leaf, err = cfg.CA.Leaf(hosts); err != nil {
			return nil, err
		}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	tlsConfig.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if cert := selector.certificate(hello.ServerName); cert != nil {
			return cert, nil
		}
		if cfg.CA == nil {
			return nil, fmt.Errorf("ingressemu: no certificate matches SNI %q", hello.ServerName)
		}
		return &leaf, nil
	}
	return tlsConfig, nil
}

// Emulated is a running emulated ingress: one addressless memlisten listener per
// published port (:80 always, :443 when TLS is requested), each fronting the reverse
// proxy. The caller registers each Hosts × Listeners pair in the SOCKS5 router.
type Emulated struct {
	// Hosts are the resolved ingress hostnames.
	Hosts []string
	// Listeners maps a listen port to the addressless listener serving that port; a
	// *memlisten.Listener satisfies socks5.LocalDialer.
	Listeners map[int]*memlisten.Listener
	close     func()
}

// Close stops serving and releases the listeners. Idempotent.
func (e *Emulated) Close() {
	if e.close != nil {
		e.close()
	}
}

// Serve resolves cfg.Spec and starts a standalone emulated ingress for a single
// workload: a plain HTTP reverse proxy on :80 and, when cfg.Spec.TLS is set, a
// TLS-terminating one on :443 (using a leaf minted from cfg.CA). It returns the
// running Emulated for the caller to publish and, later, Close.
//
// Callers that publish several ingresses which may share a host should instead hold a
// single Mux and call Add per ingress, so overlapping paths resolve by longest match
// rather than clobbering one another. Serve is a thin single-ingress adapter over Mux.
func Serve(cfg Config) (*Emulated, error) {
	em := &Emulated{Listeners: map[int]*memlisten.Listener{}}
	mux := NewMux(func(_ string, port int, lis *memlisten.Listener) func() {
		em.Listeners[port] = lis
		return nil
	})
	hosts, cleanup, err := mux.Add(cfg)
	if err != nil {
		return nil, err
	}
	em.Hosts = hosts
	em.close = cleanup
	return em, nil
}
