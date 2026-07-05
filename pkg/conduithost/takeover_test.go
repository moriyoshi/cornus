package conduithost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/listenerpass"
)

func requireMigration(t *testing.T) {
	t.Helper()
	if !listenerpass.Supported() {
		t.Skip("listener replication is unsupported on this platform")
	}
}

// A joiner must come away holding a reference to the conduit's listening socket.
// Taken at JOIN time and not at the host's exit, because a SIGKILLed host hands
// nothing to anyone.
func TestJoinerTakesAListenerReplica(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	mustOpenAt(t, r, "0.0.0.0:"+strconv.Itoa(port), &fakeRegistrar{})

	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	j, ok := joiner.(*Joiner)
	if !ok {
		t.Fatalf("second Open returned %T, want a *Joiner", joiner)
	}
	if !j.CanTakeOver() {
		t.Fatal("joiner holds no listener replica, so it could never take over")
	}
}

// THE test this whole design exists for: the address must never refuse a
// connection while ownership moves.
//
// A test that only asserted "a survivor became the host" would pass even if the
// address went unbound for a moment — and that moment is precisely the failure
// being designed against, because a browser dialing during it gets a refusal.
// So the measurement is a connect loop running across the handover, counting
// refusals, and the assertion is that the count is zero.
func TestAddressNeverRefusesDuringTakeover(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	hostReg, joinReg := &fakeRegistrar{}, &fakeRegistrar{}

	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), hostReg)
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), joinReg).(*Joiner)
	if !joiner.CanTakeOver() {
		t.Fatal("joiner holds no replica")
	}
	addr := host.Addr().String()

	// Serve accepts on whichever listener currently belongs to us, so a connection
	// made mid-handover is answered rather than merely queued.
	var serving atomic.Pointer[net.Listener]
	hostLn := host.(*Host).Listener()
	serving.Store(&hostLn)
	acceptLoop := func(stop <-chan struct{}) {
		for {
			select {
			case <-stop:
				return
			default:
			}
			lp := serving.Load()
			if lp == nil {
				time.Sleep(time.Millisecond)
				continue
			}
			ln := *lp
			if tl, ok := ln.(*net.TCPListener); ok {
				_ = tl.SetDeadline(time.Now().Add(50 * time.Millisecond))
			}
			c, err := ln.Accept()
			if err == nil {
				_, _ = c.Write([]byte("ok"))
				_ = c.Close()
			}
		}
	}
	stop := make(chan struct{})
	go acceptLoop(stop)
	defer close(stop)

	var refused, unanswered, attempts atomic.Int64
	// 0 = before the host died, 1 = nobody owns the socket, 2 = the new host serves.
	var phase atomic.Int64
	var connected [3]atomic.Int64
	dialStop := make(chan struct{})
	var dialers sync.WaitGroup
	// Several dialers, because one is not enough to sample the ownerless interval:
	// a connection made during it is QUEUED and only answered once the new host
	// starts accepting, so a serial dialer blocks there and contributes exactly one
	// observation of the very window under test.
	const dialerCount = 4
	for range dialerCount {
		dialers.Add(1)
		go func() {
			defer dialers.Done()
			for {
				select {
				case <-dialStop:
					return
				default:
				}
				attempts.Add(1)
				// Tag the attempt by the phase it CONNECTS in, not the one it finishes
				// in: a connection made while nobody owns the socket is queued and only
				// answered after the takeover, so crediting it on completion attributes
				// it to the wrong phase and leaves the interval under test looking
				// unvisited.
				at := phase.Load()
				c, err := net.DialTimeout("tcp", addr, 2*time.Second)
				if err == nil {
					connected[at].Add(1)
				}
				if err != nil {
					// A refusal is the failure. A timeout is not: it means the socket is
					// bound and the backlog is holding us, which is exactly the behaviour
					// that makes the handover invisible.
					if isRefused(err) {
						refused.Add(1)
					}
					time.Sleep(time.Millisecond)
					continue
				}
				// A successful connect proves only that the socket is BOUND. TCP completes
				// the handshake in the kernel, before anything calls Accept, so a socket
				// nobody serves accepts connections into the backlog and leaves them there
				// — no refusal, no answer, client hangs. Requiring a reply is what tells
				// "bound" apart from "served", and it is the difference between a conduit
				// that works and one that is merely advertised.
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 2)
				if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ok" {
					unanswered.Add(1)
				}
				_ = c.Close()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Let the loop establish a baseline against the original host.
	waitFor(t, "the dial loop to get going", func() bool { return attempts.Load() > 20 })

	// The host goes away. Its listener closes; the joiner's replica keeps the socket
	// alive.
	if err := host.Close(); err != nil {
		t.Fatalf("host Close: %v", err)
	}
	phase.Store(1)
	select {
	case <-joiner.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("joiner never noticed the host's death")
	}

	// Hand the accept loop over the moment we own the socket.
	serving.Store(nil)

	// Sit in the gap deliberately. Without this the handover is over in
	// microseconds, and the test would pass for a future version that only stayed
	// bound because nothing had time to notice. The claim is that the socket is held
	// throughout the INTERVAL between one host dying and the next taking over, so
	// the interval has to be long enough to observe.
	time.Sleep(250 * time.Millisecond)

	p, err := joiner.Takeover(context.Background())
	if err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if !p.Hosting() {
		t.Fatal("the only survivor did not become the host")
	}
	newLn := p.(*Host).Listener()
	serving.Store(&newLn)
	phase.Store(2)

	waitFor(t, "connections to be established against the NEW host", func() bool {
		return connected[2].Load() > 5
	})
	close(dialStop)
	dialers.Wait()

	if n := refused.Load(); n != 0 {
		t.Errorf("%d of %d connections were REFUSED across the handover; the address went unbound", n, attempts.Load())
	}
	if n := unanswered.Load(); n != 0 {
		t.Errorf("%d of %d connections were accepted but never ANSWERED; the address stayed bound while serving nothing", n, attempts.Load())
	}
	// Each phase has to have been exercised, or the zero above is a zero over
	// nothing. Counted by WHEN THE DIAL CONNECTED rather than when its round trip
	// finished: a connection made during the ownerless interval is queued and only
	// answered once the new host accepts, so counting completions credits it to the
	// wrong phase and can leave the interval under test looking unvisited.
	if connected[1].Load() <= 0 {
		t.Errorf("no connections were established during the ownerless interval, so it went unmeasured")
	}
	if connected[2].Load() <= 0 {
		t.Errorf("no connections were established after the takeover, so the new host was never exercised")
	}
	t.Logf("connected: %d before the host died, %d while nobody owned the socket, %d against the new host; %d refused, %d unanswered (of %d dials)",
		connected[0].Load(), connected[1].Load(), connected[2].Load(), refused.Load(), unanswered.Load(), attempts.Load())
}

// Without a replica the address genuinely does go down when its host closes.
// This gives the test above its teeth: if replication silently did nothing, the
// zero-refusal assertion would still have to fail somewhere, and here is where.
func TestWithoutAReplicaTheAddressGoesDownWithTheHost(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	addr := host.Addr().String()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		_ = c.Close()
		t.Fatalf("%s still accepted a connection after its only host closed", addr)
	}
	if !isRefused(err) {
		t.Errorf("dial error = %v, want a refusal", err)
	}
}

// Registrations are scoped to a control connection, so they all die with the host.
// Only the survivor still knows what it had registered, so the survivor must
// restore them — otherwise a takeover silently loses every name the conduit was
// resolving.
func TestTakeoverReplaysTheSurvivorsRegistrations(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	survivorReg := &fakeRegistrar{}
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), survivorReg).(*Joiner)

	ctx := context.Background()
	if _, err := joiner.Register(ctx, "a", "service", json.RawMessage(`{"name":"web"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.Register(ctx, "b", "ingress", json.RawMessage(`{"host":"app.test"}`)); err != nil {
		t.Fatal(err)
	}
	// They went to the HOST's registrar, not the survivor's own.
	if added, _ := survivorReg.snapshot(); len(added) != 0 {
		t.Fatalf("survivor's registrar was used while joining: %+v", added)
	}

	_ = host.Close()
	<-joiner.Done()
	p, err := joiner.Takeover(ctx)
	if err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	added, _ := survivorReg.snapshot()
	if len(added) != 2 {
		t.Fatalf("after takeover the survivor's registrar holds %+v, want both registrations restored", added)
	}
	// Order is preserved: replaying out of order would resolve a name to whichever
	// registration happened to land last.
	if added[0].Kind != "service" || added[1].Kind != "ingress" {
		t.Errorf("replayed %q then %q, want service then ingress", added[0].Kind, added[1].Kind)
	}
	if added[0].Payload != `{"name":"web"}` || added[1].Payload != `{"host":"app.test"}` {
		t.Errorf("replayed payloads = %q, %q", added[0].Payload, added[1].Payload)
	}
	if !added[0].Peer.Local {
		t.Error("a replayed registration is not marked Local although this process now hosts")
	}
}

// A registration the survivor had already withdrawn must NOT come back. Replaying
// it would resurrect a name the caller deliberately dropped.
func TestTakeoverDoesNotReplayWithdrawnRegistrations(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	survivorReg := &fakeRegistrar{}
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), survivorReg).(*Joiner)

	ctx := context.Background()
	withdraw, err := joiner.Register(ctx, "gone", "service", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.Register(ctx, "kept", "service", json.RawMessage(`{"keep":true}`)); err != nil {
		t.Fatal(err)
	}
	withdraw()

	_ = host.Close()
	<-joiner.Done()
	p, err := joiner.Takeover(ctx)
	if err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	added, _ := survivorReg.snapshot()
	if len(added) != 1 {
		t.Fatalf("replayed %+v, want only the registration that was still live", added)
	}
	if added[0].Payload != `{"keep":true}` {
		t.Errorf("replayed %q, want the kept registration", added[0].Payload)
	}
}

// Several survivors racing must produce exactly one host, and the losers must end
// up attached to the winner rather than erroring out. The port lock is what makes
// that true, and only a concurrent test can show it.
func TestConcurrentTakeoverProducesExactlyOneHost(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})

	const survivors = 5
	joiners := make([]*Joiner, survivors)
	for i := range joiners {
		joiners[i] = mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)
	}
	_ = host.Close()
	for _, j := range joiners {
		select {
		case <-j.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("a joiner never noticed the host's death")
		}
	}

	var wg sync.WaitGroup
	results := make([]Participant, survivors)
	errs := make([]error, survivors)
	start := make(chan struct{})
	for i, j := range joiners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = j.Takeover(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	hosts, joined := 0, 0
	for i, p := range results {
		if errs[i] != nil {
			t.Errorf("survivor %d: %v", i, errs[i])
			continue
		}
		if p.Hosting() {
			hosts++
		} else {
			joined++
		}
		t.Cleanup(func() { _ = p.Close() })
	}
	if hosts != 1 {
		t.Errorf("hosts after the election = %d, want exactly 1 (joined = %d)", hosts, joined)
	}
	if joined != survivors-1 {
		t.Errorf("survivors that re-joined = %d, want %d", joined, survivors-1)
	}
}

// A survivor that lost the election must still be a working participant — with a
// replica of its own, so a second death migrates again rather than ending the
// chain.
func TestALoserOfTheElectionCanStillTakeOverLater(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	first := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)
	second := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)

	_ = host.Close()
	<-first.Done()
	<-second.Done()

	winner, err := first.Takeover(context.Background())
	if err != nil {
		t.Fatalf("first Takeover: %v", err)
	}
	if !winner.Hosting() {
		t.Fatal("the first survivor did not become host")
	}
	loser, err := second.Takeover(context.Background())
	if err != nil {
		t.Fatalf("second Takeover: %v", err)
	}
	if loser.Hosting() {
		t.Fatal("both survivors became hosts")
	}
	lj := loser.(*Joiner)
	if !lj.CanTakeOver() {
		t.Error("the re-joined survivor holds no replica, so the migration chain ends here")
	}

	// And the chain really does continue: kill the new host, and the loser takes over.
	_ = winner.Close()
	select {
	case <-lj.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the re-joined survivor never noticed the second host's death")
	}
	third, err := lj.Takeover(context.Background())
	if err != nil {
		t.Fatalf("second-generation Takeover: %v", err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if !third.Hosting() {
		t.Error("the second-generation takeover produced no host")
	}
}

func TestTakeoverIsRefusedWhileTheHostLives(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)

	if _, err := joiner.Takeover(context.Background()); err == nil {
		t.Fatal("Takeover succeeded while the host was still live; it would have unlinked a live control socket")
	}
}

func TestTakeoverRunsOnlyOnce(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	joiner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)
	_ = host.Close()
	<-joiner.Done()

	p, err := joiner.Takeover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if _, err := joiner.Takeover(context.Background()); err == nil {
		t.Error("a second Takeover on the same participant succeeded")
	}
}

// isRefused distinguishes the one failure that matters — nothing is listening —
// from a timeout, which means the socket IS bound and the backlog is holding the
// connection. Conflating them would turn the handover's intended behaviour into a
// test failure.
func isRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscallConnRefused) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	return contains(err.Error(), "refused")
}

// Precedence must survive the host that assigned it.
//
// This is the end-to-end form of the defect measured before sequences existed: two
// projects claim one short name, the host dies, and the survivors replay in
// whatever order the election and the reconnect race produced — commonly the
// REVERSE of the original, because whoever wins the election replays first. Without
// a carried sequence the claims are renumbered by arrival, so the same crash routes
// differently every time and no participant can detect it, since each replays its
// own claims in its own original order.
func TestTakeoverPreservesPrecedenceAcrossParticipants(t *testing.T) {
	requireMigration(t)
	ctx := context.Background()
	r := testRegistry(t)
	port := freePort(t)

	hostReg := &fakeRegistrar{}
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), hostReg)
	aReg, bReg := &fakeRegistrar{}, &fakeRegistrar{}
	a := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), aReg).(*Joiner)
	b := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), bReg).(*Joiner)

	// A claims first, B claims second and therefore outranks it.
	if _, err := a.Register(ctx, "a1", "alias", json.RawMessage(`{"dep":"demo-web"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register(ctx, "b1", "alias", json.RawMessage(`{"dep":"shop-web"}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := hostReg.snapshot()
	if len(before) != 2 {
		t.Fatalf("host registrar holds %+v, want both claims", before)
	}
	aSeq, bSeq := before[0].Seq, before[1].Seq
	if aSeq == 0 || bSeq == 0 || !(aSeq < bSeq) {
		t.Fatalf("sequences = %d then %d, want two non-zero and ascending", aSeq, bSeq)
	}

	// The host dies; B wins the election and replays first, A rejoins after.
	_ = host.Close()
	<-a.Done()
	<-b.Done()
	pb, err := b.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pb.Close() })
	pa, err := a.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pa.Close() })

	// The new host saw them in the REVERSE order — that part is unavoidable — but
	// each carries the sequence it was originally given, so a registrar that orders
	// by sequence reconstructs the original precedence exactly.
	after, _ := bReg.snapshot()
	if len(after) != 2 {
		t.Fatalf("new host registrar holds %+v, want both claims restored", after)
	}
	got := map[string]uint64{}
	for _, rec := range after {
		got[rec.Payload] = rec.Seq
	}
	if got[`{"dep":"demo-web"}`] != aSeq {
		t.Errorf("A's claim replayed at seq %d, want its original %d", got[`{"dep":"demo-web"}`], aSeq)
	}
	if got[`{"dep":"shop-web"}`] != bSeq {
		t.Errorf("B's claim replayed at seq %d, want its original %d", got[`{"dep":"shop-web"}`], bSeq)
	}
	if !(got[`{"dep":"demo-web"}`] < got[`{"dep":"shop-web"}`]) {
		t.Errorf("after the takeover A(%d) no longer precedes B(%d); the short name has silently moved",
			got[`{"dep":"demo-web"}`], got[`{"dep":"shop-web"}`])
	}
	t.Logf("arrival order after takeover: %s then %s; sequences preserved as %d and %d",
		after[0].Payload, after[1].Payload, after[0].Seq, after[1].Seq)
}

// A claim made after a takeover must outrank everything inherited, or a project
// joining a recovered conduit could never win a contested name.
func TestClaimAfterTakeoverOutranksRestoredOnes(t *testing.T) {
	requireMigration(t)
	ctx := context.Background()
	r := testRegistry(t)
	port := freePort(t)

	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	survivorReg := &fakeRegistrar{}
	s := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), survivorReg).(*Joiner)
	if _, err := s.Register(ctx, "old", "alias", json.RawMessage(`{"dep":"old-web"}`)); err != nil {
		t.Fatal(err)
	}

	_ = host.Close()
	<-s.Done()
	p, err := s.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if _, err := p.Register(ctx, "new", "alias", json.RawMessage(`{"dep":"new-web"}`)); err != nil {
		t.Fatal(err)
	}
	added, _ := survivorReg.snapshot()
	var restored, fresh uint64
	for _, rec := range added {
		switch rec.Payload {
		case `{"dep":"old-web"}`:
			restored = rec.Seq
		case `{"dep":"new-web"}`:
			fresh = rec.Seq
		}
	}
	if restored == 0 || fresh == 0 {
		t.Fatalf("registrar holds %+v, want both claims", added)
	}
	if fresh <= restored {
		t.Errorf("a claim made after the takeover got seq %d, not above the restored %d — it would land underneath everything inherited", fresh, restored)
	}
}

// recoveringRegistrar is a Registrar that also implements Recoverer, so a test can
// see whether a takeover opened a recovery window and for how long.
type recoveringRegistrar struct {
	fakeRegistrar
	mu     sync.Mutex
	begun  int
	until  time.Time
	atCall int // how many registrations had been applied when the window opened
}

func (r *recoveringRegistrar) BeginRecovery(until time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begun++
	r.until = until
	added, _ := r.fakeRegistrar.snapshot()
	r.atCall = len(added)
}

func (r *recoveringRegistrar) recovery() (int, time.Time, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.begun, r.until, r.atCall
}

// A takeover must open the recovery window, and open it BEFORE replaying — a
// request arriving mid-replay is exactly the case the window exists for.
func TestTakeoverOpensTheRecoveryWindowBeforeReplaying(t *testing.T) {
	requireMigration(t)
	ctx := context.Background()
	r := testRegistry(t)
	port := freePort(t)

	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	survivorReg := &recoveringRegistrar{}
	a, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(ctx, Config{
		Registry: r, Addr: a, Registrar: survivorReg, RecoveryWindow: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	survivor := p.(*Joiner)
	if _, err := survivor.Register(ctx, "one", "alias", json.RawMessage(`{"dep":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := survivor.Register(ctx, "two", "alias", json.RawMessage(`{"dep":"b"}`)); err != nil {
		t.Fatal(err)
	}

	_ = host.Close()
	<-survivor.Done()
	before := time.Now()
	out, err := survivor.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })

	begun, until, atCall := survivorReg.recovery()
	if begun != 1 {
		t.Fatalf("BeginRecovery called %d times, want exactly 1", begun)
	}
	if atCall != 0 {
		t.Errorf("the window opened after %d registrations had been replayed, want before any of them", atCall)
	}
	if window := until.Sub(before); window < time.Second || window > 4*time.Second {
		t.Errorf("recovery window is %s, want about the configured 2s", window)
	}
	// The replay still happened.
	added, _ := survivorReg.fakeRegistrar.snapshot()
	if len(added) != 2 {
		t.Errorf("replayed %d registrations, want 2", len(added))
	}
}

// A survivor that LOST the election registers into somebody else's router, and that
// host has opened its own window. Opening one locally would put this process's
// router into recovery when it is not serving anything.
func TestTakeoverDoesNotOpenARecoveryWindowWhenItOnlyJoins(t *testing.T) {
	requireMigration(t)
	ctx := context.Background()
	r := testRegistry(t)
	port := freePort(t)
	a, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}

	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	winner := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)
	loserReg := &recoveringRegistrar{}
	p, err := Open(ctx, Config{Registry: r, Addr: a, Registrar: loserReg})
	if err != nil {
		t.Fatal(err)
	}
	loser := p.(*Joiner)

	_ = host.Close()
	<-winner.Done()
	<-loser.Done()

	w, err := winner.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	l, err := loser.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if l.Hosting() {
		t.Fatal("both survivors became hosts")
	}
	if begun, _, _ := loserReg.recovery(); begun != 0 {
		t.Errorf("the survivor that only joined opened %d recovery windows, want 0", begun)
	}
}

// A registrar without the capability must be told about, not silently degraded:
// the takeover still works, but for a moment it answers unrestored names as
// unknown, and nothing else would reveal that.
func TestTakeoverReportsARegistrarThatCannotRecover(t *testing.T) {
	requireMigration(t)
	ctx := context.Background()
	r := testRegistry(t)
	port := freePort(t)
	a, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}

	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	var mu sync.Mutex
	var logged []string
	p, err := Open(ctx, Config{
		Registry: r, Addr: a, Registrar: &fakeRegistrar{}, // no Recoverer
		Logf: func(format string, args ...any) {
			mu.Lock()
			logged = append(logged, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	survivor := p.(*Joiner)
	_ = host.Close()
	<-survivor.Done()
	out, err := survivor.Takeover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })

	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, l := range logged {
		if contains(l, "recovery window") {
			found = true
		}
	}
	if !found {
		t.Errorf("a takeover with a registrar that cannot recover logged %v, want it to say so", logged)
	}
}
