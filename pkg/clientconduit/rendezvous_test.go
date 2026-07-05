package clientconduit

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/conduithost"
	"cornus/pkg/socks5"
)

// tunnelDialer records which deployment each CONNECT asked for, and answers with a
// pipe carrying that name — so a test can prove not only that a name resolved, but
// which conduit's registration answered it.
type tunnelDialer struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (d *tunnelDialer) PortForward(_ context.Context, service string, port int, _ string) (net.Conn, error) {
	d.mu.Lock()
	d.seen = append(d.seen, fmt.Sprintf("%s:%d", service, port))
	d.mu.Unlock()
	here, there := net.Pipe()
	go func() {
		_, _ = there.Write([]byte(d.name + "/" + service))
		_ = there.Close()
	}()
	return here, nil
}

func (d *tunnelDialer) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

func testRegistry(t *testing.T) *conduithost.Registry {
	t.Helper()
	dir, err := os.MkdirTemp("", "cnd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return conduithost.NewRegistry(dir)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// socksGet performs a SOCKS5 CONNECT through proxyAddr and returns whatever the
// far side wrote.
func socksGet(t *testing.T, proxyAddr, host string, port int) (string, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
		return "", err
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return "", err
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		return "", err
	}
	if reply[1] != 0x00 {
		return "", fmt.Errorf("socks5 reply code %d", reply[1])
	}
	b, _ := io.ReadAll(c)
	return string(b), nil
}

// The headline behaviour of the whole redesign, end to end and in one place: two
// independent conduits ask for the same address, the second JOINS rather than
// forking a second proxy, and a name registered by the JOINER resolves through the
// HOST's proxy — reaching the joiner's own workload.
//
// Before this, "join" was a map lookup inside one agent process, so a conduit in
// any other process was unjoinable by construction.
func TestSecondConduitJoinsAndItsNamesResolveThroughTheHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))

	hostDialer := &tunnelDialer{name: "hostside"}
	cfg := Config{Mode: ModeSocks5, Socks5Listen: addr}
	host, err := Start(ctx, hostDialer, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatalf("first conduit: %v", err)
	}
	defer host.Close()

	joinDialer := &tunnelDialer{name: "joinside"}
	joined, err := Start(ctx, joinDialer, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatalf("second conduit: %v", err)
	}
	defer joined.Close()

	// The second one did not bind anything of its own.
	rc, ok := joined.(*rendezvousConduit)
	if !ok {
		t.Fatalf("second conduit is %T, want a rendezvousConduit", joined)
	}
	if rc.Participant().Hosting() {
		t.Fatal("the second conduit hosted its own proxy instead of joining the first")
	}
	// And it reports the address a browser must actually point at.
	if got := rc.Banner(); len(got) == 0 || !strings.Contains(got[0], addr) {
		t.Errorf("joiner banner = %v, want it to name %s", got, addr)
	}

	// Each conduit registers one service.
	if _, err := host.Add(ctx, "demo-web", nil, "web"); err != nil {
		t.Fatalf("host Add: %v", err)
	}
	if _, err := joined.Add(ctx, "shop-api", nil, "api"); err != nil {
		t.Fatalf("joiner Add: %v", err)
	}

	// The host's own name resolves, through the host's tunnel.
	body, err := socksGet(t, addr, "web.cornus.internal", 8080)
	if err != nil {
		t.Fatalf("CONNECT to web: %v", err)
	}
	if body != "hostside/demo-web" {
		t.Errorf("web answered %q, want hostside/demo-web", body)
	}

	// And the JOINER's name resolves too — through the one proxy, which is the
	// entire point.
	body, err = socksGet(t, addr, "api.cornus.internal", 9090)
	if err != nil {
		t.Fatalf("CONNECT to the joiner's name: %v", err)
	}
	if !strings.HasSuffix(body, "/shop-api") {
		t.Errorf("api answered %q, want it served as shop-api", body)
	}
	if len(hostDialer.calls()) == 0 {
		t.Error("the host's tunnel was never used")
	}
}

// A joiner leaving must withdraw only its own names, and the host must keep
// serving. This is the everyday case: one compose project ends while another
// carries on.
func TestAJoinerLeavingWithdrawsOnlyItsOwnNames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	cfg := Config{Mode: ModeSocks5, Socks5Listen: addr}

	host, err := Start(ctx, &tunnelDialer{name: "hostside"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	joined, err := Start(ctx, &tunnelDialer{name: "joinside"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Add(ctx, "demo-web", nil, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := joined.Add(ctx, "shop-api", nil, "api"); err != nil {
		t.Fatal(err)
	}

	joined.Close()

	// The joiner's name is gone. Detected by what it RESOLVES TO, not by an error:
	// once the alias is withdrawn the label passes through unmapped, so the CONNECT
	// still succeeds and simply asks for a deployment literally called "api". A test
	// waiting for a failure here would wait forever and prove nothing.
	waitFor(t, "the joiner's alias to be withdrawn", func() bool {
		body, err := socksGet(t, addr, "api.cornus.internal", 9090)
		return err == nil && strings.HasSuffix(body, "/api")
	})
	// ...and the host's is untouched.
	body, err := socksGet(t, addr, "web.cornus.internal", 8080)
	if err != nil {
		t.Fatalf("the host stopped serving when a joiner left: %v", err)
	}
	if body != "hostside/demo-web" {
		t.Errorf("web answered %q, want hostside/demo-web", body)
	}
}

// An ephemeral address must stay private: there is no agreed port for anyone to
// rendezvous on, so it is neither advertised nor joinable even with a registry.
func TestEphemeralConduitStaysPrivateEvenWithARegistry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	cfg := Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"}

	c, err := Start(ctx, &tunnelDialer{name: "a"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := c.(*rendezvousConduit); ok {
		t.Error("an ephemeral conduit went through the rendezvous; it has no address to be joined at")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var _ = api.PortMapping{}

// The original report's last mile: a name published by a JOINER is reachable
// through the HOST's proxy.
//
// A published name is served by a listener with no address at all, so the host
// cannot reach it by any ordinary means; the publisher lends it a unix socket and
// the host dials that. Until this existed, `cornus web --publish-in-conduit` could
// only publish into a conduit it hosted itself — which is why it forked a second
// proxy rather than joining one.
func TestAJoinerPublishesANameTheHostServes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	cfg := Config{Mode: ModeSocks5, Socks5Listen: addr}

	host, err := Start(ctx, &tunnelDialer{name: "hostside"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	joined, err := Start(ctx, &tunnelDialer{name: "joinside"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer joined.Close()
	if joined.(*rendezvousConduit).Participant().Hosting() {
		t.Fatal("the second conduit hosted rather than joined")
	}

	// The joiner publishes a UI of its own, served by an addressless listener in
	// THIS process.
	pubCtx, withdraw := context.WithCancel(ctx)
	defer withdraw()
	published, err := joined.AddLocal(pubCtx, "cornus.internal", 80, greeter{body: "from-the-joiner"})
	if err != nil || !published {
		t.Fatalf("joiner AddLocal = %v, %v; want it published", published, err)
	}

	// Reached through the HOST's proxy, which is the whole point: one browser proxy
	// setting reaches the workloads and the UI alike.
	body, err := socksGet(t, addr, "cornus.internal", 80)
	if err != nil {
		t.Fatalf("CONNECT to the joiner's published name: %v", err)
	}
	if body != "from-the-joiner" {
		t.Errorf("published name answered %q, want from-the-joiner", body)
	}

	// Withdrawing it takes the name out of the ROUTER, asserted directly rather than
	// through a failing CONNECT. Teardown also closes the lent socket, so a CONNECT
	// fails either way — a test watching for that would pass whether or not the claim
	// was ever withdrawn, leaving a name registered in the host for a publisher that
	// has gone. Confirmed by neutralization: skipping the withdrawal leaves the
	// CONNECT-based check green and fails this one.
	withdraw()
	router := host.(*rendezvousConduit).router
	waitFor(t, "the published name to leave the host's router", func() bool {
		got, err := router.Resolve("cornus.internal", 80)
		return err == nil && got.Kind != socks5.KindLocal
	})
}

// A host publishes through the same path, so a published name is the same kind of
// thing wherever it came from — which is what lets it replay after a takeover with
// no case of its own.
func TestAHostPublishesThroughTheSamePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))

	host, err := Start(ctx, &tunnelDialer{name: "hostside"}, Config{Mode: ModeSocks5, Socks5Listen: addr}, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	published, err := host.AddLocal(ctx, "cornus.internal", 80, greeter{body: "from-the-host"})
	if err != nil || !published {
		t.Fatalf("host AddLocal = %v, %v", published, err)
	}
	body, err := socksGet(t, addr, "cornus.internal", 80)
	if err != nil {
		t.Fatal(err)
	}
	if body != "from-the-host" {
		t.Errorf("published name answered %q, want from-the-host", body)
	}
}

// greeter stands in for a BFF on an addressless listener: every connection gets one
// line and is closed.
type greeter struct{ body string }

func (g greeter) DialLocal(context.Context) (net.Conn, error) {
	here, there := net.Pipe()
	go func() {
		_, _ = there.Write([]byte(g.body))
		_ = there.Close()
	}()
	return here, nil
}

// The banner must name the address actually BOUND, not the one requested. Go binds
// a wildcard request as a dual-stack "[::]", so a conduit asked for "0.0.0.0:PORT"
// serves "[::]:PORT" — and the banner is the line telling a user where to point a
// browser, stored once and handed to every joiner, so naming an address the
// listener does not have propagates the error to everyone.
//
// Observed live: a compose session on 0.0.0.0:10080 advertised bind "[::]:10080"
// while its banner said "0.0.0.0:10080".
func TestBannerNamesTheBoundAddress(t *testing.T) {
	if !conduithost.DualStackWildcard() {
		t.Skip("no dual-stack IPv6 wildcard on this host")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	port := freePort(t)
	cfg := Config{
		Mode:                   ModeSocks5,
		Socks5Listen:           "0.0.0.0:" + strconv.Itoa(port),
		Socks5AllowNonLoopback: true,
	}

	host, err := Start(ctx, &tunnelDialer{name: "hostside"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	bound := host.(*rendezvousConduit).Participant().Addr().String()
	banner := host.Banner()
	if len(banner) == 0 {
		t.Fatal("no banner")
	}
	if !strings.Contains(banner[0], bound) {
		t.Errorf("banner %q does not name the bound address %s", banner[0], bound)
	}

	// And a joiner is handed the same corrected line, since that is the one it prints.
	joined, err := Start(ctx, &tunnelDialer{name: "joinside"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatal(err)
	}
	defer joined.Close()
	if got := joined.Banner(); len(got) == 0 || !strings.Contains(got[0], bound) {
		t.Errorf("joiner banner = %v, want it to name the bound address %s", got, bound)
	}
}

// A publisher that dies without running its teardown leaves its lent socket behind.
// Nothing else reaps them, so the directory would grow by one entry per bad exit,
// forever, with nothing to prompt anyone to look.
func TestStaleUpstreamSocketsAreReaped(t *testing.T) {
	dir := t.TempDir()

	// A corpse: a real socket inode with nothing listening, which is exactly what a
	// SIGKILLed publisher leaves.
	corpse := filepath.Join(dir, "corpse-1-1.sock")
	ln, err := net.Listen("unix", corpse)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(corpse); err != nil {
		t.Fatalf("the corpse should still exist: %v", err)
	}

	// A live one must survive: reaping by pid or by age would take this too.
	livePath, closeLive, err := publishOverSocket(context.Background(), dir, "live", greeter{body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLive()

	if _, err := os.Stat(corpse); !os.IsNotExist(err) {
		t.Errorf("the corpse survived (stat err = %v)", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("the live publisher's socket was reaped: %v", err)
	}
}

// The reported sequence, which an unwired migration broke in both directions:
// compose hosts, the web UI joins, compose is terminated, compose starts again.
//
// Every joiner holds a replica of the listening socket so ownership can move
// without the address going down — but that only helps if somebody TAKES it. With
// the replica held and nobody accepting, the address stayed bound and unserved
// (clients hang rather than fail over) AND a later session could not rebind it. An
// unwired migration is strictly worse than none: without replication the address
// would simply have been freed.
func TestConduitSurvivesItsHostAndAcceptsANewOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := testRegistry(t)
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	cfg := Config{Mode: ModeSocks5, Socks5Listen: addr}

	// 1. A compose session hosts the conduit and registers a name.
	first, err := Start(ctx, &tunnelDialer{name: "compose1"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if _, err := first.Add(ctx, "demo-web", nil, "web"); err != nil {
		t.Fatal(err)
	}

	// 2. A web UI joins it.
	web, err := Start(ctx, &tunnelDialer{name: "web"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatalf("joining session: %v", err)
	}
	defer web.Close()
	if web.(*rendezvousConduit).Participant().Hosting() {
		t.Fatal("the second session hosted instead of joining")
	}

	// 3. The host is terminated. The survivor must take the socket over.
	first.Close()
	waitFor(t, "the survivor to take over", func() bool {
		return web.(*rendezvousConduit).Participant().Hosting()
	})

	// Served, not merely bound. A test that only checked the address still accepted
	// a connection would pass against exactly the broken state this guards.
	if _, err := web.Add(ctx, "web-own", nil, "ui"); err != nil {
		t.Fatalf("the survivor cannot register after taking over: %v", err)
	}
	body, err := socksGet(t, addr, "ui.cornus.internal", 80)
	if err != nil {
		t.Fatalf("after the host exited the conduit does not answer: %v", err)
	}
	if body != "web/web-own" {
		t.Errorf("answered %q, want it served by the survivor as web-own", body)
	}

	// 4. A new session at the same address JOINS the survivor rather than failing to
	// bind — the half that made the reported sequence unrecoverable.
	second, err := Start(ctx, &tunnelDialer{name: "compose2"}, cfg, WithRendezvous(r))
	if err != nil {
		t.Fatalf("a new session at the same address: %v", err)
	}
	defer second.Close()
	if second.(*rendezvousConduit).Participant().Hosting() {
		t.Error("the new session bound its own proxy instead of joining the survivor")
	}
	if _, err := second.Add(ctx, "demo2-web", nil, "web2"); err != nil {
		t.Fatal(err)
	}
	body, err = socksGet(t, addr, "web2.cornus.internal", 80)
	if err != nil {
		t.Fatalf("the new session's name does not resolve: %v", err)
	}
	if body != "web/demo2-web" {
		t.Errorf("answered %q, want the survivor serving demo2-web", body)
	}
}
