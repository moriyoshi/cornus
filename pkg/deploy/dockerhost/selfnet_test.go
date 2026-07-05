package dockerhost

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// selfCtrID is the id of the container a containerized cornus runs in, for the
// tests below. Long and distinctive on purpose: sameContainer accepts a >=12
// character prefix in either direction, so an id that could prefix-match a
// workload id ("id-cornus-...") would make these tests agree with the code for
// the wrong reason.
const selfCtrID = "aaaaaaaaaaaabbbbbbbbbbbbccccccccccccdddddddddddd0000000000000000"

// netSpec is a one-network deployment spec, the shape that reproduces the bug:
// a workload on a user-defined network the server is not a member of.
func netSpec(nets ...string) api.DeploySpec {
	spec := api.DeploySpec{Name: "app", Image: "app:v1"}
	for _, n := range nets {
		spec.Networks = append(spec.Networks, api.NetworkAttachment{Name: n, Aliases: []string{"app"}})
	}
	return spec
}

// connectsOf returns the recorded "net:container" connects, under the lock.
func connectsOf(f *fakeDocker) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.connects...)
}

// TestApplyJoinsWorkloadNetworksWhenContainerized is the regression test for the
// reported bug: a cornus server running as a container on the daemon it drives
// had no route to a workload on a user-defined network, because Docker's
// inter-network isolation drops traffic between two bridge networks (measured on
// Docker 29.2.1 — container-on-bridge to container-on-user-network is
// unreachable while host-to-the-same-address is not). Apply must therefore
// attach the server's own container to every network it puts a workload on.
func TestApplyJoinsWorkloadNetworksWhenContainerized(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), netSpec("appnet", "backnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, want := range []string{"appnet:" + selfCtrID, "backnet:" + selfCtrID} {
		if !containsStr(connectsOf(f), want) {
			t.Fatalf("server did not join the workload's network: connects = %v, want %q", connectsOf(f), want)
		}
	}

	// The server is a guest on the workload's network, not a member of the
	// deployment: it must not take the deployment's DNS aliases with it, or
	// "app" would resolve to the cornus server for every workload on that
	// network.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, body := range f.connectBodies {
		if body.Container != selfCtrID {
			continue
		}
		if body.EndpointConfig != nil {
			t.Fatalf("the server's own network join carried endpoint config %v; it must be a bare connect", body.EndpointConfig)
		}
	}
}

// TestApplyJoinsNetworksIdempotently pins that a re-apply of the same
// deployment is not an error. dockerd answers a repeat connect with 403
// "endpoint ... already exists", and every apply of a long-lived deployment
// re-runs this — treating that as a failure would turn every second apply into
// a warning storm.
func TestApplyJoinsNetworksIdempotently(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	for i := 0; i < 3; i++ {
		if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
			t.Fatalf("Apply #%d: %v", i, err)
		}
	}
	f.mu.Lock()
	still := f.attached["appnet"][selfCtrID]
	f.mu.Unlock()
	if !still {
		t.Fatal("after three applies the server is no longer attached to appnet")
	}
}

// TestApplyDoesNotJoinNetworksOnHost pins the scope. A cornus running as a host
// process can already route to every docker bridge, so it must not attach itself
// to workload networks — an endpoint per deployment, for nothing.
func TestApplyDoesNotJoinNetworksOnHost(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithSelfContainerID(selfCtrID)) // no WithIsolatedNetwork
	if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, c := range connectsOf(f) {
		if strings.HasSuffix(c, ":"+selfCtrID) {
			t.Fatalf("a host-process server attached itself to a workload network: %q", c)
		}
	}
}

// TestApplyDoesNotJoinNetworksInRemoteMode pins the other half of the scope.
// Remote mode reaches workloads through a per-instance companion and its daemon
// need not be this machine's at all, so a network membership there buys nothing.
func TestApplyDoesNotJoinNetworksInRemoteMode(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID),
		WithRemote(true), WithAgentImage("cornus:test"))
	if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, c := range connectsOf(f) {
		if strings.HasSuffix(c, ":"+selfCtrID) {
			t.Fatalf("a remote-mode server attached itself to a workload network: %q", c)
		}
	}
}

// TestApplySucceedsWhenTheServerCannotJoin pins that the join is best-effort.
// The deployment is what the user asked for; the server's own route to it is a
// capability layered on top, and ForwardPort already explains its own absence.
// A daemon that refuses the connect must not discard a workload that started
// fine.
func TestApplySucceedsWhenTheServerCannotJoin(t *testing.T) {
	f := &fakeDocker{refuseConnectFor: selfCtrID}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
		t.Fatalf("Apply must survive a refused self-join, got %v", err)
	}
	if f.appCreates() != 1 {
		t.Fatalf("appCreates = %d, want 1 (the workload must still be created)", f.appCreates())
	}
}

// TestDeleteReapsNetworkTheServerJoined is the other half of the fix. cornus's
// own endpoint keeps a network non-empty, so the `docker compose down` network
// GC would stop working the moment the server started joining them — every
// deploy/delete cycle leaving a network and an endpoint behind. Delete must
// detach first, then reap.
func TestDeleteReapsNetworkTheServerJoined(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(context.Background(), "app"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !containsStr(f.disconnects, "appnet:"+selfCtrID) {
		t.Fatalf("the server did not leave appnet on delete: disconnects = %v", f.disconnects)
	}
	if !containsStr(f.netRemoved, "appnet") {
		t.Fatalf("appnet was not reaped: netRemoved = %v (still attached: %v)", f.netRemoved, f.attached["appnet"])
	}
}

// TestReapNetworkKeepsNetworkWithOtherMembers pins that leaving does not become
// removing: a network another deployment still uses must survive, even though
// cornus detached itself from it.
func TestReapNetworkKeepsNetworkWithOtherMembers(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	// Two deployments sharing one network; deleting the first must not take it.
	if _, err := b.Apply(context.Background(), netSpec("shared")); err != nil {
		t.Fatalf("Apply app: %v", err)
	}
	other := netSpec("shared")
	other.Name = "other"
	if _, err := b.Apply(context.Background(), other); err != nil {
		t.Fatalf("Apply other: %v", err)
	}
	if err := b.Delete(context.Background(), "app"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if containsStr(f.netRemoved, "shared") {
		t.Fatal("a network another deployment is still on was reaped")
	}
}

// TestReachableNetworksReportsTheServersOwnMemberships covers the input
// ForwardPort's address choice depends on.
func TestReachableNetworksReportsTheServersOwnMemberships(t *testing.T) {
	f := &fakeDocker{containers: map[string]map[string]any{
		selfCtrID: {"Id": selfCtrID, "State": "running", "nets": map[string]string{"bridge": "172.17.0.2", "appnet": "172.20.0.2"}},
	}}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	got := b.reachableNetworks(context.Background())
	if !got["bridge"] || !got["appnet"] || len(got) != 2 {
		t.Fatalf("reachableNetworks = %v, want {bridge, appnet}", got)
	}

	// A host-process server must report nil, which is what tells containerIP that
	// every network is routable and the historical preference order applies.
	onHost := newTestBackendOpts(t, f, WithSelfContainerID(selfCtrID))
	if got := onHost.reachableNetworks(context.Background()); got != nil {
		t.Fatalf("reachableNetworks on a host-process server = %v, want nil", got)
	}
}

// TestPickNetworkIPHonoursReachability is the address-choice half of the bug.
// Before the fix pickNetworkIP always preferred the default bridge "which the
// server host can route to" — true for a host process, and precisely backwards
// for a containerized server, for which the bridge is often the ONE network the
// workload is on that it cannot see.
func TestPickNetworkIPHonoursReachability(t *testing.T) {
	both := map[string]string{"bridge": "172.17.0.9", "appnet": "172.20.0.9"}

	// The server is on appnet only: the bridge address is unroutable for it, so
	// the appnet one must win despite bridge's historical preference.
	for i := 0; i < 100; i++ {
		if got := pickNetworkIP(both, map[string]bool{"appnet": true}); got != "172.20.0.9" {
			t.Fatalf("pickNetworkIP(reachable=appnet) = %q, want 172.20.0.9", got)
		}
	}
	// The server is on the bridge too: back to the historical preference.
	if got := pickNetworkIP(both, map[string]bool{"appnet": true, "bridge": true}); got != "172.17.0.9" {
		t.Fatalf("pickNetworkIP(reachable=both) = %q, want the bridge address", got)
	}
	// Disjoint sets are "no route", not "pick something and time out".
	if got := pickNetworkIP(both, map[string]bool{"unrelated": true}); got != "" {
		t.Fatalf("pickNetworkIP(no overlap) = %q, want empty", got)
	}
}

// TestForwardPortExplainsAnUnreachableWorkload pins the diagnostic for the case
// the fix cannot repair: a workload on the default bridge (no `networks:` in the
// spec) seen from a server that is not on it. Joining the default bridge is the
// one attachment this code refuses to make automatically — measured on Docker
// 29.2.1, connecting to the bridge from a user-defined network MOVES the
// container's default route, so cornus would silently re-home its own egress.
// The operator gets a message naming both remedies instead of a bare timeout.
func TestForwardPortExplainsAnUnreachableWorkload(t *testing.T) {
	f := &fakeDocker{containers: map[string]map[string]any{
		selfCtrID: {"Id": selfCtrID, "State": "running", "nets": map[string]string{"cornus_default": "172.20.0.2"}},
	}}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "app", Image: "app:v1"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.mu.Lock()
	for id, c := range f.containers {
		if id != selfCtrID {
			c["nets"] = map[string]string{"bridge": "172.17.0.5"}
		}
	}
	f.mu.Unlock()

	err := b.ForwardPort(context.Background(), "app", 80, "tcp", nopConn{})
	if err == nil {
		t.Fatal("ForwardPort to a workload on an unreachable network must fail")
	}
	// "no IP address" is the containerIP verdict, and asserting it is what makes
	// this a test of the reachability logic rather than of a dial that happens to
	// fail: without it the test would also pass on the pre-fix code, which picks
	// the bridge address and only fails minutes later at connect time. Reaching
	// this error means the address choice itself concluded there is no route.
	for _, want := range []string{"no IP address", "cannot route to", "own network namespace", "--network host", "CORNUS_DOCKER_REMOTE"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ForwardPort error %q does not name %q", err, want)
		}
	}
}

// TestForwardPortRejoinsAfterTheServerIsReplaced is the regression test for the
// worst of the follow-on landmines: a Docker endpoint belongs to a CONTAINER,
// not to an image or a name, so Apply's join survives `docker restart` and does
// NOT survive `docker rm` + `docker run` — which is exactly how a cornus server
// is upgraded. Measured: after a recreate the workloads keep running, healthy
// and untouched, while the new server container is back to `bridge` alone and
// every port-forward fails; and the error blamed the network namespace, pointing
// at `--network host` when the real remedy was "re-apply". ForwardPort must
// therefore be able to join on demand, from the container's own networks.
func TestForwardPortRejoinsAfterTheServerIsReplaced(t *testing.T) {
	// The server's own container has to be inspectable here, and on the default
	// bridge alone: that is the post-upgrade state, and it is also what makes the
	// test observe the fix. Without it reachableNetworks cannot resolve anything,
	// falls back to the historical "every network is routable" behaviour, and the
	// dial would be attempted (and this test would pass) with no join at all.
	f := &fakeDocker{containers: map[string]map[string]any{
		selfCtrID: {"Id": selfCtrID, "State": "running", "nets": map[string]string{defaultBridge: "172.17.0.2"}},
	}}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The upgrade: a NEW server container, on the default bridge alone, driving
	// the same daemon with the same workloads still running on appnet.
	f.mu.Lock()
	delete(f.attached["appnet"], selfCtrID)
	f.connects, f.disconnects = nil, nil
	for id, c := range f.containers {
		if id != selfCtrID {
			c["nets"] = map[string]string{"appnet": "172.20.0.9"}
		}
	}
	f.mu.Unlock()

	// It must resolve a routable address anyway — by joining, not by luck.
	// instanceIP rather than ForwardPort, so the assertion is about the address
	// resolution and the test does not spend seconds on a real dial to an address
	// no test host has.
	id, err := b.firstInstanceID(context.Background(), "app")
	if err != nil {
		t.Fatalf("firstInstanceID: %v", err)
	}
	ip, err := b.instanceIP(context.Background(), "app", id)
	if err != nil {
		t.Fatalf("a replaced server could not resolve a route to a running workload: %v", err)
	}
	if ip != "172.20.0.9" {
		t.Fatalf("instanceIP = %q, want the workload's appnet address", ip)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !containsStr(f.connects, "appnet:"+selfCtrID) {
		t.Fatalf("a replaced server did not re-join the running workload's network: connects = %v", f.connects)
	}
}

// TestForwardPortDoesNotRejoinTheDefaultBridge pins the exception. Joining the
// default bridge moves a container's default route (measured on Docker 29.2.1),
// so the on-demand path must leave that case alone exactly as the Apply-time
// path does — an explained error beats silently re-homing the server's egress.
func TestForwardPortDoesNotRejoinTheDefaultBridge(t *testing.T) {
	f := &fakeDocker{containers: map[string]map[string]any{
		selfCtrID: {"Id": selfCtrID, "State": "running", "nets": map[string]string{"cornus_default": "172.20.0.2"}},
	}}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "app", Image: "app:v1"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.mu.Lock()
	for id, c := range f.containers {
		if id != selfCtrID {
			c["nets"] = map[string]string{"bridge": "172.17.0.5"}
		}
	}
	f.mu.Unlock()

	if err := b.ForwardPort(context.Background(), "app", 80, "tcp", nopConn{}); err == nil {
		t.Fatal("ForwardPort to a bridge-only workload from a server not on the bridge must fail")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.connects {
		if c == defaultBridge+":"+selfCtrID {
			t.Fatal("the server attached itself to the default bridge, which moves its own default route")
		}
	}
}

// TestForwardPortDoesNotRejoinForAStoppedWorkload separates the two things a
// failed address resolution can mean. A stopped container still reports every
// network it was created on, each with an EMPTY address (measured on Docker
// 29.2.1) — so "no address" from a stopped workload looks exactly like "on a
// network I cannot see". Treating the first as the second would attach the
// server to networks it has no reason to be on, once per failed forward, for a
// deployment that is simply down.
func TestForwardPortDoesNotRejoinForAStoppedWorkload(t *testing.T) {
	f := &fakeDocker{containers: map[string]map[string]any{
		selfCtrID: {"Id": selfCtrID, "State": "running", "nets": map[string]string{defaultBridge: "172.17.0.2"}},
	}}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := b.Apply(context.Background(), netSpec("appnet")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Stop the workload: still on appnet by membership, but unaddressed. Drop
	// the server's own endpoint too, so the ONLY thing that could re-create it is
	// the on-demand path under test.
	f.mu.Lock()
	delete(f.attached["appnet"], selfCtrID)
	f.connects = nil
	for id, c := range f.containers {
		if id != selfCtrID {
			c["State"] = "stopped"
			c["nets"] = map[string]string{"appnet": ""}
		}
	}
	f.mu.Unlock()

	id, err := b.firstInstanceID(context.Background(), "app")
	if err != nil {
		t.Fatalf("firstInstanceID: %v", err)
	}
	if _, err := b.instanceIP(context.Background(), "app", id); err == nil {
		t.Fatal("a stopped workload must not resolve to an address")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.connects) != 0 {
		t.Fatalf("the server joined networks chasing a workload that is merely stopped: %v", f.connects)
	}
}

// TestAddressPoolHintNamesCornusOwnEndpoint covers the second landmine: cornus's
// endpoint consumes one address from the workload network's IPAM pool, ahead of
// the replicas. Measured on a /29 (six usable): five replicas fit where six used
// to, and the sixth fails at START with the daemon naming only the network. An
// operator counting their own replicas against their own subnet cannot discover
// the extra tenant from that, so the error has to say it.
func TestAddressPoolHintNamesCornusOwnEndpoint(t *testing.T) {
	f := &fakeDocker{startErr: `docker api: 500 Internal Server Error: {"message":"failed to set up container networking: no available IPv4 addresses on this network's address pools: appnet"}`}
	b := newTestBackendOpts(t, f, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	_, err := b.Apply(context.Background(), netSpec("appnet"))
	if err == nil {
		t.Fatal("Apply must fail when a replica cannot get an address")
	}
	for _, want := range []string{"no available IPv4 addresses", "holds one address from this network's pool", "widen the subnet"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Apply error %q does not name %q", err, want)
		}
	}

	// It must not editorialize on an unrelated start failure, nor on a
	// host-process server that holds no endpoint at all.
	other := &fakeDocker{startErr: `docker api: 500 Internal Server Error: {"message":"driver failed programming external connectivity"}`}
	ob := newTestBackendOpts(t, other, WithIsolatedNetwork(true), WithSelfContainerID(selfCtrID))
	if _, err := ob.Apply(context.Background(), netSpec("appnet")); err == nil {
		t.Fatal("expected the unrelated start failure to surface")
	} else if strings.Contains(err.Error(), "holds one address") {
		t.Fatalf("the address-pool hint fired on an unrelated failure: %v", err)
	}
	onHost := &fakeDocker{startErr: `docker api: 500 Internal Server Error: {"message":"no available IPv4 addresses on this network's address pools: appnet"}`}
	hb := newTestBackendOpts(t, onHost, WithSelfContainerID(selfCtrID)) // not containerized
	if _, err := hb.Apply(context.Background(), netSpec("appnet")); err == nil {
		t.Fatal("expected the start failure to surface")
	} else if strings.Contains(err.Error(), "holds one address") {
		t.Fatalf("a host-process server blamed an endpoint it does not have: %v", err)
	}
}

func containsStr(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// nopConn is a closed, inert ReadWriteCloser: ForwardPort must fail before it
// ever reads or writes in these tests.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)    { return 0, context.Canceled }
func (nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (nopConn) Close() error                { return nil }
