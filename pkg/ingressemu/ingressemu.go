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

	"cornus/pkg/api"
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
		sub = sanitizeSubdomain(sub)
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

// Handler builds the reverse-proxy handler for one emulated ingress: it validates the
// request Host against the ingress hosts (defense in depth) and the path against the
// ingress path/pathType, then reverse-proxies to the workload's container port
// through d.PortForward. path defaults to "/" and pathType to "Prefix".
func Handler(d portfwd.Dialer, workload string, targetPort int, path, pathType string, hosts []string) http.Handler {
	allowed := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		allowed[strings.ToLower(h)] = true
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Scheme = "http"
			// A synthetic upstream authority: the Transport ignores the address and
			// dials the workload directly, but net/http still needs a URL host.
			pr.Out.URL.Host = fmt.Sprintf("%s:%d", workload, targetPort)
			// Preserve the ingress hostname the client used, so the app sees it.
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.PortForward(ctx, workload, targetPort, "tcp")
			},
		},
	}
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
		if !pathMatches(path, pathType, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// pathMatches reports whether req satisfies the ingress path rule. Exact requires an
// exact match; Prefix (and ImplementationSpecific) matches on path-element
// boundaries, like a Kubernetes Ingress Prefix path.
func pathMatches(p, pathType, req string) bool {
	if p == "" || p == "/" {
		return true
	}
	p = strings.TrimRight(p, "/")
	switch pathType {
	case "Exact":
		return req == p
	default: // Prefix / ImplementationSpecific
		return req == p || strings.HasPrefix(req, p+"/")
	}
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
	servers   []*http.Server
}

// Close stops serving and releases the listeners. Idempotent.
func (e *Emulated) Close() {
	for _, srv := range e.servers {
		_ = srv.Close()
	}
	for _, lis := range e.Listeners {
		_ = lis.Close()
	}
}

// Serve resolves cfg.Spec and starts the emulated ingress: a plain HTTP reverse proxy
// on :80 and, when cfg.Spec.TLS is set, a TLS-terminating one on :443 (using a leaf
// minted from cfg.CA). It returns the running Emulated for the caller to publish and,
// later, Close.
func Serve(cfg Config) (*Emulated, error) {
	hosts, targetPort, err := Resolve(cfg.Spec, cfg.Ports, cfg.Workload, cfg.SuffixDomain)
	if err != nil {
		return nil, err
	}
	handler := Handler(cfg.Dialer, cfg.Workload, targetPort, cfg.Spec.Path, cfg.Spec.PathType, hosts)

	em := &Emulated{Hosts: hosts, Listeners: map[int]*memlisten.Listener{}}

	lisHTTP := memlisten.New(hosts[0] + ":80")
	srvHTTP := &http.Server{Handler: handler}
	em.Listeners[httpPort] = lisHTTP
	em.servers = append(em.servers, srvHTTP)
	go func() { _ = srvHTTP.Serve(lisHTTP) }()

	if cfg.Spec.TLS != nil {
		if cfg.CA == nil {
			em.Close()
			return nil, fmt.Errorf("ingressemu: ingress requests TLS but no CA was provided")
		}
		leaf, err := cfg.CA.Leaf(hosts)
		if err != nil {
			em.Close()
			return nil, err
		}
		lisTLS := memlisten.New(hosts[0] + ":443")
		srvTLS := &http.Server{
			Handler:   handler,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{leaf}},
		}
		em.Listeners[httpsPort] = lisTLS
		em.servers = append(em.servers, srvTLS)
		go func() { _ = srvTLS.ServeTLS(lisTLS, "", "") }()
	}
	return em, nil
}

// sanitizeSubdomain maps an arbitrary subdomain/name into dot-separated DNS-1123
// labels: each label lowercased, non-alphanumerics collapsed to '-', edges trimmed.
// Empty labels are dropped. Mirrors the kubernetes backend's sanitizeSubdomain so
// derived hosts match.
func sanitizeSubdomain(s string) string {
	labels := strings.Split(s, ".")
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		var b strings.Builder
		lastDash := false
		for _, r := range strings.ToLower(label) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
				lastDash = false
			default:
				if b.Len() > 0 && !lastDash {
					b.WriteByte('-')
					lastDash = true
				}
			}
		}
		l := strings.Trim(b.String(), "-")
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, ".")
}
