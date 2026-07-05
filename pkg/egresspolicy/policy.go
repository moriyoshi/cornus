// Package egresspolicy is the shared routing engine for client-side egress. It
// compiles an api.EgressSpec into a Policy that maps a destination to one of four
// routes — Client (relay to the client-side network), Gateway (relay to a durable
// egress-gateway node), Cluster (egress directly from the workload's own network,
// no relay), or Deny (drop). It is deliberately dependency-free so the caretaker
// (which decides the route per connection), the server (which re-checks the route
// fail-closed) and the client (which applies a final guard) all evaluate the SAME
// policy identically.
//
// Two policy forms sit behind the one Policy interface: the declarative rule list
// (rulePolicy, here) and — added separately — a PAC-style script evaluator. Both
// come out of Compile.
package egresspolicy

import (
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"

	"cornus/pkg/api"
)

// The four routes a destination can be sent to. A Policy.Route call returns one
// of these.
const (
	RouteClient  = "client"  // relay to the client-side network (the caller / VPN vantage point)
	RouteGateway = "gateway" // relay to a durable egress-gateway node (for --detach)
	RouteCluster = "cluster" // egress directly from the workload's own network, no relay
	RouteDeny    = "deny"    // drop the connection
)

// ErrScriptUnsupported is returned by Compile when a spec carries a Script but
// this build has no script evaluator linked. The scriptPolicy implementation
// replaces it.
var ErrScriptUnsupported = errors.New("egresspolicy: script policy is not supported")

// Dest is a resolved egress destination. Host is the destination hostname when the
// app dialed a name (may be empty for a raw-IP dial); IP is the destination
// address when known (may be nil for a name not yet resolved); Port is the TCP/UDP
// port; Proto is "tcp" (default) or "udp".
type Dest struct {
	Host  string
	IP    net.IP
	Port  int
	Proto string
}

// Policy decides where a destination's traffic egresses. Route returns one of the
// Route* constants. A non-nil error means the policy could not be evaluated; every
// caller treats that as fail-closed (Deny).
type Policy interface {
	Route(Dest) (string, error)
}

// ValidRoute reports whether r is one of the four routes.
func ValidRoute(r string) bool {
	switch r {
	case RouteClient, RouteGateway, RouteCluster, RouteDeny:
		return true
	}
	return false
}

// Compile builds a Policy from a spec. A Script (when set) supersedes Rules and
// selects the script evaluator; otherwise the declarative rule list is used.
func Compile(spec api.EgressSpec) (Policy, error) {
	def := spec.Default
	if def == "" {
		def = RouteCluster
	}
	if !ValidRoute(def) {
		return nil, fmt.Errorf("egresspolicy: invalid default route %q", spec.Default)
	}
	if strings.TrimSpace(spec.Script) != "" {
		return compileScript(spec.Script, def)
	}
	return compileRules(spec.Rules, def)
}

// compileRules turns the declarative rules into a rulePolicy. It is a package
// function (not a method) so both Compile and tests can build one directly.
func compileRules(rules []api.EgressRule, def string) (*rulePolicy, error) {
	p := &rulePolicy{def: def}
	for i, r := range rules {
		if !ValidRoute(r.Route) {
			return nil, fmt.Errorf("egresspolicy: rule %d (%q): invalid route %q", i, r.Pattern, r.Route)
		}
		cr, err := compileRule(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("egresspolicy: rule %d: %w", i, err)
		}
		cr.route = r.Route
		p.rules = append(p.rules, cr)
	}
	return p, nil
}

// rulePolicy evaluates an ordered rule list, first match wins, falling back to a
// default route. It carries no live state and is safe for concurrent use.
type rulePolicy struct {
	rules []compiledRule
	def   string
}

func (p *rulePolicy) Route(d Dest) (string, error) {
	for _, r := range p.rules {
		if r.matches(d) {
			return r.route, nil
		}
	}
	return p.def, nil
}

// compiledRule is one parsed pattern plus its route. Exactly one of cidr / ip /
// hostGlob is set for the host part; port==0 means "any port".
type compiledRule struct {
	cidr     *net.IPNet
	ip       net.IP
	hostGlob string // shell-style glob over the hostname (lower-cased); "" when ip/cidr set
	port     int
	route    string
}

func (r compiledRule) matches(d Dest) bool {
	if r.port != 0 && r.port != d.Port {
		return false
	}
	switch {
	case r.cidr != nil:
		if d.IP != nil && r.cidr.Contains(d.IP) {
			return true
		}
		// A host that is itself an IP literal (raw-IP dial carried as Host).
		if ip := net.ParseIP(d.Host); ip != nil && r.cidr.Contains(ip) {
			return true
		}
		return false
	case r.ip != nil:
		if d.IP != nil && r.ip.Equal(d.IP) {
			return true
		}
		if ip := net.ParseIP(d.Host); ip != nil && r.ip.Equal(ip) {
			return true
		}
		return false
	default:
		host := strings.ToLower(strings.TrimSuffix(d.Host, "."))
		if host == "" && d.IP != nil {
			host = d.IP.String()
		}
		ok, err := path.Match(r.hostGlob, host)
		return err == nil && ok
	}
}

// compileRule parses a pattern into host + optional port. Grammar:
//
//	host                 e.g. "*.internal", "api.example.com"
//	host:PORT            e.g. "api.example.com:443"
//	CIDR                 e.g. "10.0.0.0/8", "fe80::/10"
//	CIDR:PORT            e.g. "10.0.0.0/8:5432"
//	[IPv6-or-CIDR]:PORT  e.g. "[fe80::/10]:443"  (brackets required to port an IPv6 host)
//
// The host part is a CIDR (contains "/"), an IP literal, or a shell-style glob.
func compileRule(pattern string) (compiledRule, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return compiledRule{}, errors.New("empty pattern")
	}
	hostPart, portPart, err := splitPattern(pattern)
	if err != nil {
		return compiledRule{}, err
	}
	var cr compiledRule
	if portPart != "" {
		port, err := strconv.Atoi(portPart)
		if err != nil || port < 1 || port > 65535 {
			return compiledRule{}, fmt.Errorf("invalid port %q in %q", portPart, pattern)
		}
		cr.port = port
	}
	switch {
	case strings.Contains(hostPart, "/"):
		_, ipnet, err := net.ParseCIDR(hostPart)
		if err != nil {
			return compiledRule{}, fmt.Errorf("invalid CIDR %q: %w", hostPart, err)
		}
		cr.cidr = ipnet
	default:
		if ip := net.ParseIP(hostPart); ip != nil {
			cr.ip = ip
		} else {
			// A bare "*" (any host) or any glob. Validate it as a path.Match pattern.
			glob := strings.ToLower(hostPart)
			if _, err := path.Match(glob, ""); err != nil {
				return compiledRule{}, fmt.Errorf("invalid host pattern %q: %w", hostPart, err)
			}
			cr.hostGlob = glob
		}
	}
	return cr, nil
}

// splitPattern separates the optional trailing :PORT from the host part. To keep
// unbracketed IPv6 literals/CIDRs unambiguous, a port may be attached to an IPv6
// host only via brackets ("[::1]:443"); an unbracketed value with more than one
// colon is treated as an IPv6 host with no port.
func splitPattern(pattern string) (host, port string, err error) {
	if strings.HasPrefix(pattern, "[") {
		end := strings.IndexByte(pattern, ']')
		if end < 0 {
			return "", "", fmt.Errorf("unterminated '[' in %q", pattern)
		}
		host = pattern[1:end]
		rest := pattern[end+1:]
		switch {
		case rest == "":
			return host, "", nil
		case strings.HasPrefix(rest, ":"):
			return host, rest[1:], nil
		default:
			return "", "", fmt.Errorf("unexpected %q after ']' in %q", rest, pattern)
		}
	}
	switch strings.Count(pattern, ":") {
	case 0:
		return pattern, "", nil
	case 1:
		i := strings.IndexByte(pattern, ':')
		return pattern[:i], pattern[i+1:], nil
	default:
		// Unbracketed IPv6 literal/CIDR: whole thing is the host, no port.
		return pattern, "", nil
	}
}

// NoProxyPatterns returns the destination patterns that should BYPASS a forward
// proxy — the ones a rule routes to "cluster" (reach directly) — for building a
// NO_PROXY value in env mode. Deny/relay routes cannot be expressed through proxy
// env vars, so they are omitted; the caller may still merge an explicit NO_PROXY.
func NoProxyPatterns(spec api.EgressSpec) []string {
	var out []string
	for _, r := range spec.Rules {
		if r.Route == RouteCluster {
			out = append(out, r.Pattern)
		}
	}
	return out
}

// ProxyEnv builds the proxy environment variables that point an app container at a
// caretaker forward proxy listening on loopback port. HTTP_PROXY/HTTPS_PROXY use the
// http:// scheme (the caretaker is a full HTTP proxy — the most portable form) and
// ALL_PROXY uses socks5h:// (SOCKS with terminus-side DNS, correct for an air-gapped
// pod). Both upper- and lower-case spellings are set; NO_PROXY keeps loopback and
// any cluster-routed destinations off the proxy. Backend-agnostic (returns a map) so
// every backend renders it into its own env form.
func ProxyEnv(spec api.EgressSpec, port int) map[string]string {
	httpProxy := fmt.Sprintf("http://127.0.0.1:%d", port)
	socks := fmt.Sprintf("socks5h://127.0.0.1:%d", port)
	noProxy := append([]string{"localhost", "127.0.0.1", "::1"}, NoProxyPatterns(spec)...)
	np := strings.Join(noProxy, ",")
	return map[string]string{
		"HTTP_PROXY": httpProxy, "http_proxy": httpProxy,
		"HTTPS_PROXY": httpProxy, "https_proxy": httpProxy,
		"ALL_PROXY": socks, "all_proxy": socks,
		"NO_PROXY": np, "no_proxy": np,
	}
}
