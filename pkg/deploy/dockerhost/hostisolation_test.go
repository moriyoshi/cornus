package dockerhost

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// mvSpec is a workload on a macvlan network, optionally alongside a bridge one.
func mvSpec(nets ...string) api.DeploySpec {
	spec := api.DeploySpec{Name: "app", Image: "app:v1"}
	for _, n := range nets {
		spec.Networks = append(spec.Networks, api.NetworkAttachment{Name: n})
	}
	return spec
}

// TestHostServerPrefersARoutableAddressOverAMacvlanOne is the host-process
// counterpart of the containerized routing bug, and it is NOT hypothetical: a
// macvlan (or l2 ipvlan) child cannot talk to its own parent interface, by
// kernel design. Measured on Docker 29.2.1 — an nginx on a macvlan network is
// unreachable from the host on :80, where the same container on a bridge network
// is reachable.
//
// The trap is the tie-break. A workload on BOTH has two addresses, one dialable
// and one not, and pickNetworkIP's fallback orders by network NAME — so which
// one ForwardPort chose was decided alphabetically. Here "a-macvlan" sorts
// first, which is exactly the arrangement that used to pick the dead address.
func TestHostServerPrefersARoutableAddressOverAMacvlanOne(t *testing.T) {
	f := &fakeDocker{netDrivers: map[string]string{"a-macvlan": "macvlan", "z-bridge": "bridge"}}
	b := newTestBackendOpts(t, f) // host process: no WithIsolatedNetwork
	if _, err := b.Apply(context.Background(), mvSpec("a-macvlan", "z-bridge")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.mu.Lock()
	for id, c := range f.containers {
		c["nets"] = map[string]string{"a-macvlan": "192.168.10.240", "z-bridge": "172.20.0.9"}
		_ = id
	}
	f.mu.Unlock()

	id, err := b.firstInstanceID(context.Background(), "app")
	if err != nil {
		t.Fatalf("firstInstanceID: %v", err)
	}
	for i := 0; i < 50; i++ { // map order is randomized; the choice must not be
		ip, err := b.instanceIP(context.Background(), "app", id)
		if err != nil {
			t.Fatalf("instanceIP: %v", err)
		}
		if ip != "172.20.0.9" {
			t.Fatalf("instanceIP = %q, want the bridge address (the macvlan one is unreachable from the host)", ip)
		}
	}
}

// TestHostServerExplainsAMacvlanOnlyWorkload covers the case the preference
// cannot rescue. Dialing anyway meant a bare timeout minutes later with nothing
// naming the cause; the workload is healthy and simply not reachable from the
// machine asking.
func TestHostServerExplainsAMacvlanOnlyWorkload(t *testing.T) {
	f := &fakeDocker{netDrivers: map[string]string{"lan": "macvlan"}}
	b := newTestBackendOpts(t, f)
	if _, err := b.Apply(context.Background(), mvSpec("lan")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.mu.Lock()
	for _, c := range f.containers {
		c["nets"] = map[string]string{"lan": "192.168.10.240"}
	}
	f.mu.Unlock()

	id, err := b.firstInstanceID(context.Background(), "app")
	if err != nil {
		t.Fatalf("firstInstanceID: %v", err)
	}
	_, err = b.instanceIP(context.Background(), "app", id)
	if err == nil {
		t.Fatal("a macvlan-only workload must not resolve to an address a host process cannot dial")
	}
	for _, want := range []string{"host-isolated", "macvlan/ipvlan", "publish a port"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}

// TestHostServerUnchangedForOrdinaryBridgeNetworks is the guard on the cost of
// the above. The overwhelmingly normal deployment is all-bridge, and for it the
// narrowing must produce NO opinion at all — same address choice as before, and
// the driver of each network looked up once and then remembered, because
// ForwardPort runs per CONNECTION and an uncached inspect would ride every one.
func TestHostServerUnchangedForOrdinaryBridgeNetworks(t *testing.T) {
	f := &fakeDocker{netDrivers: map[string]string{"appnet": "bridge"}}
	b := newTestBackendOpts(t, f)
	if _, err := b.Apply(context.Background(), mvSpec("appnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.mu.Lock()
	for _, c := range f.containers {
		c["nets"] = map[string]string{"bridge": "172.17.0.4", "appnet": "172.20.0.9"}
	}
	f.netInspects = 0
	f.mu.Unlock()

	id, err := b.firstInstanceID(context.Background(), "app")
	if err != nil {
		t.Fatalf("firstInstanceID: %v", err)
	}
	f.mu.Lock()
	f.ctrInspects = 0
	f.mu.Unlock()
	for i := 0; i < 10; i++ {
		ip, err := b.instanceIP(context.Background(), "app", id)
		if err != nil {
			t.Fatalf("instanceIP: %v", err)
		}
		// The historical preference: the default bridge, not the lexicographically
		// first name. Unchanged for a host-process server.
		if ip != "172.17.0.4" {
			t.Fatalf("instanceIP = %q, want the default-bridge address (historical behaviour)", ip)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.netInspects > 2 {
		t.Fatalf("network driver looked up %d times across 10 forwards; it must be cached (a network's driver cannot change)", f.netInspects)
	}
	// The claim the whole containerAddresses/selectIP split exists to keep: one
	// container inspect per forward, not two. ForwardPort runs per CONNECTION, so
	// "learn the networks, then resolve an address" would have doubled the daemon
	// traffic of every forward on the default topology to serve a rare one.
	if f.ctrInspects != 10 {
		t.Fatalf("10 forwards cost %d container inspects, want 10 (one each)", f.ctrInspects)
	}
}

// TestRemoteDaemonWithoutRemoteModeIsExplained covers the other host-process
// topology with no diagnostic: a server pointed at a daemon over TCP. If that
// daemon is on another machine its container IPs mean nothing locally, and the
// dial can only time out. The existing hint was gated on being CONTAINERIZED, so
// this case produced a bare timeout and no pointer to CORNUS_DOCKER_REMOTE — the
// setting that exists for exactly it.
func TestRemoteDaemonWithoutRemoteModeIsExplained(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f) // newTestBackendOpts points DOCKER_HOST at tcp://
	if !b.api.nonLocal() {
		t.Fatal("test setup: expected the fake daemon to be reached over TCP")
	}
	hint := b.unreachableHint()
	for _, want := range []string{"over TCP", "CORNUS_DOCKER_REMOTE"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q does not name %q", hint, want)
		}
	}

	// Not in remote mode's own error path: there the companion IS the answer and
	// telling the operator to enable what is already enabled would be noise.
	rb := newTestBackendOpts(t, f, WithRemote(true))
	if h := rb.unreachableHint(); h != "" {
		t.Fatalf("remote mode must not suggest enabling remote mode: %q", h)
	}
}
