package clientconduit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/conduithost"
	"cornus/pkg/portfwd"
	"cornus/pkg/socks5"
)

// WithRendezvous makes a SOCKS5 conduit joinable by other processes: it is bound
// through r, so a later request for an address this one covers joins it instead of
// forking a second proxy that cannot bind.
//
// It changes nothing for port-forward, none, or an EPHEMERAL socks5 address. An
// ephemeral conduit is private by construction — there is no agreed address for
// anyone to rendezvous on — which is what replaces the old session-local flag with
// a property that follows from the address rather than contradicting it.
func WithRendezvous(r *conduithost.Registry) Option {
	return func(o *options) { o.registry = r }
}

// rendezvousConduit is a conduit reached through pkg/conduithost, whether this
// process hosts it or joined one another process hosts.
//
// The two cases share an implementation deliberately: registrations go through the
// Participant either way, so the host applies them to its own router and a joiner
// sends them over the control socket, and no caller has to know which it got. That
// indifference is the whole point of the redesign.
type rendezvousConduit struct {
	// mu guards p/proxy/router/local, which are REPLACED when ownership moves: a
	// joiner whose host dies becomes the host, and everything downstream of that has
	// to follow.
	mu     sync.Mutex
	p      conduithost.Participant
	proxy  *socks5.Proxy  // nil while this process is only a joiner
	router *socks5.Router // this process's router; serves once it hosts
	local  *socks5Conduit // set once hosting, for ingress, which terminates here
	// upstreamDir is where this process lends sockets for the names it publishes.
	upstreamDir string
	nextID      atomic.Uint64

	// Kept for a takeover: becoming the host means serving the socket this process
	// has been holding a replica of all along, which needs the same dialer and
	// settings the conduit was opened with.
	ctx  context.Context
	d    portfwd.Dialer
	cfg  Config
	opts options
}

// startRendezvous binds or joins the conduit for cfg through the registry.
func startRendezvous(ctx context.Context, d portfwd.Dialer, cfg Config, o options) (Conduit, error) {
	addr, err := conduithost.ParseAddr(listenAddr(cfg))
	if err != nil {
		return nil, err
	}
	router, err := Router(cfg)
	if err != nil {
		return nil, err
	}
	settings, err := json.Marshal(conduitSettings{
		Suffix:    cfg.Socks5Suffix,
		BareNames: cfg.Socks5BareServiceNames == nil || *cfg.Socks5BareServiceNames,
	})
	if err != nil {
		return nil, err
	}
	reg := &Registrar{
		Router: router,
		// Every registration this host applies is dialed through its own tunnel for
		// now. A joiner's ConnSpec-derived dialer is what makes consolidation across
		// SERVERS work, and it arrives with the wire payload that carries it.
		Dialer: func(conduithost.Peer) portfwd.Dialer { return nil },
		// A published name is served by a listener with no address, so its publisher
		// lends a socket and this side dials it. The publisher may be another process
		// or this one; the mechanism is the same either way, which is what lets a
		// published name replay after a takeover like any other claim.
		LocalDial: func(upstream string) socks5.LocalDialer {
			if upstream == "" {
				return nil
			}
			return unixLocalDialer{path: upstream}
		},
		// A name moving between workloads is invisible to everyone except this
		// process, which is the only one that sees both claims.
		Logf: o.logf,
	}

	p, err := conduithost.Open(ctx, conduithost.Config{
		Registry:  o.registry,
		Addr:      addr,
		Registrar: reg,
		Settings:  settings,
		Banner:    socks5Banner(cfg, addr.String()),
		// Built again from the address actually bound: a wildcard request comes back as
		// a dual-stack "[::]", and the banner is what tells a user where to point a
		// browser — and is handed to every joiner.
		BannerFor: func(bound conduithost.Addr) []string { return socks5Banner(cfg, bound.String()) },
		Logf:      o.logf,
	})
	if err != nil {
		return nil, err
	}

	rc := &rendezvousConduit{
		p: p, router: router, upstreamDir: upstreamDir(o.registry.Dir()),
		ctx: ctx, d: d, cfg: cfg, opts: o,
	}
	if !p.Hosting() {
		// Joined: the data plane belongs to the host. This process only registers
		// names into it — until that host exits, when one of the joiners has to take
		// the socket over, and it may be this one.
		go rc.watchForTakeover(p)
		return rc, nil
	}

	host, ok := p.(*conduithost.Host)
	if !ok {
		_ = p.Close()
		return nil, fmt.Errorf("clientconduit: conduit host has unexpected type %T", p)
	}
	proxy, err := socks5.Serve(ctx, d, router, host.Listener(),
		socks5.WithLogf(o.logf),
		socks5.WithAllowNonLoopback(cfg.Socks5AllowNonLoopback))
	if err != nil {
		// Serve refuses without closing the listener, so releasing the rendezvous is
		// this side's job — otherwise the address stays claimed by a conduit that
		// never serves.
		_ = p.Close()
		return nil, err
	}
	rc.proxy = proxy
	rc.local = &socks5Conduit{proxy: proxy, router: router, banner: p.Banner(), d: d, ingress: cfg.Ingress}
	return rc, nil
}

// watchForTakeover waits for the conduit's host to exit and then contends for it.
//
// Without this the replica every joiner holds is not a feature but a HAZARD: the
// address stays bound because a reference survives, yet nobody accepts on it, so
// clients hang instead of failing over — and a later session cannot rebind it
// either. An unwired migration is strictly worse than none at all, which is what
// the reported "start compose, join, kill compose, start compose again" sequence
// found.
func (c *rendezvousConduit) watchForTakeover(p conduithost.Participant) {
	select {
	case <-p.Done():
	case <-c.ctx.Done():
		return
	}
	j, ok := p.(*conduithost.Joiner)
	if !ok {
		return
	}
	next, err := j.Takeover(c.ctx)
	if err != nil {
		// Nothing else can be done here, and it matters: the conduit is not being
		// served by this process and may not be served at all.
		c.logf("conduit %s: the host exited and this process could not take over: %v", j.Addr(), err)
		return
	}
	if err := c.adopt(next); err != nil {
		c.logf("conduit %s: took over but could not serve it: %v", next.Addr(), err)
		_ = next.Close()
		return
	}
}

// adopt installs the participant a takeover produced, serving the conduit when it
// made this process the host.
func (c *rendezvousConduit) adopt(next conduithost.Participant) error {
	if !next.Hosting() {
		// Another survivor won; this process is a joiner again, and must watch the
		// new host in turn or the chain ends after one hop.
		c.mu.Lock()
		c.p = next
		c.mu.Unlock()
		go c.watchForTakeover(next)
		return nil
	}
	host, ok := next.(*conduithost.Host)
	if !ok {
		return fmt.Errorf("clientconduit: conduit host has unexpected type %T", next)
	}
	proxy, err := socks5.Serve(c.ctx, c.d, c.router, host.Listener(),
		socks5.WithLogf(c.opts.logf),
		socks5.WithAllowNonLoopback(c.cfg.Socks5AllowNonLoopback))
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.p = next
	c.proxy = proxy
	c.local = &socks5Conduit{proxy: proxy, router: c.router, banner: next.Banner(), d: c.d, ingress: c.cfg.Ingress}
	c.mu.Unlock()
	c.logf("conduit %s: the previous host exited; this process is now serving it", next.Addr())
	return nil
}

// participant is the current handle, which a takeover may have replaced.
func (c *rendezvousConduit) participant() conduithost.Participant {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.p
}

func (c *rendezvousConduit) logf(format string, args ...any) {
	if c.opts.logf != nil {
		c.opts.logf(format, args...)
	}
}

// unregister withdraws a registration through the CURRENT participant. Routed by
// id rather than by the closure Register returned, because that closure refers to a
// participant a takeover may already have replaced.
func (c *rendezvousConduit) unregister(id string) {
	p := c.participant()
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.Unregister(ctx, id)
}

// conduitSettings is what a joiner is told about the conduit it landed in. Kept
// small on purpose: it exists so a joiner can report the suffix a browser must
// actually use, which after consolidation is the incumbent's and not its own.
type conduitSettings struct {
	Suffix    string `json:"suffix,omitempty"`
	BareNames bool   `json:"bareNames"`
}

// listenAddr is the address a socks5 conduit binds, with the engine's default
// substituted, so the rendezvous keys on the same value socks5.Start would use.
func listenAddr(cfg Config) string {
	if cfg.Socks5Listen == "" {
		return socks5.DefaultListen
	}
	return cfg.Socks5Listen
}

func (c *rendezvousConduit) Banner() []string { return c.participant().Banner() }

func (c *rendezvousConduit) CAInfo() []string {
	if l := c.localConduit(); l != nil {
		return l.CAInfo()
	}
	return nil
}

// localConduit is the host-side helper, non-nil only while this process serves.
func (c *rendezvousConduit) localConduit() *socks5Conduit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.local
}

// Add registers each alias as a short name for the deployment, withdrawn when ctx
// ends. It binds no listeners: in SOCKS5 mode one proxy already reaches every
// deployment by name.
func (c *rendezvousConduit) Add(ctx context.Context, name string, _ []api.PortMapping, aliases ...string) ([]Forward, error) {
	for _, alias := range aliases {
		if alias == "" || alias == name {
			continue // nothing to add beyond the deployment name the proxy already serves
		}
		payload, err := json.Marshal(AliasPayload{Label: alias, Deployment: name})
		if err != nil {
			return nil, err
		}
		id := c.id("alias")
		if _, err := c.participant().Register(ctx, id, KindAlias, payload); err != nil {
			return nil, fmt.Errorf("clientconduit: registering alias %q: %w", alias, err)
		}
		go func() {
			<-ctx.Done()
			c.unregister(id)
		}()
	}
	return nil, nil
}

// AddLocal publishes d under host:port, whether this process hosts the conduit or
// joined one.
//
// The listener has no address by design, so it is lent to the conduit over a unix
// socket and the host dials that. This is the same path when this process IS the
// host — one local hop for one name, in exchange for a published name being the
// same kind of thing wherever it came from, and so replaying after a takeover with
// no case of its own.
func (c *rendezvousConduit) AddLocal(ctx context.Context, host string, port int, d socks5.LocalDialer) (bool, error) {
	if host == "" || port < 1 || port > 65535 || d == nil {
		return false, fmt.Errorf("clientconduit: publish %s:%d: need a non-empty host, a port in 1-65535, and a dialer", host, port)
	}
	upstream, closeUpstream, err := publishOverSocket(ctx, c.upstreamDir, host, d)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(LocalPayload{Host: host, Port: port, Upstream: upstream})
	if err != nil {
		closeUpstream()
		return false, err
	}
	id := c.id("local")
	if _, err := c.participant().Register(ctx, id, KindLocal, payload); err != nil {
		closeUpstream()
		return false, fmt.Errorf("clientconduit: publishing %s:%d: %w", host, port, err)
	}
	go func() {
		<-ctx.Done()
		c.unregister(id)
		closeUpstream()
	}()
	return true, nil
}

// AddIngress registers the workload's ingress host(s). Host-only, for the same
// reason as AddLocal: both terminate in this process.
func (c *rendezvousConduit) AddIngress(ctx context.Context, name string, in *api.IngressSpec, ports []api.PortMapping) ([]string, error) {
	l := c.localConduit()
	if l == nil {
		if in == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("clientconduit: cannot serve the ingress for %q in a conduit hosted by another process", name)
	}
	return l.AddIngress(ctx, name, in, ports)
}

// Close leaves the conduit. For a host that stops serving and frees the address for
// a successor; for a joiner it withdraws everything this process registered.
func (c *rendezvousConduit) Close() {
	c.mu.Lock()
	local, p := c.local, c.p
	c.mu.Unlock()
	if local != nil {
		local.Close()
	}
	_ = p.Close()
}

// Participant exposes the rendezvous handle, so a caller can watch Done and take
// over when the host dies.
func (c *rendezvousConduit) Participant() conduithost.Participant { return c.participant() }

func (c *rendezvousConduit) id(kind string) string {
	return fmt.Sprintf("%s-%d", kind, c.nextID.Add(1))
}

var _ Conduit = (*rendezvousConduit)(nil)

// ephemeralListen reports whether cfg asks for a kernel-assigned port. Such a
// conduit cannot be rendezvoused on: there is no address for anyone else to name.
func ephemeralListen(cfg Config) bool {
	a, err := conduithost.ParseAddr(listenAddr(cfg))
	return err == nil && a.Ephemeral()
}

// Suffix is the service-host suffix of the conduit this participant is in.
//
// After consolidation that is the INCUMBENT's suffix, which may not be the one this
// caller asked for — and it is the one that matters, because a name derived from
// the requested suffix would resolve through the proxy and then be refused by a
// BFF whose Host allow-list was pinned to a name the conduit does not serve.
func (c *rendezvousConduit) Suffix() string {
	var s conduitSettings
	if err := json.Unmarshal(c.participant().Settings(), &s); err != nil || s.Suffix == "" {
		return socks5.DefaultSuffix
	}
	return s.Suffix
}

// SuffixOf reports the service-host suffix a conduit resolves names under, for a
// caller that must derive a name the conduit will actually serve. Conduits that
// resolve no names report the socks5 default, which is inert for them.
func SuffixOf(c Conduit) string {
	type suffixed interface{ Suffix() string }
	if s, ok := c.(suffixed); ok {
		return s.Suffix()
	}
	return socks5.DefaultSuffix
}
