//go:build linux

package barehost

// The resolver's listener lifecycle. Binding :53 on a bridge gateway needs
// privilege, so the bind SUCCESS path belongs to the E2E harness; what unit tests
// can and must pin is everything around it — that a bind failure degrades instead
// of failing the deploy, that listeners for departed networks are released, and
// which networks are offered for binding at all.

import (
	"net"
	"testing"

	"cornus/pkg/deploy/internal/hostrun"
)

func TestDNSReconcileIsANoOpWhenDisabled(t *testing.T) {
	m := newDNSManager(false)
	m.reconcile(map[string]string{"default": "10.4.0.1"}, map[string]map[string][]net.IP{
		"default": {"web.": {net.IPv4(10, 4, 0, 2)}},
	})
	if len(m.listeners) != 0 {
		t.Errorf("a disabled resolver bound %d listeners, want none", len(m.listeners))
	}
	if len(m.zones) != 0 {
		t.Errorf("a disabled resolver kept zones %v, want none published", m.zones)
	}
}

// TestDNSReconcileSurvivesABindFailure is the graceful-degradation contract the
// whole feature leans on: containers keep the host upstreams in resolv.conf and
// peer names in /etc/hosts, so a resolver that cannot bind must be skipped, not
// fatal. (203.0.113.1 is TEST-NET-3 — never a local address — so the bind fails
// on any host, privileged or not.)
func TestDNSReconcileSurvivesABindFailure(t *testing.T) {
	m := newDNSManager(true)
	zones := map[string]map[string][]net.IP{"default": {"web.": {net.IPv4(10, 4, 0, 2)}}}
	m.reconcile(map[string]string{"default": "203.0.113.1"}, zones)

	if len(m.listeners) != 0 {
		t.Errorf("an unbindable gateway produced %d listeners, want none", len(m.listeners))
	}
	// The zone is still published: the handler is reachable through any listener
	// that DID bind, and the next reconcile must not have to rebuild it.
	if len(m.zones["default"]) != 1 {
		t.Errorf("zones = %v, want the answer table published regardless of the bind", m.zones)
	}
}

// TestDNSReconcileReleasesDepartedNetworks covers the other half of the
// reconcile: when the last instance on a network goes away its socket must be
// given up, or the gateway address stays bound after the bridge is reaped.
func TestDNSReconcileReleasesDepartedNetworks(t *testing.T) {
	m := newDNSManager(true)
	// Stand in for a bound listener without needing the privilege to bind one.
	m.listeners["retired"] = &dnsListener{gateway: "10.4.9.1"}
	m.listeners["kept"] = &dnsListener{gateway: "10.4.8.1"}

	m.reconcile(map[string]string{"kept": "10.4.8.1"}, nil)

	if _, ok := m.listeners["retired"]; ok {
		t.Error("the listener for a departed network was not released")
	}
	if _, ok := m.listeners["kept"]; !ok {
		t.Error("a still-in-use network's listener must be left alone (never rebound)")
	}
}

// TestDNSCloseReleasesEveryListener covers server shutdown: the sockets are
// process-bound, so a restarted server must be able to rebind them.
func TestDNSCloseReleasesEveryListener(t *testing.T) {
	m := newDNSManager(true)
	m.listeners["a"] = &dnsListener{gateway: "10.4.0.1"}
	m.listeners["b"] = &dnsListener{gateway: "10.4.1.1"}

	m.close()

	if len(m.listeners) != 0 {
		t.Errorf("close left %d listeners, want none", len(m.listeners))
	}
	m.close() // idempotent: Close may run after a failed startup
}

// TestDNSGatewaysCoverOnlyNetworksInUse pins which addresses the resolver offers
// to bind: a network no instance is on must not keep a socket alive.
func TestDNSGatewaysCoverOnlyNetworksInUse(t *testing.T) {
	b, _ := newTestBackend(t)
	if got := b.dnsGateways(); len(got) != 0 {
		t.Errorf("with no instances, gateways = %v, want none", got)
	}

	seedNetworked(t, b, "web", 0, "10.4.0.2", nil)
	if err := b.net.EnsureNetworks([]string{hostrun.DefaultNetwork}); err != nil {
		t.Fatalf("EnsureNetworks: %v", err)
	}

	got := b.dnsGateways()
	if len(got) != 1 || got[hostrun.DefaultNetwork] != "10.4.0.1" {
		t.Errorf("gateways = %v, want just the default network's 10.4.0.1", got)
	}
}

// TestReconcileDNSIsSkippedWhenDisabled keeps the backend-level entry point in
// step with the manager: with CORNUS_BARE_DNS off nothing is bound and nothing
// is published, even after a deploy.
func TestReconcileDNSIsSkippedWhenDisabled(t *testing.T) {
	b, _ := newTestBackend(t) // the fixture disables DNS
	seedNetworked(t, b, "web", 0, "10.4.0.2", nil)

	b.reconcileDNS()

	if len(b.dns.listeners) != 0 || len(b.dns.zones) != 0 {
		t.Errorf("disabled resolver reconciled to listeners=%v zones=%v", b.dns.listeners, b.dns.zones)
	}
}
