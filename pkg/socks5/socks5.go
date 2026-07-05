// Package socks5 is a client-side SOCKS5 split-tunnel proxy that reaches cornus
// workloads by name. It is the alternative to per-port automatic forwarding
// (pkg/portfwd): instead of binding one local listener per published port, a
// single SOCKS5 listener routes each CONNECT by a set of resolution rules.
//
// A CONNECT target "host:port" whose subject matches a rule is rewritten to a
// "service:port" and tunneled into the cluster through a portfwd.Dialer (the
// same PortForward transport port-forwarding uses, reaching a deployment by name
// on any backend). A target that matches no rule is dialed directly from the
// proxy host — the "split tunnel": cluster names go in, everything else egresses
// normally.
//
// Scope is deliberately small: no-auth + CONNECT (TCP) only. BIND, UDP ASSOCIATE,
// and username/password auth are not implemented (SOCKS5 CONNECT is TCP, which is
// all the workload port-forward transport carries anyway).
package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cornus/pkg/logging"
	"cornus/pkg/portfwd"
	"cornus/pkg/wire"
)

// DefaultListen is the local address the proxy binds when none is configured.
const DefaultListen = "127.0.0.1:1080"

// DefaultHandshakeTimeout bounds how long an accepted connection may take to
// complete the SOCKS5 method negotiation and CONNECT request before it is
// reaped. Without it, a client that finishes the TCP handshake but sends no
// SOCKS5 bytes would park its handling goroutine, file descriptor, and conns
// entry for the entire proxy lifetime (an unbounded resource leak / DoS).
const DefaultHandshakeTimeout = 30 * time.Second

// DefaultSuffix is the service-host suffix NewSuffixRouter matches when none is
// given: "<name>.cornus.internal:<port>" resolves to service "<name>".
const DefaultSuffix = ".cornus.internal"

// DirectDialer dials the destinations no rule matched — the split-tunnel direct
// egress path. *net.Dialer satisfies it; tests inject a fake.
type DirectDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// LocalDialer opens a connection to a listener published in this process under a
// name. It is the third destination kind, alongside "tunnel to a workload" and
// "dial the internet directly": a published name resolves to its listener without
// any address, so nothing is bound and no port exists for the kernel to recycle to
// an unrelated process. pkg/memlisten satisfies it.
type LocalDialer interface {
	DialLocal(ctx context.Context) (net.Conn, error)
}

// Rule is one resolution rule: a Go regexp Pattern tested against the "host:port"
// subject, and a Replace template that yields the rewritten "service:port". The
// template accepts sed-style \1 backreferences (translated to Go's $1 form) so a
// rule can rewrite both the host and the port.
type Rule struct {
	Pattern string
	Replace string
}

// Router resolves a CONNECT target to a workload service (or reports no match, so
// the caller egresses directly). Rules are tried in order; the first match wins.
//
// On top of the static rules, a Router carries a mutable alias table mapping an
// unqualified service label to its real (context-prefixed) deployment name — e.g.
// "web" -> "demo-web". Aliases let a caller reach a compose service by the name it
// wrote in the compose file, in either form: the suffixed "web.cornus.internal"
// (a rule strips the suffix to "web", then the alias remaps it to "demo-web") or
// the bare, single-label "web" (which no suffix rule matches, so it is routed
// inward only when it exactly matches an alias — everything else egresses
// directly).
//
// Aliases are pure session state: registered/withdrawn as services come and go and
// never persisted, so the table is guarded for concurrent use against Resolve. A
// single proxy can be shared by more than one session (the agent refcounts one
// conduit per tunnel config), so a label is tracked per distinct deployment with a
// live-registration count. A label routes inward only while exactly one deployment
// claims it; if two concurrent sessions both register "web", the bare form is
// ambiguous and is not routed (the suffixed/qualified form still disambiguates),
// rather than silently reaching the wrong workload.
//
// A Router also carries a table of published local names: an exact "host:port"
// subject that resolves to a listener in this process (see LocalDialer). Locals are
// consulted BEFORE the rules, so a published name cannot be shadowed by a
// resolution rule — a catch-all rule would otherwise swallow it, and even the
// default suffix rule claims the whole "<name><suffix>" space.
type Router struct {
	rules []compiledRule

	aliasMu          sync.RWMutex
	aliases          map[string]map[string]int // service label -> deployment name -> live registrations
	locals           map[string]LocalDialer    // "host:port" subject -> in-process listener
	bareServiceNames bool                      // when false, only the suffixed alias form routes inward
}

type compiledRule struct {
	re   *regexp.Regexp
	repl string
}

// Kind is which destination a routed target resolves to.
type Kind int

const (
	// KindDirect: no name or rule claimed the target — dial host:port directly
	// (the "split" in split-tunnel).
	KindDirect Kind = iota
	// KindService: a rule or alias claimed it — tunnel to Service:Port through the
	// port-forward transport.
	KindService
	// KindLocal: a published name claimed it — hand off to Local, a listener in
	// this process.
	KindLocal
)

// Result is the outcome of routing one target. Kind selects which of the other
// fields is meaningful: KindService reads Service/Port, KindLocal reads Local, and
// KindDirect reads neither (the caller dials the original host:port).
type Result struct {
	Kind    Kind
	Service string
	Port    int
	Local   LocalDialer
}

// NewRouter compiles an ordered rule list. A bad pattern is a construction error.
func NewRouter(rules []Rule) (*Router, error) {
	r := &Router{
		aliases:          map[string]map[string]int{},
		locals:           map[string]LocalDialer{},
		bareServiceNames: true,
	}
	for i, rr := range rules {
		re, err := regexp.Compile(rr.Pattern)
		if err != nil {
			return nil, fmt.Errorf("socks5 resolution rule %d: bad pattern %q: %w", i, rr.Pattern, err)
		}
		r.rules = append(r.rules, compiledRule{re: re, repl: translateReplace(rr.Replace)})
	}
	return r, nil
}

// NewSuffixRouter builds the everyday default Router: a single rule that matches
// hosts bearing suffix and strips it, keeping the port —
// "<name><suffix>:<port>" -> "<name>:<port>". Hosts without the suffix match no
// rule and are dialed directly, so ordinary internet egress keeps working. An
// empty suffix uses DefaultSuffix.
func NewSuffixRouter(suffix string) (*Router, error) {
	if suffix == "" {
		suffix = DefaultSuffix
	}
	pattern := "^(.*)" + regexp.QuoteMeta(suffix) + ":([0-9]+)$"
	return NewRouter([]Rule{{Pattern: pattern, Replace: `\1:\2`}})
}

// SetBareServiceNames controls whether a bare, single-label CONNECT host (no
// suffix) that exactly matches a registered alias is routed inward. Enabled by
// default; disable it as an escape hatch when a service name would shadow a real
// single-label host the caller means to reach directly. The suffixed alias form is
// unaffected.
func (r *Router) SetBareServiceNames(enabled bool) {
	r.aliasMu.Lock()
	r.bareServiceNames = enabled
	r.aliasMu.Unlock()
}

// RegisterAlias records that the unqualified label resolves to deployment for the
// life of one service session. Registrations are counted per (label, deployment),
// so a recreate that overlaps its predecessor and a genuine cross-session collision
// are told apart: the count keeps the alias live across the overlap, and two
// distinct deployments under one label make that label ambiguous (see Resolve). An
// empty label or deployment is ignored.
func (r *Router) RegisterAlias(label, deployment string) {
	if label == "" || deployment == "" {
		return
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	deps := r.aliases[label]
	if deps == nil {
		deps = map[string]int{}
		r.aliases[label] = deps
	}
	deps[deployment]++
}

// UnregisterAlias drops one registration of label -> deployment (its withdrawal
// when a service session ends). The label is forgotten once its last registration
// is gone. Unbalanced or unknown removals are no-ops.
func (r *Router) UnregisterAlias(label, deployment string) {
	if label == "" || deployment == "" {
		return
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	deps := r.aliases[label]
	if deps == nil {
		return
	}
	if deps[deployment] <= 1 {
		delete(deps, deployment)
	} else {
		deps[deployment]--
	}
	if len(deps) == 0 {
		delete(r.aliases, label)
	}
}

// ClearAliases forgets every registered alias at once — the deterministic teardown
// a conduit runs when its session ends, so no alias outlives the session even if a
// per-service withdrawal was missed.
func (r *Router) ClearAliases() {
	r.aliasMu.Lock()
	r.aliases = map[string]map[string]int{}
	r.aliasMu.Unlock()
}

// lookupAlias returns the deployment a label resolves to, but only when exactly one
// distinct deployment claims it. A label with no registrations, or an ambiguous one
// claimed by two live sessions, returns "".
func (r *Router) lookupAlias(label string) string {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	deps := r.aliases[label]
	if len(deps) != 1 {
		return ""
	}
	for dep := range deps {
		return dep
	}
	return ""
}

// bareEnabled reports whether bare single-label alias matching is on.
func (r *Router) bareEnabled() bool {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	return r.bareServiceNames
}

// localSubject is the exact key a published name is stored under. Keying on
// "host:port" rather than host alone keeps every other port on that host falling
// through: "cornus.internal:443" stays a direct-egress target instead of tunneling
// TLS into a plaintext handler.
func localSubject(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

// RegisterLocal publishes d under host:port, so a CONNECT to exactly that target
// is handed to d instead of being routed by the rules. Registering the same
// subject twice replaces the previous dialer; callers that must not steal a live
// name check for a conflict before calling (the agent keys its own table on the
// published name and errors loudly on a duplicate). An empty host, an out-of-range
// port, or a nil dialer is ignored.
func (r *Router) RegisterLocal(host string, port int, d LocalDialer) {
	if host == "" || port < 1 || port > 65535 || d == nil {
		return
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	r.locals[localSubject(host, port)] = d
}

// UnregisterLocal withdraws the name published at host:port. Unknown removals are
// no-ops.
func (r *Router) UnregisterLocal(host string, port int) {
	if host == "" {
		return
	}
	r.aliasMu.Lock()
	defer r.aliasMu.Unlock()
	delete(r.locals, localSubject(host, port))
}

// ClearLocals forgets every published name at once — the deterministic teardown a
// conduit runs when its session ends, mirroring ClearAliases.
func (r *Router) ClearLocals() {
	r.aliasMu.Lock()
	r.locals = map[string]LocalDialer{}
	r.aliasMu.Unlock()
}

// lookupLocal returns the dialer published at host:port, or nil.
func (r *Router) lookupLocal(host string, port int) LocalDialer {
	r.aliasMu.RLock()
	defer r.aliasMu.RUnlock()
	return r.locals[localSubject(host, port)]
}

// Resolve routes "host:port".
//
// A published local name is checked FIRST and wins outright (KindLocal). It must
// outrank the rules: the name a UI is published under is a reserved claim, and a
// rule would otherwise shadow it — a catch-all "^(.*)$" rule swallows everything,
// and even a service-host suffix spelled without its leading dot (an accepted
// configuration) makes the default rule match the suffix's own apex, rewrite it to
// ":port", and fail the CONNECT rather than fall through.
//
// Otherwise the rules are tried in order. On the first match it applies the
// replacement and splits the result into a service name and port; a registered
// alias then remaps the resulting service label to its real deployment name (so a
// suffix rule's "web" becomes "demo-web"). A match whose rewritten result is not a
// valid "service:port" returns KindService with a non-nil error (the CONNECT fails
// rather than leaking to direct egress). When no rule matches, a bare host that
// exactly matches an alias is routed inward; anything else returns KindDirect and a
// nil error (dial host:port directly).
func (r *Router) Resolve(host string, port int) (Result, error) {
	if d := r.lookupLocal(host, port); d != nil {
		return Result{Kind: KindLocal, Local: d}, nil
	}
	subject := host + ":" + strconv.Itoa(port)
	for _, rl := range r.rules {
		loc := rl.re.FindStringSubmatchIndex(subject)
		if loc == nil {
			continue
		}
		out := string(rl.re.ExpandString(nil, rl.repl, subject, loc))
		svc, p, err := splitServicePort(out)
		if err != nil {
			return Result{Kind: KindService}, fmt.Errorf("resolution rule rewrote %q to %q: %w", subject, out, err)
		}
		// Remap an unqualified service label onto its real deployment name; a label
		// with no alias (e.g. an already-qualified "demo-web") passes through.
		if dep := r.lookupAlias(svc); dep != "" {
			svc = dep
		}
		return Result{Kind: KindService, Service: svc, Port: p}, nil
	}
	// No rule matched. A bare, single-label host is routed inward only when bare
	// aliasing is on and it exactly (and unambiguously) names a registered service;
	// everything else egresses directly (the split).
	if r.bareEnabled() {
		if dep := r.lookupAlias(host); dep != "" {
			return Result{Kind: KindService, Service: dep, Port: port}, nil
		}
	}
	return Result{Kind: KindDirect}, nil
}

// splitServicePort splits a rewritten "service:port" on the last colon and
// validates both halves (non-empty service, port in 1..65535).
func splitServicePort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("no port")
	}
	svc, portStr := s[:i], s[i+1:]
	if svc == "" {
		return "", 0, fmt.Errorf("empty service name")
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("port %q is not a number", portStr)
	}
	if p < 1 || p > 65535 {
		return "", 0, fmt.Errorf("port %d out of range (1-65535)", p)
	}
	return svc, p, nil
}

// translateReplace converts a sed-style replacement template (\1, \2, ...) into
// Go regexp's $-form for Regexp.Expand: a backslash-digit becomes ${N}, any other
// backslash-escape drops the backslash, and a literal $ is escaped to $$ so it is
// not read as a group reference.
func translateReplace(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '$':
			b.WriteString("$$")
		case '\\':
			if i+1 < len(s) {
				n := s[i+1]
				if n >= '0' && n <= '9' {
					b.WriteString("${")
					b.WriteByte(n)
					b.WriteByte('}')
				} else {
					b.WriteByte(n)
				}
				i++
				continue
			}
			b.WriteByte('\\')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Option configures Start.
type Option func(*options)

type options struct {
	logf             func(format string, args ...any)
	direct           DirectDialer
	handshakeTimeout time.Duration
	allowNonLoopback bool
}

// WithLogf routes non-fatal per-connection warnings (default: slog warnings).
func WithLogf(logf func(format string, args ...any)) Option {
	return func(o *options) { o.logf = logf }
}

// WithDirectDialer overrides the dialer used for unmatched (direct-egress)
// targets. The default is &net.Dialer{}; tests inject a fake.
func WithDirectDialer(d DirectDialer) Option {
	return func(o *options) {
		if d != nil {
			o.direct = d
		}
	}
}

// WithHandshakeTimeout overrides how long an accepted connection may take to
// finish the SOCKS5 negotiation and CONNECT before it is reaped (default
// DefaultHandshakeTimeout). A non-positive d disables the deadline. Mainly a
// testing seam.
func WithHandshakeTimeout(d time.Duration) Option {
	return func(o *options) { o.handshakeTimeout = d }
}

// WithAllowNonLoopback permits binding the proxy to a non-loopback address.
//
// Start refuses one by default, because this proxy performs no authentication
// (the SOCKS5 no-auth method is the only one offered) and dials arbitrary
// unmatched destinations from the host it runs on: reachable off-host, it is an
// open proxy for anyone who can route to it, including into this machine's own
// loopback services. Only pass this when the caller has explicitly asked for it
// and understands that; a non-loopback proxy additionally refuses to dial
// loopback and link-local destinations (see loopbackGuard), so it cannot be used
// to pivot into services on the proxy host.
func WithAllowNonLoopback(allow bool) Option {
	return func(o *options) { o.allowNonLoopback = allow }
}

// loopbackAddr reports whether a bound listener address is loopback-only.
// It reads the address the kernel actually bound rather than the requested string,
// so a hostname (or a poisoned "localhost") is judged by where it truly landed and
// a wildcard bind ("" / "0.0.0.0" / "::") is correctly rejected as unspecified.
func loopbackAddr(a net.Addr) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return false
	}
	return ta.IP.IsLoopback()
}

// loopbackGuard wraps the direct-egress dialer for a proxy bound off-host, and
// refuses connections that landed on the proxy host's own loopback or on a
// link-local address. It checks the established connection's remote address rather
// than resolving the requested name first, so a name that resolves to 127.0.0.1
// (or re-resolves between check and dial) cannot slip through.
type loopbackGuard struct{ inner DirectDialer }

func (g loopbackGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := g.inner.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	ta, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return c, nil
	}
	if ta.IP.IsLoopback() || ta.IP.IsLinkLocalUnicast() || ta.IP.IsLinkLocalMulticast() || ta.IP.IsUnspecified() {
		_ = c.Close()
		return nil, fmt.Errorf("socks5: refusing to dial %s (%s): a non-loopback proxy may not reach the proxy host's loopback or link-local addresses", address, ta.IP)
	}
	return c, nil
}

// Proxy is one live SOCKS5 listener. Its lifetime mirrors portfwd.Group: it
// closes itself when the Start ctx ends, Close tears it down earlier, and in-
// flight connections are severed on teardown.
type Proxy struct {
	ln               net.Listener
	router           *Router
	dialer           portfwd.Dialer
	direct           DirectDialer
	logf             func(format string, args ...any)
	handshakeTimeout time.Duration
	cancel           context.CancelFunc
	wg               sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  bool
}

// Start binds a SOCKS5 listener on addr (DefaultListen when empty) and serves
// CONNECT requests, routing each via router: a matched target tunnels into the
// workload through d.PortForward, an unmatched target is dialed directly (the
// split tunnel). The proxy runs until ctx is cancelled or Close is called.
func Start(ctx context.Context, d portfwd.Dialer, router *Router, addr string, opts ...Option) (*Proxy, error) {
	log := logging.FromContext(ctx)
	o := options{
		logf:             func(format string, args ...any) { log.WarnContext(ctx, fmt.Sprintf(format, args...)) },
		direct:           &net.Dialer{},
		handshakeTimeout: DefaultHandshakeTimeout,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if addr == "" {
		addr = DefaultListen
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("socks5: listen on %s: %w", addr, err)
	}
	// Judge the address the kernel actually bound, not the requested string.
	direct := o.direct
	if !loopbackAddr(ln.Addr()) {
		if !o.allowNonLoopback {
			_ = ln.Close()
			return nil, fmt.Errorf("socks5: refusing to bind %s: this proxy has no authentication and dials arbitrary destinations from this host, so a non-loopback listener is an open proxy for anyone who can reach it (bind a loopback address, or pass the explicit opt-in if you really mean it)", ln.Addr())
		}
		// Explicitly allowed off-host: at least deny the loopback pivot, so a remote
		// client cannot use the proxy to reach this machine's own services.
		direct = loopbackGuard{inner: direct}
	}
	fctx, cancel := context.WithCancel(ctx)
	p := &Proxy{
		ln:               ln,
		router:           router,
		dialer:           d,
		direct:           direct,
		logf:             o.logf,
		handshakeTimeout: o.handshakeTimeout,
		cancel:           cancel,
		conns:            map[net.Conn]struct{}{},
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.serve(fctx)
	}()
	// Tie the proxy's lifetime to ctx so a caller holding a session needs no
	// explicit Close on the cancel path.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		<-fctx.Done()
		p.shutdown()
	}()
	return p, nil
}

// Addr is the actually-bound local listen address (meaningful when addr used
// port 0).
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Close tears the proxy down: the listener closes, in-flight connections are
// severed, and serving goroutines drain. Idempotent.
func (p *Proxy) Close() {
	p.cancel()
	p.shutdown()
	p.wg.Wait()
}

func (p *Proxy) serve(ctx context.Context) {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		if !p.track(c) {
			_ = c.Close()
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(c)
			defer c.Close()
			p.handle(ctx, c)
		}()
	}
}

// handle runs the SOCKS5 no-auth handshake and one CONNECT, then routes and
// splices. Protocol errors and dial failures are logged, not fatal to the proxy.
func (p *Proxy) handle(ctx context.Context, c net.Conn) {
	// Bound the negotiation + CONNECT reads so a client that connects but sends
	// nothing is reaped instead of parking a goroutine and FD forever. The
	// deadline is cleared before splicing (below) so an idle-but-established
	// tunnel is not torn down.
	if p.handshakeTimeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(p.handshakeTimeout))
	}
	if err := serveHandshake(c); err != nil {
		p.logf("socks5: handshake from %s failed: %v", c.RemoteAddr(), err)
		return
	}
	host, port, err := readConnect(c)
	if err != nil {
		p.logf("socks5: connect from %s failed: %v", c.RemoteAddr(), err)
		return
	}
	if host == "" {
		// A zero-length domain would otherwise dial ":port" (the proxy host's own
		// localhost); reject it.
		p.logf("socks5: empty destination host from %s", c.RemoteAddr())
		_ = writeReply(c, repHostUnreachable)
		return
	}

	res, rerr := p.router.Resolve(host, port)
	if rerr != nil {
		p.logf("socks5: %v", rerr)
		_ = writeReply(c, repHostUnreachable)
		return
	}

	var upstream net.Conn
	switch res.Kind {
	case KindLocal:
		// A published name: hand off to the in-process listener. Neither the
		// port-forward transport nor the direct dialer is involved — there is no
		// address to dial.
		upstream, err = res.Local.DialLocal(ctx)
		if err != nil {
			p.logf("socks5: local handoff for %s:%d failed: %v", host, port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
	case KindService:
		upstream, err = p.dialer.PortForward(ctx, res.Service, res.Port, "tcp")
		if err != nil {
			p.logf("socks5: tunnel to %s:%d failed: %v", res.Service, res.Port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
	default:
		upstream, err = p.direct.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			p.logf("socks5: direct dial to %s:%d failed: %v", host, port, err)
			_ = writeReply(c, repHostUnreachable)
			return
		}
	}
	if !p.track(upstream) {
		_ = upstream.Close()
		return
	}
	defer p.untrack(upstream)
	defer upstream.Close()
	if err := writeReply(c, repSucceeded); err != nil {
		return
	}
	// Negotiation is done: clear the handshake read deadline so the spliced
	// connection can idle without being reaped.
	if p.handshakeTimeout > 0 {
		_ = c.SetReadDeadline(time.Time{})
	}
	wire.Pipe(c, upstream)
}

// shutdown closes the listener and severs in-flight connections exactly once.
func (p *Proxy) shutdown() {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return
	}
	p.done = true
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()

	_ = p.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
}

// track registers an in-flight conn for severing on Close. It reports false — and
// does not register — when the proxy is already shutting down.
func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// SOCKS5 wire constants (RFC 1928).
const (
	socksVersion = 0x05
	methodNoAuth = 0x00
	methodNone   = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded        = 0x00
	repHostUnreachable  = 0x04
	repCmdNotSupported  = 0x07
	repAddrNotSupported = 0x08
)

// serveHandshake reads the client's method-negotiation greeting and selects the
// no-auth method (the only one offered). It errors — after replying "no
// acceptable methods" — when the client does not offer no-auth.
func serveHandshake(c net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c, header); err != nil {
		return err
	}
	if header[0] != socksVersion {
		return fmt.Errorf("unsupported version %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	for _, m := range methods {
		if m == methodNoAuth {
			_, err := c.Write([]byte{socksVersion, methodNoAuth})
			return err
		}
	}
	_, _ = c.Write([]byte{socksVersion, methodNone})
	return fmt.Errorf("client offered no no-auth method")
}

// readConnect reads a CONNECT request and returns its destination host and port.
// A non-CONNECT command or an unsupported address type is answered with the
// matching SOCKS5 error reply before returning an error.
func readConnect(c net.Conn) (string, int, error) {
	header := make([]byte, 4) // VER, CMD, RSV, ATYP
	if _, err := io.ReadFull(c, header); err != nil {
		return "", 0, err
	}
	if header[0] != socksVersion {
		return "", 0, fmt.Errorf("unsupported version %d", header[0])
	}
	if header[1] != cmdConnect {
		_ = writeReply(c, repCmdNotSupported)
		return "", 0, fmt.Errorf("unsupported command %d (only CONNECT)", header[1])
	}

	var host string
	switch header[3] {
	case atypIPv4:
		b := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case atypIPv6:
		b := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case atypDomain:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", 0, err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		_ = writeReply(c, repAddrNotSupported)
		return "", 0, fmt.Errorf("unsupported address type %d", header[3])
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return "", 0, err
	}
	return host, int(pb[0])<<8 | int(pb[1]), nil
}

// writeReply writes a SOCKS5 reply with a zero BND.ADDR/BND.PORT (a bound
// address the CONNECT client ignores).
func writeReply(c net.Conn, rep byte) error {
	_, err := c.Write([]byte{socksVersion, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}
