package caretaker

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"cornus/pkg/logging"
)

// DNSRole is a tiny per-pod DNS resolver. It answers A queries for known peer
// names (Records: bare service name -> IPv4) authoritatively and forwards
// everything else to Upstream (the cluster DNS). It exists for Multus network
// modes where the pod must resolve peers to their SECONDARY (user-network) IPs —
// which CoreDNS never publishes, since Kubernetes Endpoints only carry the
// primary cluster IP — or where the pod is detached from the cluster network and
// CoreDNS is otherwise unreachable. The pod's dnsConfig points its resolver at
// this server (127.0.0.1:53), so a bare `peer` lookup returns the peer's
// user-network address and the connection rides the user network.
//
// Domain is the cluster search domain of the pod's namespace (e.g.
// "ns.svc.cluster.local"); a record for "peer" is matched for both the bare
// query "peer" and the search-expanded "peer.<Domain>", and for nothing else, so
// unrelated names (peer.example.com) are forwarded untouched.
//
// Besides the static Records, the resolver consults the process-wide dynamic
// overlay (DynamicDNS): when the same caretaker also runs a hub role with
// ReachDynamic, every dynamically imported service is published there as
// name -> synthetic loopback IP the moment its reach listener binds, and
// withdrawn when it unbinds — so `dial(peer)` works for names unknown at deploy
// time. Static Records WIN on conflict: a deploy-time record is authoritative
// and shadows a dynamic record of the same name. Standalone (no hub role in the
// process) the overlay is simply empty and behavior is exactly the static
// table's.
type DNSRole struct {
	Listen   string            `json:"listen,omitempty"`   // default ":53"
	Records  map[string]string `json:"records,omitempty"`  // bare service name -> IPv4
	Upstream string            `json:"upstream,omitempty"` // host:port of the cluster DNS for unknowns
	Domain   string            `json:"domain,omitempty"`   // cluster search domain, e.g. "ns.svc.cluster.local"
}

// DynamicRecords is a concurrency-safe overlay of dynamically discovered
// records over a DNS role's static table: name -> IPv4, keyed by the bare
// canonical name (lower-cased, no trailing dot). The dns role checks its static
// table FIRST, so a static (deploy-time) record always shadows a dynamic one of
// the same name; domain search-expansion is applied at lookup time by the
// server (which knows its own Domain), so writers publish bare names only.
// The zero value is ready to use; Set/Remove may race freely with lookups.
type DynamicRecords struct {
	mu sync.RWMutex
	m  map[string]net.IP
}

// Set publishes (or updates) a dynamic record: name resolves to ip (an IPv4
// dotted quad). Non-IPv4 values and empty names are ignored (only A records are
// served, matching the static table).
func (r *DynamicRecords) Set(name, ip string) {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return
	}
	key := strings.TrimSuffix(dns.CanonicalName(name), ".")
	if key == "" {
		return
	}
	r.mu.Lock()
	if r.m == nil {
		r.m = map[string]net.IP{}
	}
	r.m[key] = parsed
	r.mu.Unlock()
}

// Remove withdraws a dynamic record. Removing an absent name is a no-op.
func (r *DynamicRecords) Remove(name string) {
	key := strings.TrimSuffix(dns.CanonicalName(name), ".")
	r.mu.Lock()
	delete(r.m, key)
	r.mu.Unlock()
}

// lookup resolves a canonical (FQDN-form) query name against the overlay,
// matching both the bare name and, when domainSuffix is set (".<domain>", no
// trailing dot), the search-expanded form. Allocation-free: suffix trims are
// substrings.
func (r *DynamicRecords) lookup(canon, domainSuffix string) (net.IP, bool) {
	name := strings.TrimSuffix(canon, ".")
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ip, ok := r.m[name]; ok {
		return ip, true
	}
	if domainSuffix != "" {
		if bare := strings.TrimSuffix(name, domainSuffix); bare != name {
			if ip, ok := r.m[bare]; ok {
				return ip, true
			}
		}
	}
	return nil, false
}

// dnsDynamic is the process-wide dynamic-record overlay — the rendezvous
// between the caretaker's dns role and the hub role's dynamic import discovery
// (runDynamicReach). A caretaker process serves exactly ONE pod (Config carries
// at most one dns role and one hub role), so a single shared overlay IS the
// pod's dynamic zone, and the two roles need no Config-level plumbing to find
// each other. With no hub role in the process it stays empty and the dns role
// serves exactly its static configuration.
var dnsDynamic = &DynamicRecords{}

// DynamicDNS returns the process-wide dynamic-record overlay the caretaker's
// dns role serves alongside its static table. The hub role's dynamic-reach
// machinery publishes into it; in-process compositions embedding the caretaker
// may use it the same way. Static records always win over dynamic ones.
func DynamicDNS() *DynamicRecords { return dnsDynamic }

// dnsServer is the resolved runtime form of a DNSRole: a table from every
// accepted (canonical, fully-qualified) query name to its answer IP, plus the
// dynamic overlay consulted on static misses.
type dnsServer struct {
	table        map[string]net.IP
	dynamic      *DynamicRecords // overlay for dynamically discovered names; static wins
	domainSuffix string          // ".<domain>" (canonical, no trailing dot); "" when no Domain
	upstream     string
	client       *dns.Client
}

// newDNSServer resolves a DNSRole into its runtime lookup table (each record
// keyed by both its bare name and, when Domain is set, the search-expanded name,
// each canonicalised to the FQDN form the wire carries) and attaches the
// process-wide dynamic overlay.
func newDNSServer(d DNSRole) *dnsServer {
	srv := &dnsServer{
		table:    map[string]net.IP{},
		dynamic:  dnsDynamic,
		upstream: d.Upstream,
		client:   &dns.Client{Timeout: 5 * time.Second},
	}
	if d.Domain != "" {
		srv.domainSuffix = "." + strings.TrimSuffix(dns.CanonicalName(d.Domain), ".")
	}
	for name, ipStr := range d.Records {
		ip := net.ParseIP(ipStr).To4()
		if ip == nil {
			continue // only A records are served
		}
		bare := strings.TrimSuffix(name, ".")
		srv.table[dns.CanonicalName(bare)] = ip
		if d.Domain != "" {
			srv.table[dns.CanonicalName(bare+"."+strings.TrimSuffix(d.Domain, "."))] = ip
		}
	}
	return srv
}

// runDNS serves the DNS role until ctx is cancelled.
func runDNS(ctx context.Context, d DNSRole) error {
	listen := d.Listen
	if listen == "" {
		listen = ":53"
	}
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return err
	}
	logging.FromContext(ctx).InfoContext(ctx, "caretaker dns listening", "listen", listen, "records", len(d.Records), "upstream", d.Upstream)
	return newDNSServer(d).serve(ctx, pc)
}

// serve answers queries on pc until ctx is cancelled.
func (s *dnsServer) serve(ctx context.Context, pc net.PacketConn) error {
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(s.handle)}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown()
	}()
	err := srv.ActivateAndServe()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// handle answers one query: an A record we own is synthesized, a non-A query for
// an owned name gets an authoritative empty answer (NODATA), and anything else is
// forwarded to the upstream resolver. Ownership is the static table first (a
// deploy-time record is authoritative and shadows a dynamic one), then the
// dynamic overlay; dynamic names only ADD positive answers — unknown names keep
// the exact forward/NODATA semantics above.
func (s *dnsServer) handle(w dns.ResponseWriter, req *dns.Msg) {
	mt := metrics()
	if len(req.Question) != 1 {
		mt.dnsQueries.Add(context.Background(), 1, dnsAttr("forward"))
		s.forward(w, req)
		return
	}
	q := req.Question[0]
	canon := dns.CanonicalName(q.Name)
	ip, owned := s.table[canon]
	if !owned && s.dynamic != nil {
		ip, owned = s.dynamic.lookup(canon, s.domainSuffix)
	}
	if !owned {
		mt.dnsQueries.Add(context.Background(), 1, dnsAttr("forward"))
		s.forward(w, req)
		return
	}
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	if q.Qtype == dns.TypeA {
		mt.dnsQueries.Add(context.Background(), 1, dnsAttr("local"))
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
			A:   ip,
		})
	} else {
		mt.dnsQueries.Add(context.Background(), 1, dnsAttr("nodata"))
	}
	// Non-A query for an owned name: authoritative NODATA rather than leaking to
	// upstream (we are the authority for this name).
	_ = w.WriteMsg(m)
}

// forward relays a query to the upstream resolver and copies its reply back;
// with no upstream configured it answers NODATA.
func (s *dnsServer) forward(w dns.ResponseWriter, req *dns.Msg) {
	if s.upstream == "" {
		m := new(dns.Msg)
		m.SetReply(req)
		_ = w.WriteMsg(m)
		return
	}
	resp, _, err := s.client.Exchange(req, s.upstream)
	if err != nil || resp == nil {
		m := new(dns.Msg)
		m.SetReply(req)
		m.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(m)
		return
	}
	_ = w.WriteMsg(resp)
}
