package egresspolicy

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grafana/sobek"
)

// scriptTimeout bounds a single FindProxyForURL evaluation. A runaway or infinite
// script is interrupted and the evaluation fails closed to deny.
const scriptTimeout = 50 * time.Millisecond

// compileScript builds a Policy from a PAC-style JavaScript program. The program is
// compiled once here (a syntax error is a deploy-time failure); each evaluation runs
// FindProxyForURL(url, host) in a sandboxed, pooled runtime with the pure PAC
// builtins, a bounded execution time, and no ambient authority (no require, no live
// I/O, deterministic Date/random). def is the route for a null / no-match return.
func compileScript(script, def string) (Policy, error) {
	prog, err := sobek.Compile("egress.pac", script, false)
	if err != nil {
		return nil, fmt.Errorf("egresspolicy: compile script: %w", err)
	}
	p := &scriptPolicy{prog: prog, def: def}
	p.pool.New = func() any { return p.newRuntime() }
	// Validate the program actually defines FindProxyForURL by building one runtime.
	sr := p.pool.Get().(*scriptRuntime)
	ok := sr.fn != nil
	p.pool.Put(sr)
	if !ok {
		return nil, fmt.Errorf("egresspolicy: script does not define function FindProxyForURL(url, host)")
	}
	return p, nil
}

// scriptPolicy evaluates a PAC program. sobek runtimes are not goroutine-safe, so a
// pool of pre-initialized runtimes is kept; each is used by one goroutine at a time.
type scriptPolicy struct {
	prog *sobek.Program
	def  string
	pool sync.Pool
}

// scriptRuntime is one pooled runtime plus its resolved FindProxyForURL and the
// destination currently being evaluated (read by the dest-dependent builtins).
type scriptRuntime struct {
	rt  *sobek.Runtime
	fn  sobek.Callable
	cur Dest
}

func (p *scriptPolicy) newRuntime() *scriptRuntime {
	rt := sobek.New()
	// Determinism / no ambient authority: fixed random + time sources (Date and
	// Math.random cannot leak nondeterminism), and sobek links no require/FS/net.
	rt.SetRandSource(func() float64 { return 0 })
	rt.SetTimeSource(func() time.Time { return time.Unix(0, 0).UTC() })
	sr := &scriptRuntime{rt: rt}
	registerPACBuiltins(rt, sr)
	if _, err := rt.RunProgram(p.prog); err == nil {
		if fn, ok := sobek.AssertFunction(rt.Get("FindProxyForURL")); ok {
			sr.fn = fn
		}
	}
	return sr
}

func (p *scriptPolicy) Route(d Dest) (string, error) {
	sr := p.pool.Get().(*scriptRuntime)
	defer p.pool.Put(sr)
	if sr.fn == nil {
		return RouteDeny, fmt.Errorf("egresspolicy: script runtime uninitialized")
	}
	sr.cur = d

	host := d.Host
	if host == "" && d.IP != nil {
		host = d.IP.String()
	}
	// PAC's first argument is the full URL; we synthesize a stable one from the
	// destination (a real scheme is unknown at L4, so "tcp"). host is the 2nd arg.
	url := "tcp://" + net.JoinHostPort(host, strconv.Itoa(d.Port)) + "/"

	timer := time.AfterFunc(scriptTimeout, func() { sr.rt.Interrupt("egress script timeout") })
	ret, err := sr.fn(sobek.Undefined(), sr.rt.ToValue(url), sr.rt.ToValue(host))
	timer.Stop()
	sr.rt.ClearInterrupt() // clear any interrupt (fired or not) before reuse
	if err != nil {
		// Exception or timeout: fail closed.
		return RouteDeny, fmt.Errorf("egresspolicy: script eval: %w", err)
	}
	if sobek.IsUndefined(ret) || sobek.IsNull(ret) {
		return p.def, nil
	}
	return pacReturnToRoute(ret.String(), p.def), nil
}

// pacReturnToRoute maps a FindProxyForURL return string to a route. The return is a
// ';'-separated list of directives; the first is used. Standard PAC keywords and
// cornus's own route names are both accepted:
//
//	DIRECT                       -> cluster  (connect directly from the pod's network)
//	DENY / BLOCK                 -> deny
//	CLIENT / CLUSTER / GATEWAY   -> that route (cornus extension)
//	PROXY|HTTPS|SOCKS|SOCKS5 arg -> if arg is client/gateway/cluster, that route;
//	                                otherwise client (route via the client, which
//	                                holds the real proxy)
//
// An empty or unrecognized directive falls back to def.
func pacReturnToRoute(ret, def string) string {
	first := strings.TrimSpace(strings.SplitN(ret, ";", 2)[0])
	if first == "" {
		return def
	}
	fields := strings.Fields(first)
	kw := strings.ToUpper(fields[0])
	switch kw {
	case "DIRECT":
		return RouteCluster
	case "DENY", "BLOCK":
		return RouteDeny
	case "CLIENT":
		return RouteClient
	case "CLUSTER":
		return RouteCluster
	case "GATEWAY":
		return RouteGateway
	case "PROXY", "HTTPS", "HTTP", "SOCKS", "SOCKS5":
		if len(fields) >= 2 {
			switch strings.ToLower(fields[1]) {
			case "client":
				return RouteClient
			case "gateway":
				return RouteGateway
			case "cluster":
				return RouteCluster
			}
		}
		// A concrete proxy host: route via the client, which applies the real proxy.
		return RouteClient
	default:
		return def
	}
}

// registerPACBuiltins installs the pure PAC helper functions. The dest-dependent
// ones (dnsResolve/isInNet/isResolvable/myIpAddress) close over sr and read the
// destination currently being evaluated; they do NO live DNS/network I/O — a name
// resolves only to the destination IP the caller already knows, keeping evaluation
// pure and reproducible across the caretaker/server/client evaluation points.
func registerPACBuiltins(rt *sobek.Runtime, sr *scriptRuntime) {
	rt.Set("shExpMatch", func(str, shexp string) bool { return shExpMatch(str, shexp) })
	rt.Set("dnsDomainIs", func(host, domain string) bool { return strings.HasSuffix(host, domain) })
	rt.Set("isPlainHostName", func(host string) bool { return !strings.Contains(host, ".") })
	rt.Set("localHostOrDomainIs", func(host, hostdom string) bool {
		return host == hostdom || (!strings.Contains(host, ".") && strings.HasPrefix(hostdom, host+"."))
	})
	rt.Set("dnsResolve", func(host string) string {
		if ip := resolveForScript(sr, host); ip != nil {
			return ip.String()
		}
		return ""
	})
	rt.Set("isResolvable", func(host string) bool { return resolveForScript(sr, host) != nil })
	rt.Set("myIpAddress", func() string { return "127.0.0.1" }) // bound placeholder, not live
	rt.Set("isInNet", func(host, pattern, mask string) bool { return isInNet(sr, host, pattern, mask) })
	rt.Set("dnsDomainLevels", func(host string) int { return strings.Count(host, ".") })
}

// resolveForScript returns the destination IP for host WITHOUT live DNS: an IP
// literal resolves to itself, and the destination's own hostname resolves to the IP
// the caller already knows; every other name is unresolvable (nil).
func resolveForScript(sr *scriptRuntime, host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	if host != "" && strings.EqualFold(host, sr.cur.Host) && sr.cur.IP != nil {
		return sr.cur.IP
	}
	return nil
}

func isInNet(sr *scriptRuntime, host, pattern, mask string) bool {
	ip := resolveForScript(sr, host)
	if ip == nil {
		return false
	}
	ip4, pat, m := ip.To4(), net.ParseIP(pattern).To4(), net.ParseIP(mask).To4()
	if ip4 == nil || pat == nil || m == nil {
		return false
	}
	for i := 0; i < 4; i++ {
		if ip4[i]&m[i] != pat[i]&m[i] {
			return false
		}
	}
	return true
}

// shExpMatch implements PAC's shell-glob match (* and ?) over the whole string.
func shExpMatch(str, shexp string) bool {
	m, err := regexp.MatchString("^"+globToRegexp(shexp)+"$", str)
	return err == nil && m
}

// globToRegexp converts a shell glob (only * and ? are special) to a regexp,
// escaping every other regexp metacharacter.
func globToRegexp(glob string) string {
	var b strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}
