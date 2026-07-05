//go:build linux

package barehost

import (
	"net"
	"os"
	"strings"
	"testing"

	"cornus/pkg/deploy/internal/hostrun"
	"github.com/miekg/dns"
)

// capWriter is a dns.ResponseWriter that captures the reply message for tests.
type capWriter struct{ msg *dns.Msg }

func (c *capWriter) LocalAddr() net.Addr       { return &net.UDPAddr{} }
func (c *capWriter) RemoteAddr() net.Addr      { return &net.UDPAddr{IP: net.IPv4(10, 4, 0, 5)} }
func (c *capWriter) WriteMsg(m *dns.Msg) error { c.msg = m; return nil }
func (c *capWriter) Write([]byte) (int, error) { return 0, nil }
func (c *capWriter) Close() error              { return nil }
func (c *capWriter) TsigStatus() error         { return nil }
func (c *capWriter) TsigTimersOnly(bool)       {}
func (c *capWriter) Hijack()                   {}

func query(name string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.CanonicalName(name), qtype)
	return m
}

// newZonedManager builds a DNS manager with a single network's zone and no
// upstreams (so a forward answers SERVFAIL, never touching the network).
func newZonedManager(network string, zone map[string][]net.IP) *dnsManager {
	m := newDNSManager(true)
	m.upstreams = nil
	m.zones = map[string]map[string][]net.IP{network: zone}
	return m
}

func TestDNSHandlerAnswersKnownName(t *testing.T) {
	m := newZonedManager("default", map[string][]net.IP{
		"web.": {net.IPv4(10, 4, 0, 2).To4()},
	})
	w := &capWriter{}
	m.handlerFor("default")(w, query("web", dns.TypeA))
	if w.msg == nil || len(w.msg.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %+v", w.msg)
	}
	if !w.msg.Authoritative {
		t.Error("reply should be authoritative for an owned name")
	}
	a, ok := w.msg.Answer[0].(*dns.A)
	if !ok || a.A.String() != "10.4.0.2" {
		t.Errorf("answer = %v, want A 10.4.0.2", w.msg.Answer[0])
	}
}

func TestDNSHandlerRoundRobin(t *testing.T) {
	m := newZonedManager("default", map[string][]net.IP{
		"web.": {net.IPv4(10, 4, 0, 2).To4(), net.IPv4(10, 4, 0, 3).To4()},
	})
	// Both replica IPs must appear in the answer, and the leading answer must
	// rotate across successive queries (load spreading the hosts file can't do).
	first := ""
	rotated := false
	for i := 0; i < 4; i++ {
		w := &capWriter{}
		m.handlerFor("default")(w, query("web", dns.TypeA))
		if len(w.msg.Answer) != 2 {
			t.Fatalf("expected 2 A records (both replicas), got %d", len(w.msg.Answer))
		}
		lead := w.msg.Answer[0].(*dns.A).A.String()
		if i == 0 {
			first = lead
		} else if lead != first {
			rotated = true
		}
	}
	if !rotated {
		t.Error("expected the leading A record to rotate across queries")
	}
}

func TestDNSHandlerUnknownNameForwardsNoUpstreamServfail(t *testing.T) {
	m := newZonedManager("default", map[string][]net.IP{"web.": {net.IPv4(10, 4, 0, 2).To4()}})
	w := &capWriter{}
	m.handlerFor("default")(w, query("nope.example.com", dns.TypeA))
	if w.msg == nil || w.msg.Rcode != dns.RcodeServerFailure {
		t.Errorf("unknown name with no upstream should SERVFAIL, got %+v", w.msg)
	}
}

func TestDNSHandlerNonAOwnedNameNodata(t *testing.T) {
	m := newZonedManager("default", map[string][]net.IP{"web.": {net.IPv4(10, 4, 0, 2).To4()}})
	w := &capWriter{}
	m.handlerFor("default")(w, query("web", dns.TypeAAAA))
	if w.msg == nil {
		t.Fatal("expected a reply")
	}
	// Authoritative NODATA: owned name, no answer, not forwarded to SERVFAIL.
	if !w.msg.Authoritative || len(w.msg.Answer) != 0 || w.msg.Rcode != dns.RcodeSuccess {
		t.Errorf("AAAA for owned name should be authoritative NODATA, got %+v", w.msg)
	}
}

func TestDNSZonesFromRecords(t *testing.T) {
	b, _ := newTestBackend(t)
	seedNetworked(t, b, "web", 0, "10.4.0.2", nil)
	seedNetworked(t, b, "web", 1, "10.4.0.3", nil)
	seedNetworked(t, b, "cache", 0, "10.4.0.4", map[string][]string{hostrun.DefaultNetwork: {"redis"}})

	zones := b.dnsZones()
	z := zones[hostrun.DefaultNetwork]
	if z == nil {
		t.Fatal("no zone for the default network")
	}
	// web resolves to BOTH replica IPs (round-robin fan-out).
	if got := z[dns.CanonicalName("web")]; len(got) != 2 {
		t.Errorf("web -> %v, want 2 replica IPs", got)
	}
	// cache resolves under its name and its alias.
	if got := z[dns.CanonicalName("cache")]; len(got) != 1 || got[0].String() != "10.4.0.4" {
		t.Errorf("cache -> %v, want [10.4.0.4]", got)
	}
	if got := z[dns.CanonicalName("redis")]; len(got) != 1 || got[0].String() != "10.4.0.4" {
		t.Errorf("redis alias -> %v, want [10.4.0.4]", got)
	}
}

func TestDNSNameserversGatewayFirst(t *testing.T) {
	b, _ := newTestBackend(t)
	b.dns.enabled = true
	b.dns.upstreams = []string{"1.1.1.1:53"}
	// Allocate the default network so gatewayFor resolves (10.4.0.1).
	if err := b.net.EnsureNetworks([]string{hostrun.DefaultNetwork}); err != nil {
		t.Fatalf("ensureNetworks: %v", err)
	}
	ns := b.dnsNameservers(t.Context(), hostrun.DefaultNetwork, []string{"8.8.8.8"})
	if len(ns) < 3 || ns[0] != "10.4.0.1" {
		t.Fatalf("nameservers = %v, want gateway 10.4.0.1 first", ns)
	}
	// compose dns then host upstream (stripped of :53) follow.
	if ns[1] != "8.8.8.8" || ns[2] != "1.1.1.1" {
		t.Errorf("nameservers = %v, want [10.4.0.1 8.8.8.8 1.1.1.1]", ns)
	}
}

func TestResolvStoreRenders(t *testing.T) {
	r := newResolvStore(t.TempDir())
	path, err := r.create("cornus-web-0", []string{"10.4.0.1", "1.1.1.1"}, []string{"corp.local"}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"nameserver 10.4.0.1", "nameserver 1.1.1.1", "search corp.local"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("resolv.conf missing %q:\n%s", want, data)
		}
	}
}
