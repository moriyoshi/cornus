//go:build linux

package barehost

// Server-hosted DNS for guest containers, complementing the hosts-file sync. The
// bridge CNI has no embedded resolver, so — like the caretaker's dns role does
// for kubernetes pods (pkg/caretaker/dns.go) — the bare backend runs a small
// authoritative resolver in the cornus server process. It binds one socket per
// managed network on that network's CNI bridge gateway IP (10.4.<n>.1), so a
// container's query to its default gateway is answered by cornus: A records for
// peer services on that network (round-robin across all replicas — the hosts
// file only publishes replica 0), and everything else forwarded to the host's
// upstream resolvers. Each container's /etc/resolv.conf lists the gateway first
// with the host upstreams as fallback, so if a bind ever fails the container
// still resolves external names (and peer names via /etc/hosts, which nsswitch
// consults before DNS anyway) — the feature degrades gracefully.

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"cornus/pkg/logging"
)

// dnsTTL is the TTL stamped on synthesized A records (short: memberships change
// on every deploy).
const dnsTTL = 30

// dnsManager owns the per-network resolver sockets and their answer zones. Zones
// and listeners are reconciled together on every Apply/Delete.
type dnsManager struct {
	enabled bool

	mu        sync.Mutex
	zones     map[string]map[string][]net.IP // network -> canonical query name -> IPs
	listeners map[string]*dnsListener        // network -> listener
	upstreams []string                       // host resolvers (host:port) for unknowns

	rr  atomic.Uint64 // round-robin rotation counter
	log *slog.Logger
}

// dnsListener is one network's bound resolver (UDP + TCP on the bridge gateway).
type dnsListener struct {
	gateway string
	udp     *dns.Server
	tcp     *dns.Server
}

// newDNSManager builds the resolver, enabled unless CORNUS_BARE_DNS is a false
// value. Upstreams are the host's own resolvers (parsed from /etc/resolv.conf).
func newDNSManager(enabled bool) *dnsManager {
	m := &dnsManager{
		enabled:   enabled,
		zones:     map[string]map[string][]net.IP{},
		listeners: map[string]*dnsListener{},
		upstreams: hostUpstreams(),
		log:       slog.Default().With(slog.String("component", "bare-dns")),
	}
	return m
}

// hostUpstreams reads the host's resolvers from /etc/resolv.conf, skipping any
// cornus bridge gateway (a 10.4.x.1 loop back into ourselves). Empty on error.
func hostUpstreams() []string {
	cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || cfg == nil {
		return nil
	}
	port := cfg.Port
	if port == "" {
		port = "53"
	}
	var out []string
	for _, s := range cfg.Servers {
		out = append(out, net.JoinHostPort(s, port))
	}
	return out
}

// reconcile updates the answer zones and (re)binds a resolver socket per network
// to its gateway IP, closing listeners for networks that are gone. Best-effort:
// a bind failure is logged and skipped (containers keep the host upstreams in
// their resolv.conf and peer names in /etc/hosts). No-op when disabled.
func (m *dnsManager) reconcile(gateways map[string]string, zones map[string]map[string][]net.IP) {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.zones = zones
	// Start listeners for newly-present networks.
	for network, gw := range gateways {
		if _, ok := m.listeners[network]; ok {
			continue
		}
		l, err := m.startListener(network, gw)
		if err != nil {
			m.log.Warn("bare dns: bind failed; resolution falls back to /etc/hosts + upstreams", "network", network, "gateway", gw, "error", err)
			continue
		}
		m.listeners[network] = l
	}
	// Stop listeners for networks no longer in use.
	for network, l := range m.listeners {
		if _, ok := gateways[network]; ok {
			continue
		}
		l.close()
		delete(m.listeners, network)
	}
}

// startListener binds UDP+TCP :53 on the network's gateway IP and serves queries
// scoped to that network's zone. Caller holds m.mu.
func (m *dnsManager) startListener(network, gateway string) (*dnsListener, error) {
	addr := net.JoinHostPort(gateway, "53")
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	h := dns.HandlerFunc(m.handlerFor(network))
	udp := &dns.Server{PacketConn: pc, Handler: h}
	tcp := &dns.Server{Listener: ln, Handler: h}
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	m.log.Info("bare dns listening", "network", network, "gateway", gateway)
	return &dnsListener{gateway: gateway, udp: udp, tcp: tcp}, nil
}

func (l *dnsListener) close() {
	if l.udp != nil {
		_ = l.udp.Shutdown()
	}
	if l.tcp != nil {
		_ = l.tcp.Shutdown()
	}
}

// close shuts every listener down (backend Close).
func (m *dnsManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for network, l := range m.listeners {
		l.close()
		delete(m.listeners, network)
	}
}

// handlerFor returns a query handler scoped to one network's zone.
func (m *dnsManager) handlerFor(network string) func(dns.ResponseWriter, *dns.Msg) {
	return func(w dns.ResponseWriter, req *dns.Msg) {
		if len(req.Question) != 1 {
			m.forward(w, req)
			return
		}
		q := req.Question[0]
		canon := dns.CanonicalName(q.Name)
		m.mu.Lock()
		ips := append([]net.IP(nil), m.zones[network][canon]...)
		m.mu.Unlock()
		if len(ips) == 0 {
			m.forward(w, req)
			return
		}
		reply := new(dns.Msg)
		reply.SetReply(req)
		reply.Authoritative = true
		if q.Qtype == dns.TypeA {
			// Round-robin: rotate the answer order so successive lookups spread
			// across replicas (the hosts file cannot — it publishes replica 0).
			off := int(m.rr.Add(1))
			for i := range ips {
				reply.Answer = append(reply.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL},
					A:   ips[(i+off)%len(ips)],
				})
			}
		}
		// A non-A query for an owned name gets an authoritative NODATA rather than
		// leaking upstream — we are the authority for this name.
		_ = w.WriteMsg(reply)
	}
}

// forward relays an unknown query to the first responsive host upstream; with
// none it answers SERVFAIL.
func (m *dnsManager) forward(w dns.ResponseWriter, req *dns.Msg) {
	m.mu.Lock()
	ups := m.upstreams
	m.mu.Unlock()
	c := &dns.Client{Timeout: 5 * time.Second}
	for _, up := range ups {
		if resp, _, err := c.Exchange(req, up); err == nil && resp != nil {
			_ = w.WriteMsg(resp)
			return
		}
	}
	fail := new(dns.Msg)
	fail.SetReply(req)
	fail.Rcode = dns.RcodeServerFailure
	_ = w.WriteMsg(fail)
}

// dnsZones builds the per-network answer table from the instance records: for
// each network an instance joins, its service name and aliases resolve to that
// instance's IP there (accumulated across replicas for round-robin).
func (b *Backend) dnsZones() map[string]map[string][]net.IP {
	recs, err := b.listRecords()
	if err != nil {
		return nil
	}
	zones := map[string]map[string][]net.IP{}
	for _, rec := range recs {
		if rec.App == "" {
			continue
		}
		p := peerFromRecord(rec)
		for _, nw := range p.Networks {
			ip := net.ParseIP(p.IPs[nw]).To4()
			if ip == nil {
				continue
			}
			z := zones[nw]
			if z == nil {
				z = map[string][]net.IP{}
				zones[nw] = z
			}
			for _, name := range append([]string{p.App}, p.Aliases[nw]...) {
				key := dns.CanonicalName(name)
				z[key] = append(z[key], ip)
			}
		}
	}
	return zones
}

// dnsGateways returns the gateway IP of every network that currently has an
// instance, keyed by network name (the addresses the resolver binds).
func (b *Backend) dnsGateways() map[string]string {
	recs, err := b.listRecords()
	if err != nil {
		return nil
	}
	inUse := map[string]bool{}
	for _, rec := range recs {
		for _, n := range rec.Networks {
			inUse[n] = true
		}
	}
	gws := map[string]string{}
	for n := range inUse {
		if gw, err := b.net.GatewayFor(n); err == nil && gw != "" {
			gws[n] = gw
		}
	}
	return gws
}

// reconcileDNS refreshes the resolver's zones and listeners from the current
// records (called after Apply/Delete, alongside syncHosts).
func (b *Backend) reconcileDNS() {
	if b.dns == nil || !b.dns.enabled {
		return
	}
	b.dns.reconcile(b.dnsGateways(), b.dnsZones())
}

// dnsNameservers returns the resolv.conf nameserver list for an instance on the
// given primary network: the cornus resolver (its gateway) first when DNS is
// enabled, then any compose `dns:` servers, then the host upstreams as fallback,
// de-duplicated. When DNS is disabled the gateway is omitted.
func (b *Backend) dnsNameservers(ctx context.Context, primaryNetwork string, specDNS []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if b.dns != nil && b.dns.enabled {
		if gw, err := b.net.GatewayFor(primaryNetwork); err == nil {
			add(gw)
		} else {
			logging.FromContext(ctx).WarnContext(ctx, "bare dns: no gateway for network; resolv.conf uses upstreams only", "network", primaryNetwork, "error", err)
		}
	}
	for _, s := range specDNS {
		add(s)
	}
	if b.dns != nil {
		for _, u := range b.dns.upstreams {
			// resolv.conf wants bare addresses; strip the :53 the upstream carries.
			host, _, err := net.SplitHostPort(u)
			if err != nil {
				host = u
			}
			add(host)
		}
	}
	if len(out) == 0 {
		// Last resort so the container is never left with an empty resolver.
		add("127.0.0.1")
	}
	return out
}
