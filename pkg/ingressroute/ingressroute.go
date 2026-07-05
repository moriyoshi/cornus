// Package ingressroute holds the host and path vocabulary shared by every
// realization of a cornus ingress: the Kubernetes backend that creates a real
// Ingress object (pkg/deploy/kubernetes), the client-side emulation reached
// through the SOCKS5 conduit (pkg/ingressemu), and the server-side mux that
// fronts ingress tunnels on the host backends (pkg/ingressmux).
//
// Keeping the derivation in one leaf package is what makes a host derived for
// the mux byte-identical to the one the cluster serves — the sides used to carry
// near-copies of these rules and could disagree on names with runs of separator
// characters. It imports nothing but pkg/api and the standard library, so every
// side is free to depend on it.
package ingressroute

import (
	"fmt"
	"os"
	"strings"

	"cornus/pkg/api"
)

// SanitizeDNS1123 lowercases s and replaces every character outside [a-z0-9-]
// with '-', then trims leading and trailing dashes. Runs are NOT collapsed, so
// "a__b" becomes "a--b": this is the Kubernetes backend's long-standing
// behavior, and it is canonical because it is what ends up in the real cluster
// Ingress object.
func SanitizeDNS1123(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// SanitizeSubdomain sanitizes a dot-separated subdomain (e.g. "web.pr-123") into
// a DNS-1123 host fragment: each label goes through SanitizeDNS1123 and empty
// labels are dropped, so the "<service>.<project>" that compose supplies becomes
// a valid multi-label host prefix.
func SanitizeSubdomain(s string) string {
	var labels []string
	for _, part := range strings.Split(s, ".") {
		if l := SanitizeDNS1123(part); l != "" {
			labels = append(labels, l)
		}
	}
	return strings.Join(labels, ".")
}

// CanonicalHost normalizes a DNS host to its canonical form: trimmed,
// lowercased, and without a trailing dot. DNS is case-insensitive, so every
// host set that must be compared (ingress rules, managed-certificate hosts,
// routing tables) keys on this form.
func CanonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// PathMatches reports whether req satisfies the ingress path rule. Exact
// requires an exact match; Prefix (and ImplementationSpecific) matches on
// path-element boundaries, the way a Kubernetes Ingress Prefix path does — so
// "/api" matches "/api" and "/api/v1" but not "/apiary".
func PathMatches(path, pathType, req string) bool {
	if path == "" || path == "/" {
		return true
	}
	path = strings.TrimRight(path, "/")
	switch pathType {
	case "Exact":
		return req == path
	default: // Prefix / ImplementationSpecific
		return req == path || strings.HasPrefix(req, path+"/")
	}
}

// MatchLen is the specificity of a path rule, for resolving overlapping rules on
// one host: the longest rule wins, the way a real ingress controller resolves
// them. "/" and "" are the least specific.
func MatchLen(path string) int {
	return len(strings.TrimRight(path, "/"))
}

// Defaults are the server-side ingress fallbacks (ultimately Helm values) an
// operator configures once, so a deploy can enable ingress with no
// per-deployment host/class/issuer wiring. A client MAY override each of them in
// its DeploySpec/compose; the server may pin the domain with EnforceDomain so an
// override cannot escape it.
type Defaults struct {
	Domain        string // CORNUS_INGRESS_DOMAIN: base wildcard domain, e.g. "preview.example.com"
	Class         string // CORNUS_INGRESS_CLASS: default IngressClassName
	TLSIssuer     string // CORNUS_INGRESS_TLS_ISSUER: default cert-manager cluster-issuer
	EnforceDomain bool   // CORNUS_INGRESS_ENFORCE_DOMAIN: reject a resolved host outside Domain
}

// DefaultsFromEnv reads the ingress defaults from the server's environment.
func DefaultsFromEnv() Defaults {
	return Defaults{
		Domain:        strings.TrimSpace(os.Getenv("CORNUS_INGRESS_DOMAIN")),
		Class:         strings.TrimSpace(os.Getenv("CORNUS_INGRESS_CLASS")),
		TLSIssuer:     strings.TrimSpace(os.Getenv("CORNUS_INGRESS_TLS_ISSUER")),
		EnforceDomain: envBool(os.Getenv("CORNUS_INGRESS_ENFORCE_DOMAIN")),
	}
}

// WithinDomain reports whether host lies inside domain (the domain apex itself,
// or any subdomain of it). Both are canonicalized first. An empty domain admits
// everything, so callers can apply it unconditionally.
func WithinDomain(host, domain string) bool {
	domain = CanonicalHost(domain)
	if domain == "" {
		return true
	}
	host = CanonicalHost(host)
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// envBool reports whether v is a truthy env value (1/true/yes/on, case-insensitive).
func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Enabled reports whether spec asks for a SERVER-side ingress: a bare Enabled
// flag or any explicit host.
//
// A client-emulated ingress is realized entirely on the client (a reverse proxy
// reached through the conduit), so it reads as "no server ingress" here. That is
// the single gate every server-side ingress path funnels through — the cluster
// Ingress object, its managed TLS secrets, and the server-side mux alike.
func Enabled(in *api.IngressSpec) bool {
	return in != nil && !in.ClientEmulated && (in.Enabled || len(in.Hosts) > 0)
}

// Resolve derives the canonical hosts and the workload container port an ingress
// fronts, from the spec, the deployment's published ports, its name, and the
// server-side defaults.
//
// Explicit hosts win, with "@" mapping to the apex (the base domain itself, no
// "<name>." prefix, following the DNS-zone convention). With no explicit host a
// single "<subdomain|name>.<domain>" is derived. When the server pins its domain
// (Defaults.EnforceDomain), every resolved host must stay within it, so a shared
// ingress front door cannot be made to serve an arbitrary hostname on the
// client's say-so.
//
// Every failure names the knob that fixes it, because this runs server-side
// where the caller cannot see the configuration.
func Resolve(in *api.IngressSpec, ports []api.PortMapping, name string, def Defaults) (hosts []string, targetPort int, err error) {
	if err := in.Validate(); err != nil {
		return nil, 0, err
	}
	if len(ports) == 0 {
		return nil, 0, fmt.Errorf("ingress requires the deployment to publish at least one port")
	}

	// The base domain (client override, else server default) backs both host
	// auto-derivation and the "@" apex token.
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		domain = def.Domain
	}

	for _, h := range in.Hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h == "@" {
			if domain == "" {
				return nil, 0, fmt.Errorf(`ingress: host "@" (apex) requires a base domain: set ingress.domain or configure CORNUS_INGRESS_DOMAIN on the server`)
			}
			hosts = append(hosts, CanonicalHost(domain))
			continue
		}
		hosts = append(hosts, CanonicalHost(h))
	}
	if len(hosts) == 0 {
		if domain == "" {
			return nil, 0, fmt.Errorf("ingress requires a host: set ingress.hosts / ingress.domain, or configure CORNUS_INGRESS_DOMAIN on the server")
		}
		// The subdomain the client supplies (compose sets "<service>.<project>" so
		// projects do not collide on a flat name) is prefixed to the base domain;
		// with none, fall back to the deployment name. Sanitize per label so raw
		// compose service/project names (underscores, mixed case) become DNS-safe.
		sub := strings.TrimSpace(in.Subdomain)
		if sub == "" {
			sub = name
		}
		if sub = SanitizeSubdomain(sub); sub == "" {
			return nil, 0, fmt.Errorf("ingress: cannot derive a host label from subdomain/name %q", name)
		}
		hosts = []string{CanonicalHost(sub + "." + domain)}
	}

	if def.EnforceDomain && def.Domain != "" {
		for _, host := range hosts {
			if !WithinDomain(host, def.Domain) {
				return nil, 0, fmt.Errorf("ingress: host %q violates the server ingress-domain policy (must be within %q)", host, CanonicalHost(def.Domain))
			}
		}
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

// Route resolves spec into the routing-table form: the resolved rule a front
// door needs to serve the deployment, with nothing left to derive.
func Route(in *api.IngressSpec, ports []api.PortMapping, name string, def Defaults) (api.IngressRoute, error) {
	hosts, targetPort, err := Resolve(in, ports, name, def)
	if err != nil {
		return api.IngressRoute{}, err
	}
	return api.IngressRoute{
		Workload:   name,
		Hosts:      hosts,
		Path:       in.Path,
		PathType:   in.PathType,
		TargetPort: targetPort,
		TLS:        in.TLS != nil,
	}, nil
}
