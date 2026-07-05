package hub

import (
	"context"
	"net"
	"testing"

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
