// Package ingressmux is the server-side realization of a cornus ingress: a
// host/path routing table over the deployments that requested one, and an
// http.Handler that proxies each request to the matching workload port through a
// port-forward dialer.
//
// It is what gives ingress semantics to the backends that have no ingress
// controller of their own (dockerhost, containerd, bare, incus), and it is what
// an ingress tunnel fronts on those backends. On Kubernetes the real controller
// plays this role instead, so the mux is only the fallback there.
//
// It deliberately knows nothing about tunnels or backends: pkg/server owns the
// table's lifecycle (deploys write to it, deletes withdraw from it) and decides
// what to expose it through.
package ingressmux

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/ingressroute"
	"cornus/pkg/portfwd"
)

// Table is the live host/path routing state: which workload port serves which
// host and path. It is keyed by deployment name so a redeploy replaces exactly
// that deployment's rules and a delete withdraws them, and it is safe for
// concurrent use.
type Table struct {
	mu sync.RWMutex
	// byWorkload is the authoritative state — every other map is derived from it.
	byWorkload map[string][]api.IngressRoute
	// byHost indexes the rules serving each canonical host.
	byHost map[string][]api.IngressRoute
	// aliases map an extra accepted hostname onto a canonical ingress host. A
	// tunnel registers its public hostname here so requests arriving with the
	// tunnel's Host header route to the right workload while the app still sees
	// the hostname the client actually used.
	aliases map[string]string
}

// NewTable returns an empty routing table.
func NewTable() *Table {
	return &Table{
		byWorkload: map[string][]api.IngressRoute{},
		byHost:     map[string][]api.IngressRoute{},
		aliases:    map[string]string{},
	}
}

// Set installs workload's routes, replacing any it previously had. Passing no
// routes is equivalent to Remove.
func (t *Table) Set(workload string, routes []api.IngressRoute) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(routes) == 0 {
		delete(t.byWorkload, workload)
	} else {
		normalized := make([]api.IngressRoute, 0, len(routes))
		for _, r := range routes {
			r.Workload = workload
			hosts := make([]string, 0, len(r.Hosts))
			for _, h := range r.Hosts {
				if h = ingressroute.CanonicalHost(h); h != "" {
					hosts = append(hosts, h)
				}
			}
			if len(hosts) == 0 {
				continue
			}
			r.Hosts = hosts
			normalized = append(normalized, r)
		}
		if len(normalized) == 0 {
			delete(t.byWorkload, workload)
		} else {
			t.byWorkload[workload] = normalized
		}
	}
	t.reindexLocked()
}

// Remove withdraws workload's routes.
func (t *Table) Remove(workload string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byWorkload, workload)
	t.reindexLocked()
}

// reindexLocked rebuilds the host index from byWorkload. Rebuilding wholesale
// rather than patching keeps the index provably consistent with the workload map
// — the table is small (one entry per ingress-enabled deployment) and changes
// only on deploy/delete, so there is nothing to gain from incremental updates.
func (t *Table) reindexLocked() {
	byHost := make(map[string][]api.IngressRoute, len(t.byHost))
	for _, routes := range t.byWorkload {
		for _, r := range routes {
			for _, h := range r.Hosts {
				byHost[h] = append(byHost[h], r)
			}
		}
	}
	t.byHost = byHost
}

// HostAlias makes alias resolve to target's rules. An empty target removes the
// alias. Both are canonicalized.
func (t *Table) HostAlias(alias, target string) {
	alias = ingressroute.CanonicalHost(alias)
	if alias == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if target = ingressroute.CanonicalHost(target); target == "" {
		delete(t.aliases, alias)
		return
	}
	t.aliases[alias] = target
}

// Hosts returns every canonical host the table serves, sorted. Aliases are not
// included: they are additional names for these hosts, not routes of their own.
func (t *Table) Hosts() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	hosts := make([]string, 0, len(t.byHost))
	for h := range t.byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// HostsFor returns the canonical hosts workload serves, sorted.
func (t *Table) HostsFor(workload string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var hosts []string
	seen := map[string]bool{}
	for _, r := range t.byWorkload[workload] {
		for _, h := range r.Hosts {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	sort.Strings(hosts)
	return hosts
}

// Workloads returns the names of every workload with routes, sorted.
func (t *Table) Workloads() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.byWorkload))
	for n := range t.byWorkload {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Has reports whether workload currently has routes.
func (t *Table) Has(workload string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byWorkload[workload]) > 0
}

// Empty reports whether the table routes nothing.
func (t *Table) Empty() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byWorkload) == 0
}

// Lookup resolves host and path to the workload port that serves them. Among the
// rules on a host the longest matching path wins, and an Exact rule beats a
// Prefix rule of equal length — the way a real ingress controller resolves
// overlapping paths. hostKnown distinguishes "no such host here" (answer 421)
// from "this host has no rule for that path" (answer 404).
func (t *Table) Lookup(host, path string) (route api.IngressRoute, hostKnown, ok bool) {
	host = ingressroute.CanonicalHost(stripPort(host))
	t.mu.RLock()
	defer t.mu.RUnlock()
	rules, hostKnown := t.byHost[host]
	if !hostKnown {
		if target, aliased := t.aliases[host]; aliased {
			rules, hostKnown = t.byHost[target]
		}
	}
	if !hostKnown {
		return api.IngressRoute{}, false, false
	}
	best := -1
	for _, r := range rules {
		if !ingressroute.PathMatches(r.Path, r.PathType, path) {
			continue
		}
		l := ingressroute.MatchLen(r.Path)
		switch {
		case l > best:
			route, best, ok = r, l, true
		case l == best && r.PathType == "Exact" && route.PathType != "Exact":
			route = r
		}
	}
	return route, true, ok
}

// stripPort drops the ":port" a Host header may carry. It tolerates bracketed
// IPv6 literals and hosts with no port.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// Proxy is the http.Handler over a Table: it resolves each request's Host and
// path to a workload port and reverse-proxies to it through the dialer.
type Proxy struct {
	table  *Table
	dialer portfwd.Dialer
	proxy  *httputil.ReverseProxy
}

// idleBridgeTimeout is how long an idle pooled connection — and therefore the
// port-forward bridge behind it — is kept before being reclaimed. It matches
// http.DefaultTransport's IdleConnTimeout.
const idleBridgeTimeout = 90 * time.Second

// routeKey carries the resolved route from the handler down into the reverse
// proxy's Rewrite and Transport, which is how one shared proxy serves every
// workload in the table.
type routeKey struct{}

// NewProxy returns the handler serving t's routes, dialing workload ports
// through d.
func NewProxy(t *Table, d portfwd.Dialer) *Proxy {
	p := &Proxy{table: t, dialer: d}
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			r, _ := pr.In.Context().Value(routeKey{}).(api.IngressRoute)
			pr.SetXForwarded()
			pr.Out.URL.Scheme = "http"
			// A synthetic upstream authority: the Transport ignores the address and
			// dials the workload through the port-forward bridge, but net/http still
			// needs a URL host — and it doubles as the connection-pool key, so
			// pooling stays per-workload-port.
			pr.Out.URL.Host = fmt.Sprintf("%s:%d", r.Workload, r.TargetPort)
			// Preserve the hostname the client used, so the app builds redirects and
			// cookies against the name that actually reached it.
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				r, ok := ctx.Value(routeKey{}).(api.IngressRoute)
				if !ok {
					return nil, fmt.Errorf("ingressmux: no route on the dial context")
				}
				return d.PortForward(ctx, r.Workload, r.TargetPort, "tcp")
			},
			// An idle connection here is NOT the cheap thing it is for an ordinary
			// HTTP client: every pooled connection holds a live port-forward bridge
			// open — a goroutine plus a real backend resource (a Kubernetes SPDY
			// stream, a Docker hijack, a containerd exec). Left unbounded (the
			// zero values mean "never expire" and "unlimited") they accumulate one
			// per workload port and are never reclaimed, so bound them the way
			// http.DefaultTransport does.
			IdleConnTimeout:     idleBridgeTimeout,
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 4,
		},
		// The front door terminates HTTP, so the client here is always HTTP-aware.
		// Answer an unreachable workload with an informative 502 rather than
		// net/http's empty-body default, which a browser renders as a blank page
		// that looks like the connection simply dropped.
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			r, _ := req.Context().Value(routeKey{}).(api.IngressRoute)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "502 Bad Gateway\n\ncornus ingress could not reach workload %q on container port %d: %v\n\nThe service may not be running or ready yet.\n", r.Workload, r.TargetPort, err)
		},
	}
	return p
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, hostKnown, ok := p.table.Lookup(r.Host, r.URL.Path)
	if !hostKnown {
		// 421 is the honest answer: this front door does not serve that name. It
		// also keeps a shared ingress from being probed for what else it fronts.
		http.Error(w, "unrecognized Host header", http.StatusMisdirectedRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	p.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeKey{}, route)))
}
