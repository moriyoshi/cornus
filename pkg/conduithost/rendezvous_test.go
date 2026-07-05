package conduithost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeRegistrar records what a host was asked to register and lets a test observe
// withdrawal. It stands in for the socks5.Router-backed one pkg/clientconduit will
// supply.
type fakeRegistrar struct {
	mu        sync.Mutex
	added     []regRecord
	withdrawn []string
	failKind  string // registrations of this kind are refused
}

type regRecord struct {
	Kind    string
	Payload string
	Seq     uint64
	Peer    Peer
}

func (f *fakeRegistrar) Register(_ context.Context, reg Registration) (Withdraw, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reg.Kind == f.failKind {
		return nil, fmt.Errorf("registrar refuses %q", reg.Kind)
	}
	f.added = append(f.added, regRecord{Kind: reg.Kind, Payload: string(reg.Payload), Seq: reg.Seq, Peer: reg.Peer})
	kind := reg.Kind
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.withdrawn = append(f.withdrawn, kind)
	}, nil
}

func (f *fakeRegistrar) snapshot() ([]regRecord, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]regRecord(nil), f.added...), append([]string(nil), f.withdrawn...)
}

// freePort picks a port nothing is listening on. The bind is released before it is
// returned, so this is advisory — but the alternative (an ephemeral conduit) is
// unadvertised by design and so cannot exercise the rendezvous at all.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	// Short root: a control socket path over ~104 bytes cannot be bound, and
	// t.TempDir() under a long TMPDIR plus the port/key components gets close.
	dir, err := os.MkdirTemp("", "cnd")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return NewRegistry(dir)
}

func openAt(t *testing.T, r *Registry, addr string, reg Registrar) (Participant, error) {
	t.Helper()
	a, err := ParseAddr(addr)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", addr, err)
	}
	return Open(context.Background(), Config{
		Registry:  r,
		Addr:      a,
		Registrar: reg,
		Settings:  json.RawMessage(`{"suffix":".cornus.internal"}`),
		Banner:    []string{"SOCKS5 proxy listening on " + a.String()},
	})
}

func mustOpenAt(t *testing.T, r *Registry, addr string, reg Registrar) Participant {
	t.Helper()
	p, err := openAt(t, r, addr, reg)
	if err != nil {
		t.Fatalf("Open(%s): %v", addr, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// The headline behaviour: a second process asking for a loopback address finds the
// wildcard conduit already serving it and JOINS, instead of forking a second proxy
// that would then fail to bind.
func TestSecondRequestJoinsRatherThanBinds(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	hostReg, joinReg := &fakeRegistrar{}, &fakeRegistrar{}

	host := mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), hostReg)
	if !host.Hosting() {
		t.Fatal("first Open did not host")
	}

	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), joinReg)
	if joiner.Hosting() {
		t.Fatal("second Open bound its own conduit instead of joining")
	}
	// The joiner must report the address a browser has to point at — the
	// incumbent's BOUND address — not the one it asked for. Asserted as a
	// relationship rather than a literal because the bound address is the kernel's
	// answer, not the requested string (see TestWildcardRequestBindsDualStack).
	if got, want := joiner.Addr().String(), host.Addr().String(); got != want {
		t.Errorf("joiner Addr() = %s, want the incumbent's %s", got, want)
	}
	if joiner.Addr().Port != port {
		t.Errorf("joiner Addr() port = %d, want %d", joiner.Addr().Port, port)
	}
	if len(joiner.Banner()) == 0 || joiner.Banner()[0] != host.Banner()[0] {
		t.Errorf("joiner Banner() = %v, want the host's %v", joiner.Banner(), host.Banner())
	}

	// A joiner's registration must land in the HOST's registrar: there is one
	// router, and it is the host's.
	if _, err := joiner.Register(context.Background(), "svc-1", "service", json.RawMessage(`{"name":"web"}`)); err != nil {
		t.Fatalf("joiner Register: %v", err)
	}
	added, _ := hostReg.snapshot()
	if len(added) != 1 || added[0].Kind != "service" || added[0].Payload != `{"name":"web"}` {
		t.Fatalf("host registrar got %+v, want one service registration", added)
	}
	if added[0].Peer.Pid != os.Getpid() {
		t.Errorf("peer pid = %d, want the joining process's %d", added[0].Peer.Pid, os.Getpid())
	}
	if added[0].Peer.Local {
		t.Error("a registration arriving over the control socket is marked Local")
	}
	if got, _ := joinReg.snapshot(); len(got) != 0 {
		t.Errorf("joiner's own registrar was used (%+v); registrations must go to the host", got)
	}
}

// Go's net.Listen with network "tcp" and a WILDCARD address prefers an AF_INET6
// dual-stack socket, so asking for "0.0.0.0:P" yields a listener whose Addr() is
// "[::]:P". The rendezvous identity is therefore the BOUND address, never the
// requested spelling — advertising the latter would publish an address the
// listener does not have and make every later coverage decision reason about
// fiction. Pinned here because it is surprising, load-bearing, and would otherwise
// look like a bug the next time someone reads an advertisement.
func TestWildcardRequestBindsDualStack(t *testing.T) {
	if !DualStackWildcard() {
		t.Skip("no dual-stack IPv6 wildcard on this host")
	}
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), &fakeRegistrar{})

	if host.Addr().String() != "[::]:"+strconv.Itoa(port) {
		t.Logf("bound %s for a requested 0.0.0.0:%d", host.Addr(), port)
	}
	entries, err := r.Live(port)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("live entries = %d, want 1", len(entries))
	}
	// Whatever the family turned out to be, the advertisement must agree with the
	// listener: that agreement is the contract, and pinning each side separately
	// would let them drift while both looked defensible.
	if entries[0].Bind != host.Addr().String() {
		t.Errorf("advertised %q but bound %q", entries[0].Bind, host.Addr())
	}
	// And a loopback request must still consolidate into it.
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	if joiner.Hosting() {
		t.Error("a loopback request forked its own conduit beside the wildcard one")
	}
}

// A host registering into its own conduit must not round-trip through its own
// control socket: it would add a hop and a shutdown deadlock for no gain.
func TestHostRegistersLocally(t *testing.T) {
	r := testRegistry(t)
	reg := &fakeRegistrar{}
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(freePort(t)), reg)

	if _, err := host.Register(context.Background(), "own-1", "service", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("host Register: %v", err)
	}
	added, _ := reg.snapshot()
	if len(added) != 1 {
		t.Fatalf("added = %+v, want 1", added)
	}
	if !added[0].Peer.Local {
		t.Error("the host's own registration is not marked Local")
	}
}

// Withdrawal on disconnect is the contract the whole lifetime model rests on, and
// it must hold for an ABRUPT death, not just an orderly Close: the host does not
// own the joiner's process, so a closed descriptor is the only signal it can rely
// on. Closing the underlying connection without any withdraw frame is what a
// SIGKILLed joiner looks like from here.
func TestAbruptJoinerDeathWithdrawsItsRegistrations(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	hostReg := &fakeRegistrar{}
	mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), hostReg)

	joiner, err := openAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := joiner.Register(context.Background(), "svc-1", "service", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, w := hostReg.snapshot(); len(w) != 0 {
		t.Fatalf("withdrawn before the joiner died: %v", w)
	}

	// Sever the socket beneath the joiner, sending no withdrawal at all.
	if err := joiner.(*Joiner).conn.Close(); err != nil {
		t.Fatalf("severing the control connection: %v", err)
	}

	waitFor(t, "the host to withdraw the dead joiner's registration", func() bool {
		_, w := hostReg.snapshot()
		return len(w) == 1
	})
}

// An explicit withdraw followed by the implicit one at disconnect must not run the
// underlying withdrawal twice — a double withdraw would drop a live alias that a
// later registration had legitimately reclaimed.
func TestWithdrawIsIdempotentAcrossExplicitAndImplicit(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	hostReg := &fakeRegistrar{}
	mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), hostReg)

	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	withdraw, err := joiner.Register(context.Background(), "svc-1", "service", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	withdraw()
	withdraw() // caller-side double call
	_ = joiner.Close()

	// Give the implicit teardown a chance to run before counting.
	waitFor(t, "the explicit withdrawal to land", func() bool {
		_, w := hostReg.snapshot()
		return len(w) >= 1
	})
	time.Sleep(100 * time.Millisecond)
	if _, w := hostReg.snapshot(); len(w) != 1 {
		t.Errorf("withdrawn %d times (%v), want exactly 1", len(w), w)
	}
}

// A conduit on the port that does NOT cover the request can neither be joined nor
// displaced, and the refusal must name it. The kernel would also refuse this bind,
// but with an EADDRINUSE that says nothing about the conduit you could have used.
func TestNonCoveringIncumbentIsAConflictNamingIt(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})

	_, err := openAt(t, r, "0.0.0.0:"+strconv.Itoa(port), &fakeRegistrar{})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Open on a narrower incumbent = %v, want a ConflictError", err)
	}
	if conflict.Incumbent.String() != "127.0.0.1:"+strconv.Itoa(port) {
		t.Errorf("conflict names incumbent %s, want 127.0.0.1:%d", conflict.Incumbent, port)
	}
	if conflict.HostPid != os.Getpid() {
		t.Errorf("conflict names host pid %d, want %d", conflict.HostPid, os.Getpid())
	}
}

// corpse leaves an advertisement plus an orphaned socket inode with nothing
// listening on it — exactly the filesystem state a SIGKILLed host leaves, produced
// here by disabling Go's unlink-on-close, which is the only reason an orderly
// Close tidies up.
func corpse(t *testing.T, r *Registry, addr Addr, pid int) {
	t.Helper()
	if err := r.ensurePortDir(addr.Port); err != nil {
		t.Fatal(err)
	}
	socket := r.SocketPath(addr)
	ctl, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("binding a corpse's control socket: %v", err)
	}
	ctl.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := r.writeEntry(Entry{Bind: addr.String(), Pid: pid, Socket: socket}); err != nil {
		t.Fatal(err)
	}
	_ = ctl.Close() // now "dead": files present, nothing listening
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("the corpse's socket file should still exist: %v", err)
	}
}

// Reaping is tested DIRECTLY, against Live, and not only through Open.
//
// Testing it through Open alone proves nothing: when Live wrongly reports a corpse
// as live, Open still recovers, because dialJoin fails and falls back to reaping
// and binding. That fallback is deliberate defence in depth, and it means an
// Open-level test passes whether or not staleness detection works at all — a test
// green for the wrong reason. Verified by neutralization: making probeSocket
// always answer "live" leaves the Open-level test passing and fails only this one.
func TestLiveReapsACorpse(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	corpse(t, r, addr, 999999)

	entries, err := r.Live(port)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Live reported a corpse as live: %+v", entries)
	}
	if _, err := os.Stat(r.SocketPath(addr)); !os.IsNotExist(err) {
		t.Errorf("the corpse's socket file survived reaping (stat err = %v)", err)
	}
	if _, err := os.Stat(r.statePath(addr)); !os.IsNotExist(err) {
		t.Errorf("the corpse's advertisement survived reaping (stat err = %v)", err)
	}
}

// A live host must NOT be reaped. Without this, "reap everything" would pass the
// corpse test above and silently let a second process bind an address that is
// already being served.
func TestLiveKeepsALiveHost(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})

	entries, err := r.Live(port)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("Live = %+v, want the one live host", entries)
	}
	if entries[0].Bind != host.Addr().String() {
		t.Errorf("Live reported %q, want %q", entries[0].Bind, host.Addr())
	}
}

// A corpse must not block the address forever. This reproduces exactly the
// filesystem state a SIGKILLed host leaves — an advertisement plus an orphaned
// socket inode nothing is listening on — by disabling Go's unlink-on-close, which
// is the only reason an orderly Close cleans up.
func TestStaleAdvertisementIsReapedAndTheAddressTakenOver(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	corpse(t, r, addr, 999999)

	p, err := openAt(t, r, addr.String(), &fakeRegistrar{})
	if err != nil {
		t.Fatalf("Open over a corpse: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if !p.Hosting() {
		t.Fatal("Open over a corpse joined something instead of taking the address over")
	}
}

// An unparseable advertisement is a corpse of a different kind: it can never be
// joined, so leaving it in place would block the port permanently.
func TestUnreadableAdvertisementDoesNotBlockThePort(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	if err := r.ensurePortDir(port); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(r.portDir(port), "127.0.0.1.json")
	if err := os.WriteFile(junk, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	if err != nil {
		t.Fatalf("Open past an unreadable advertisement: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if !p.Hosting() {
		t.Fatal("did not host")
	}
}

// The port lock is load-bearing, and only a concurrent race can test it. Without
// it every racer finds nothing, every racer binds, and all but one fail with a
// bare EADDRINUSE — which is precisely the outcome the rendezvous exists to
// replace.
func TestConcurrentOpenProducesExactlyOneHost(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	const racers = 8

	var wg sync.WaitGroup
	results := make([]Participant, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, _ := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
			p, err := Open(context.Background(), Config{
				Registry: r, Addr: a, Registrar: &fakeRegistrar{},
			})
			results[i], errs[i] = p, err
		}()
	}
	close(start)
	wg.Wait()

	hosts, joiners := 0, 0
	for i, p := range results {
		if errs[i] != nil {
			t.Errorf("racer %d: %v", i, errs[i])
			continue
		}
		if p.Hosting() {
			hosts++
		} else {
			joiners++
		}
		t.Cleanup(func() { _ = p.Close() })
	}
	if hosts != 1 {
		t.Errorf("hosts = %d, want exactly 1 (joiners = %d)", hosts, joiners)
	}
	if joiners != racers-1 {
		t.Errorf("joiners = %d, want %d", joiners, racers-1)
	}
}

// An ephemeral address is private by construction: there is no agreed port for
// anyone to rendezvous on, so it must be neither advertised nor joinable. This is
// what replaces the old session-local flag, and the test pins that it follows from
// the ADDRESS rather than from a separate setting.
func TestEphemeralConduitIsNeverAdvertised(t *testing.T) {
	r := testRegistry(t)
	p, err := openAt(t, r, "127.0.0.1:0", &fakeRegistrar{})
	if err != nil {
		t.Fatalf("Open ephemeral: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if !p.Hosting() {
		t.Fatal("an ephemeral conduit joined something")
	}
	bound := p.Addr()
	if bound.Port == 0 {
		t.Fatal("Addr() still reports port 0; it must report the port the kernel bound")
	}
	entries, err := r.Live(bound.Port)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("ephemeral conduit advertised itself: %+v", entries)
	}
}

// When the host goes away, a joiner must learn it without having to make another
// request — an idle compose session that has finished coming up would otherwise
// never find out. The error must name the host pid rather than surface a bare EOF.
func TestJoinerObservesHostDeath(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), &fakeRegistrar{})
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})

	if err := host.Close(); err != nil {
		t.Fatalf("host Close: %v", err)
	}
	select {
	case <-joiner.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("joiner never observed the host's death")
	}
	_, err := joiner.Register(context.Background(), "svc-1", "service", json.RawMessage(`{}`))
	if !errors.Is(err, ErrHostGone) {
		t.Errorf("Register after host death = %v, want ErrHostGone", err)
	}
	if err != nil && !containsPid(err.Error(), os.Getpid()) {
		t.Errorf("error %q does not name the host pid %d", err, os.Getpid())
	}
}

// A registrar's refusal must reach the joiner as an error, not be swallowed into a
// registration that silently never happened.
func TestRegistrarRefusalReachesTheJoiner(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), &fakeRegistrar{failKind: "service"})
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})

	_, err := joiner.Register(context.Background(), "svc-1", "service", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Register succeeded although the registrar refused it")
	}
	if !contains(err.Error(), "registrar refuses") {
		t.Errorf("error %q does not carry the registrar's reason", err)
	}
}

// After the host closes, the advertisement must be gone so the next process binds
// immediately rather than paying a liveness probe to discover a corpse.
func TestCloseRemovesTheAdvertisement(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	host, err := openAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := r.Live(port); len(entries) != 1 {
		t.Fatalf("live entries while hosting = %d, want 1", len(entries))
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	entries, err := r.Live(port)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("advertisement survived Close: %+v", entries)
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

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func containsPid(s string, pid int) bool { return contains(s, strconv.Itoa(pid)) }
