package socks5

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// During recovery an unclaimed conduit name must WAIT rather than answer, because
// both honest answers are wrong: the bare form would egress to public DNS and the
// suffixed form would tunnel to a service that does not exist. The information
// needed to answer correctly is arriving imminently, so the caller waits.
func TestRecoveringDefersUnclaimedConduitNames(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.SetRecoveryUntil(time.Now().Add(10 * time.Second))

	// The bare form: without recovery this egresses, which is the dangerous answer.
	got, err := r.Resolve("web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindPending || got.Label != "web" {
		t.Errorf("bare unclaimed name while recovering = %+v, want KindPending for %q", got, "web")
	}
	// The suffixed form: without recovery this resolves to a nonexistent service.
	got, err = r.Resolve("web.cornus.internal", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindPending {
		t.Errorf("suffixed unclaimed name while recovering = %+v, want KindPending", got)
	}
}

// Ordinary internet traffic must NOT be deferred. A takeover that stalled every
// unrelated request the browser makes would be a worse outage than the one it is
// recovering from.
func TestRecoveringDoesNotDeferInternetTraffic(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.SetRecoveryUntil(time.Now().Add(10 * time.Second))

	for _, host := range []string{"github.com", "example.co.uk", "10.0.0.5"} {
		got, err := r.Resolve(host, 443)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != KindDirect {
			t.Errorf("Resolve(%q) while recovering = %+v, want KindDirect", host, got)
		}
	}
}

// Outside a recovery window nothing changes: an unclaimed bare name egresses as it
// always did. The deferral is scoped to the interval where the router genuinely
// cannot tell an unknown name from one about to be restored.
func TestNotRecoveringAnswersImmediately(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve("web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindDirect {
		t.Errorf("bare unclaimed name outside recovery = %+v, want KindDirect", got)
	}
}

// The wait ends the moment the claim arrives — that is the whole point, and it is
// what turns an error into latency.
func TestAwaitClaimWakesOnRegistration(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.SetRecoveryUntil(time.Now().Add(10 * time.Second))

	done := make(chan bool, 1)
	go func() { done <- r.AwaitClaim(context.Background(), "web") }()

	// Give the waiter time to park, then restore the claim as a re-registering
	// participant would.
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	r.RegisterAlias("web", "demo-web")

	select {
	case claimed := <-done:
		if !claimed {
			t.Fatal("AwaitClaim reported no claim although one was registered")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("waiter took %s to wake after the claim landed; it waited out the window instead", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitClaim never woke after the claim was registered")
	}

	// And the name now resolves to it.
	got, err := r.Resolve("web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindService || got.Service != "demo-web" {
		t.Errorf("after the claim landed, bare web = %+v, want demo-web", got)
	}
}

// A name nothing will ever claim must cost one window of latency, not hang. The
// bound is what keeps "wait for it" from becoming "wait forever".
func TestAwaitClaimGivesUpWhenTheWindowCloses(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.SetRecoveryUntil(time.Now().Add(150 * time.Millisecond))

	start := time.Now()
	if r.AwaitClaim(context.Background(), "never") {
		t.Error("AwaitClaim reported a claim that was never registered")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("AwaitClaim took %s to give up, want about the window length", elapsed)
	}
	// And afterwards the router answers as it always would.
	got, err := r.Resolve("never", 80)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindDirect {
		t.Errorf("after the window closed, Resolve = %+v, want KindDirect", got)
	}
}

// Closing the window early must release every waiter, or a cancelled recovery
// leaves callers parked on a deadline that no longer applies.
func TestClosingTheWindowReleasesWaiters(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.SetRecoveryUntil(time.Now().Add(30 * time.Second))
	done := make(chan bool, 1)
	go func() { done <- r.AwaitClaim(context.Background(), "web") }()
	time.Sleep(50 * time.Millisecond)

	r.SetRecoveryUntil(time.Time{})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the recovery window left a waiter parked")
	}
}

// A caller that goes away must not leave a waiter behind.
func TestAwaitClaimHonoursContextCancellation(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r.SetRecoveryUntil(time.Now().Add(30 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- r.AwaitClaim(ctx, "web") }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case claimed := <-done:
		if claimed {
			t.Error("a cancelled wait reported a claim")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the context left a waiter parked")
	}
}

// countingDirect records every direct-egress dial, so a test can prove a request
// meant for a workload did NOT leave the machine.
type countingDirect struct {
	mu    sync.Mutex
	hosts []string
}

func (d *countingDirect) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.mu.Lock()
	d.hosts = append(d.hosts, address)
	d.mu.Unlock()
	return nil, fmt.Errorf("countingDirect: no egress in tests")
}

func (d *countingDirect) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.hosts...)
}

// The end-to-end contract: a CONNECT arriving during recovery is HELD until the
// claim is re-registered, and then served — rather than being answered wrongly.
//
// Both wrong answers are checked for, not just one. Failing the request would be
// visible but survivable; egressing to public DNS sends a request meant for a
// workload out of the machine, and a test that only asserted "it eventually
// worked" would pass while that happened.
func TestProxyHoldsAConnectUntilTheClaimIsRestored(t *testing.T) {
	r, err := NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	claimed := &recordingDialer{name: "claimed"}
	direct := &countingDirect{}
	p, err := Start(context.Background(), &recordingDialer{name: "proxy"}, r, "127.0.0.1:0", WithDirectDialer(direct))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// A host has just taken over; the participant owning "web" has not re-registered.
	r.SetRecoveryUntil(time.Now().Add(10 * time.Second))

	connectDone := make(chan struct{})
	go func() {
		defer close(connectDone)
		connectVia(t, p.Addr(), "web", 8080)
	}()

	// While the claim is missing the request must be neither answered nor egressed.
	select {
	case <-connectDone:
		t.Fatal("the CONNECT completed while the claim was still missing; it was answered wrongly instead of held")
	case <-time.After(300 * time.Millisecond):
	}
	if got := direct.calls(); len(got) != 0 {
		t.Fatalf("a request for a workload egressed to %v while the conduit was recovering", got)
	}

	// The owner reconnects and re-registers, exactly as a survivor does after a
	// takeover.
	r.Claim(AliasSpec{Label: "web", Deployment: "demo-web", Dialer: claimed})

	select {
	case <-connectDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the CONNECT stayed blocked after the claim was restored")
	}
	if got, want := claimed.calls(), "demo-web:8080/tcp"; len(got) != 1 || got[0] != want {
		t.Errorf("the restored claim's dialer saw %v, want [%s]", got, want)
	}
	if got := direct.calls(); len(got) != 0 {
		t.Errorf("the request egressed to %v; it should have been tunneled", got)
	}
}
