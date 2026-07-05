package caretaker

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// roundTripConfig marshals a Config and unmarshals it back, mimicking the
// env-var delivery the k8s backend uses.
func roundTripConfig(t *testing.T, in Config) Config {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeUpstream answers every A query with 9.9.9.9 and every other type with
// NODATA — stands in for the cluster DNS the caretaker forwards unknowns to.
func fakeUpstream(pc net.PacketConn) {
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		if len(req.Question) == 1 && req.Question[0].Qtype == dns.TypeA {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
				A:   net.IPv4(9, 9, 9, 9),
			})
		}
		_ = w.WriteMsg(m)
	})}
	_ = srv.ActivateAndServe()
}

// TestDNSRole drives the resolver through Go's own DNS client: an owned name
// resolves (bare and search-expanded) to its user-network IP, and an unowned
// name is forwarded to the upstream — the two behaviours the Multus modes need.
func TestDNSRole(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Upstream (cluster DNS stand-in).
	upPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upPC.Close()
	go fakeUpstream(upPC)

	// The caretaker DNS server: one peer record, plus the cluster search domain.
	srv := newDNSServer(DNSRole{
		Records:  map[string]string{"peer": "10.222.0.5"},
		Domain:   "cmns.svc.cluster.local",
		Upstream: upPC.LocalAddr().String(),
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.serve(ctx, pc)
	dnsAddr := pc.LocalAddr().String()

	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("udp", dnsAddr)
		},
	}
	lookup := func(name string) []string {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		addrs, err := r.LookupHost(c, name)
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		return addrs
	}

	// Owned name resolves to the user-network IP, both as the bare query (rooted
	// so the test host's own search domains don't interfere — in a pod the search
	// domain IS the cluster domain, which we also key on) and as the
	// search-expanded FQDN.
	if got := lookup("peer."); len(got) != 1 || got[0] != "10.222.0.5" {
		t.Errorf("lookup peer = %v, want [10.222.0.5]", got)
	}
	if got := lookup("peer.cmns.svc.cluster.local."); len(got) != 1 || got[0] != "10.222.0.5" {
		t.Errorf("lookup peer.<domain> = %v, want [10.222.0.5]", got)
	}
	// An unowned name is forwarded to the upstream (which answers 9.9.9.9).
	if got := lookup("elsewhere.example.com."); len(got) != 1 || got[0] != "9.9.9.9" {
		t.Errorf("lookup elsewhere = %v, want the forwarded [9.9.9.9]", got)
	}
}

// TestDNSConfigRoundTrip confirms the DNS role survives the env-var JSON.
func TestDNSConfigRoundTrip(t *testing.T) {
	in := Config{DNS: &DNSRole{
		Records:  map[string]string{"web": "10.222.0.2", "db": "10.222.0.3"},
		Upstream: "10.96.0.10:53",
		Domain:   "ns.svc.cluster.local",
	}}
	out := roundTripConfig(t, in)
	if out.DNS == nil || len(out.DNS.Records) != 2 || out.DNS.Records["web"] != "10.222.0.2" {
		t.Fatalf("dns round-trip mismatch: %+v", out.DNS)
	}
	if out.DNS.Upstream != "10.96.0.10:53" || out.DNS.Domain != "ns.svc.cluster.local" {
		t.Fatalf("dns upstream/domain lost: %+v", out.DNS)
	}
}

// TestDNSRoleOverlaidZone exercises the zone shape the Multus OVERLAID mode
// feeds the role (matrix row A'): the compose planner's records carry every
// member of the user network — the pod's own service name included — plus
// declared aliases, all pointing at SECONDARY (user-network) addresses. Each
// name must answer at its secondary address both bare and search-expanded, a
// non-A query of an owned name must yield authoritative NODATA (not a leak to
// upstream), and with no upstream configured (the detached-pod case) an
// unknown name gets an empty answer instead of an error.
func TestDNSRoleOverlaidZone(t *testing.T) {
	srv := newDNSServer(DNSRole{
		Records: map[string]string{
			"a":   "10.222.14.7", // the pod's own service name
			"b":   "10.222.14.9", // a peer
			"bee": "10.222.14.9", // the peer's declared alias
		},
		Domain: "cornus-e2e.svc.cluster.local",
		// No upstream: the own-records-only configuration a detached pod gets.
	})

	query := func(name string, qtype uint16) *dns.Msg {
		t.Helper()
		req := new(dns.Msg)
		req.SetQuestion(dns.CanonicalName(name), qtype)
		w := &fakeResponseWriter{}
		srv.handle(w, req)
		if w.msg == nil {
			t.Fatalf("no reply for %s", name)
		}
		return w.msg
	}

	for name, ip := range map[string]string{
		"a": "10.222.14.7", "b": "10.222.14.9", "bee": "10.222.14.9",
		"b.cornus-e2e.svc.cluster.local": "10.222.14.9",
	} {
		m := query(name, dns.TypeA)
		if !m.Authoritative || len(m.Answer) != 1 {
			t.Fatalf("%s: authoritative=%v answers=%d, want one authoritative A", name, m.Authoritative, len(m.Answer))
		}
		if a, ok := m.Answer[0].(*dns.A); !ok || a.A.String() != ip {
			t.Errorf("%s = %v, want the secondary address %s", name, m.Answer[0], ip)
		}
	}

	// An owned name queried for AAAA: authoritative NODATA, so the resolver
	// falls through to the A record instead of asking the cluster DNS (which
	// would answer with the PRIMARY address and put traffic back there).
	if m := query("b", dns.TypeAAAA); !m.Authoritative || len(m.Answer) != 0 {
		t.Errorf("AAAA b: authoritative=%v answers=%v, want authoritative NODATA", m.Authoritative, m.Answer)
	}
	// Unrelated names miss the zone; with no upstream that is an empty reply.
	if m := query("b.example.com", dns.TypeA); len(m.Answer) != 0 {
		t.Errorf("b.example.com answered %v, want nothing (not owned)", m.Answer)
	}
}

// queryServer runs one query through srv.handle and returns the captured reply.
func queryServer(t *testing.T, srv *dnsServer, name string, qtype uint16) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.CanonicalName(name), qtype)
	w := &fakeResponseWriter{}
	srv.handle(w, req)
	if w.msg == nil {
		t.Fatalf("no reply for %s", name)
	}
	return w.msg
}

// TestDNSRoleDynamicOverlay exercises the dynamic-record overlay the hub role's
// dynamic import discovery publishes into: a Set makes the name answer (bare and
// search-expanded) at the published IP with the same authoritative/NODATA
// semantics as a static record, a Remove withdraws it, and a static record of
// the same name SHADOWS the dynamic one (a deploy-time record is authoritative).
// The dns role itself is standalone here — the overlay is driven directly, no
// hub — proving the DNS side needs nothing but the overlay.
func TestDNSRoleDynamicOverlay(t *testing.T) {
	srv := newDNSServer(DNSRole{
		Records: map[string]string{"peer": "10.222.0.5"},
		Domain:  "cmns.svc.cluster.local",
		// No upstream: unknown names get an empty reply, so "not owned" is visible.
	})
	// Isolate from the process-wide overlay so other tests cannot interfere.
	overlay := &DynamicRecords{m: map[string]net.IP{}}
	srv.dynamic = overlay

	// Before any publish, a dynamic-looking name is NOT owned (empty overlay is
	// byte-identical to the static-only role).
	if m := queryServer(t, srv, "dynsvc", dns.TypeA); m.Authoritative || len(m.Answer) != 0 {
		t.Fatalf("dynsvc before publish: authoritative=%v answers=%v, want an unowned empty reply", m.Authoritative, m.Answer)
	}

	overlay.Set("dynsvc", "127.31.7.9")

	// The published name answers at its synthetic IP, bare and search-expanded.
	for _, name := range []string{"dynsvc", "dynsvc.cmns.svc.cluster.local"} {
		m := queryServer(t, srv, name, dns.TypeA)
		if !m.Authoritative || len(m.Answer) != 1 {
			t.Fatalf("%s: authoritative=%v answers=%d, want one authoritative A", name, m.Authoritative, len(m.Answer))
		}
		if a, ok := m.Answer[0].(*dns.A); !ok || a.A.String() != "127.31.7.9" {
			t.Errorf("%s = %v, want the dynamic 127.31.7.9", name, m.Answer[0])
		}
	}
	// A dynamically-owned name keeps the exact NODATA semantics: non-A queries
	// get an authoritative empty answer, not a leak to upstream.
	if m := queryServer(t, srv, "dynsvc", dns.TypeAAAA); !m.Authoritative || len(m.Answer) != 0 {
		t.Errorf("AAAA dynsvc: authoritative=%v answers=%v, want authoritative NODATA", m.Authoritative, m.Answer)
	}
	// An unrelated suffix is not search-expanded into the overlay.
	if m := queryServer(t, srv, "dynsvc.example.com", dns.TypeA); len(m.Answer) != 0 {
		t.Errorf("dynsvc.example.com answered %v, want nothing (not owned)", m.Answer)
	}

	// Static wins: publishing a dynamic record under a static name changes nothing.
	overlay.Set("peer", "127.99.99.99")
	for _, name := range []string{"peer", "peer.cmns.svc.cluster.local"} {
		m := queryServer(t, srv, name, dns.TypeA)
		if len(m.Answer) != 1 {
			t.Fatalf("%s: answers=%d, want the static record", name, len(m.Answer))
		}
		if a := m.Answer[0].(*dns.A); a.A.String() != "10.222.0.5" {
			t.Errorf("%s = %v, want the static 10.222.0.5 shadowing the dynamic record", name, a.A)
		}
	}

	// A withdrawn name stops resolving.
	overlay.Remove("dynsvc")
	if m := queryServer(t, srv, "dynsvc", dns.TypeA); m.Authoritative || len(m.Answer) != 0 {
		t.Errorf("dynsvc after remove: authoritative=%v answers=%v, want an unowned empty reply", m.Authoritative, m.Answer)
	}
}

// TestDNSDynamicOverlayConcurrent hammers the overlay with concurrent
// Set/Remove churn while queries run — the lock discipline of the dynamic
// overlay must be race-clean (run with -race).
func TestDNSDynamicOverlayConcurrent(t *testing.T) {
	srv := newDNSServer(DNSRole{
		Records: map[string]string{"static": "10.222.0.5"},
		Domain:  "cmns.svc.cluster.local",
	})
	overlay := &DynamicRecords{m: map[string]net.IP{}}
	srv.dynamic = overlay

	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "churn" + strconv.Itoa(w)
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				default:
				}
				if i%2 == 0 {
					overlay.Set(name, "127.5.5.5")
				} else {
					overlay.Remove(name)
				}
			}
		}()
	}
	for q := 0; q < 4; q++ {
		q := q
		wg.Add(1)
		go func() {
			defer wg.Done()
			names := []string{"static", "churn0", "churn1.cmns.svc.cluster.local", "churn" + strconv.Itoa(q)}
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				default:
				}
				req := new(dns.Msg)
				req.SetQuestion(dns.CanonicalName(names[i%len(names)]), dns.TypeA)
				srv.handle(&fakeResponseWriter{}, req)
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(done)
	wg.Wait()

	// The static record was never perturbed by the churn.
	if m := queryServer(t, srv, "static", dns.TypeA); len(m.Answer) != 1 {
		t.Fatalf("static record lost under churn: %v", m.Answer)
	}
}

// fakeResponseWriter captures the handler's reply without a network.
type fakeResponseWriter struct {
	msg *dns.Msg
}

func (f *fakeResponseWriter) LocalAddr() net.Addr       { return &net.UDPAddr{IP: net.IPv4zero} }
func (f *fakeResponseWriter) RemoteAddr() net.Addr      { return &net.UDPAddr{IP: net.IPv4zero} }
func (f *fakeResponseWriter) WriteMsg(m *dns.Msg) error { f.msg = m; return nil }
func (f *fakeResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (f *fakeResponseWriter) Close() error              { return nil }
func (f *fakeResponseWriter) TsigStatus() error         { return nil }
func (f *fakeResponseWriter) TsigTimersOnly(bool)       {}
func (f *fakeResponseWriter) Hijack()                   {}
