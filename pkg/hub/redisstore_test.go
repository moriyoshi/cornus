package hub

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hashicorp/yamux"
)

// newTestSession returns a live, non-nil *yamux.Session over an in-memory pipe. Only
// its identity/non-nilness matters to these tests; nothing is written over it.
func newTestSession(t *testing.T) *yamux.Session {
	t.Helper()
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })
	sess, err := yamux.Client(c1, nil)
	if err != nil {
		t.Fatalf("yamux client: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func newTestStore(t *testing.T, replicaID string) *RedisStore {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis run: %v", err)
	}
	t.Cleanup(mr.Close)
	s, err := NewRedisStore(context.Background(), "redis://"+mr.Addr(), replicaID, "ws://"+replicaID)
	if err != nil {
		t.Fatalf("new redis store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRedisStoreLookupSkipsRemovedLocalMux reproduces the RemoveConn window: a local
// delivery provider is still present in the merged Redis view but its in-memory mux
// has already been deleted (RemoveConn deletes the mux under the lock before the
// Redis HDEL lands). Lookup must skip that dead provider and fall through to a sibling
// live provider for the same name rather than reporting the service unreachable.
func TestRedisStoreLookupSkipsRemovedLocalMux(t *testing.T) {
	s := newTestStore(t, "r1")

	muxA := newTestSession(t)
	muxB := newTestSession(t)
	s.RegisterDeliver("A", "svc", muxA)
	s.RegisterDeliver("B", "svc", muxB)

	// Simulate the window: provider A's local mux is gone, but its Redis record has
	// not been HDEL'd yet, so liveProviders still returns it.
	pidA := s.providerID("A", "svc")
	s.mu.Lock()
	delete(s.muxes, pidA)
	s.mu.Unlock()

	// Providers sort by providerID, so "r1:A:svc" precedes "r1:B:svc" and the cursor
	// (starting at 0) selects the dead A first. Lookup must skip it and return B.
	tgt, ok := s.Lookup("svc")
	if !ok {
		t.Fatal("Lookup should fall through to live provider B, got not-found")
	}
	if tgt.Mux != muxB {
		t.Fatalf("Lookup returned the wrong session; want provider B's mux")
	}
}

// TestRedisStoreLookupAllLocalMuxesGone confirms a genuine miss: when every live
// provider for a name is a local delivery whose mux is gone, Lookup still reports
// not-found after exhausting them.
func TestRedisStoreLookupAllLocalMuxesGone(t *testing.T) {
	s := newTestStore(t, "r1")

	s.RegisterDeliver("A", "svc", newTestSession(t))
	s.RegisterDeliver("B", "svc", newTestSession(t))

	s.mu.Lock()
	delete(s.muxes, s.providerID("A", "svc"))
	delete(s.muxes, s.providerID("B", "svc"))
	s.mu.Unlock()

	if _, ok := s.Lookup("svc"); ok {
		t.Fatal("Lookup should miss when all live providers' muxes are gone")
	}
}

// TestRedisStoreLookupDeliverRoundRobin confirms the fix preserves round-robin across
// healthy local delivery providers.
func TestRedisStoreLookupDeliverRoundRobin(t *testing.T) {
	s := newTestStore(t, "r1")

	muxA := newTestSession(t)
	muxB := newTestSession(t)
	s.RegisterDeliver("A", "svc", muxA)
	s.RegisterDeliver("B", "svc", muxB)

	seen := map[*yamux.Session]int{}
	for i := 0; i < 4; i++ {
		tgt, ok := s.Lookup("svc")
		if !ok {
			t.Fatal("svc should resolve")
		}
		seen[tgt.Mux]++
	}
	if seen[muxA] != 2 || seen[muxB] != 2 {
		t.Fatalf("round-robin uneven: A=%d B=%d", seen[muxA], seen[muxB])
	}
}

func TestRedisStorePeerKeyRoundTripAndExpiry(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	s, err := NewRedisStore(context.Background(), "redis://"+mr.Addr(), "replica-a", "ws://replica-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	want := []byte("PUBLIC KEY PEM")
	if err := s.PublishPeerKey("replica-a", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.PeerKey("replica-a")
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("PeerKey = %q, ok=%v, err=%v", got, ok, err)
	}

	// The existing heartbeat refreshes both liveness and the peer key.
	mr.FastForward(aliveTTL - time.Second)
	if err := s.beat(context.Background()); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Second)
	if _, ok, err := s.PeerKey("replica-a"); err != nil || !ok {
		t.Fatalf("peer key was not refreshed with heartbeat: ok=%v err=%v", ok, err)
	}

	// Without another heartbeat, the peer key expires with the replica record.
	mr.FastForward(aliveTTL + time.Second)
	if got, ok, err := s.PeerKey("replica-a"); err != nil || ok || got != nil {
		t.Fatalf("expired PeerKey = %q, ok=%v, err=%v", got, ok, err)
	}
}

func TestRedisStoreCannotPublishAnotherReplicasKey(t *testing.T) {
	s := newTestStore(t, "replica-a")
	if err := s.PublishPeerKey("replica-b", []byte("PUBLIC")); err == nil {
		t.Fatal("published another replica's peer key")
	}
}

// TestHeartbeatRetryFitsInsideLivenessWindow enforces in code the constraint the
// beatAttempts comment states in words: one heartbeat tick's bounded retry must
// not consume a whole liveness window. If it can, an exhausted tick outlives the
// very liveness record it was refreshing — every provider this replica owns
// vanishes from every peer while the retry loop is still running, and nothing has
// yet reported a failure.
//
// The constants live in different files from the loop that spends them, which is
// how they drifted apart in the first place (the two hub stores carried 3s and 5s
// with no recorded reason, and the 5s side violated this bound). Asserting the
// relationship rather than the values lets either constant move for a good reason
// and fails only when the combination stops being safe.
func TestHeartbeatRetryFitsInsideLivenessWindow(t *testing.T) {
	// beatBounded sleeps i*beatBackoff before attempt i (i>0), so the backoffs sum
	// to (0+1+...+(beatAttempts-1)) * beatBackoff.
	var backoffSum time.Duration
	for i := 1; i < beatAttempts; i++ {
		backoffSum += time.Duration(i) * beatBackoff
	}
	worst := time.Duration(beatAttempts)*writeTimeout + backoffSum
	if worst >= aliveTTL {
		t.Fatalf("worst-case heartbeat tick is %v, which is not shorter than the %v liveness window: "+
			"an exhausted tick can outlive the record it renews (beatAttempts=%d, writeTimeout=%v, backoffSum=%v)",
			worst, aliveTTL, beatAttempts, writeTimeout, backoffSum)
	}
	// The heartbeat interval must also be comfortably shorter than the window, or a
	// single missed tick lapses the record.
	if heartbeatEvery*2 > aliveTTL {
		t.Fatalf("heartbeatEvery=%v leaves no room for a missed tick inside a %v window", heartbeatEvery, aliveTTL)
	}
}
