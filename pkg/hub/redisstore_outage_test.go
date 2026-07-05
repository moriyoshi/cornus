package hub

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// A shared-store outage is the failure these tests are about: the replica keeps
// serving local lookups while its writes into Redis are rejected, so its providers
// are invisible to every peer. The store must (a) REPORT that, and (b) heal on its
// own when Redis comes back — the mutation never being retried is what made the
// original fire-and-forget writes so hard to diagnose.

// outageStores builds two RedisStores ("r1" and its peer "r2") over ONE miniredis, so
// a test can assert what a PEER replica sees, and returns the miniredis for outage
// injection (mr.SetError makes every command fail; "" restores service). Both stores
// are constructed before any outage, since construction is deliberately fail-closed.
// beatEvery drives the heartbeat/reconcile goroutine; pass a long period and call
// tick() to drive reconciliation deterministically.
func outageStores(t *testing.T, beatEvery time.Duration) (*miniredis.Miniredis, *RedisStore, *RedisStore) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis run: %v", err)
	}
	t.Cleanup(mr.Close)
	newStore := func(id string) *RedisStore {
		opt, err := redis.ParseURL("redis://" + mr.Addr())
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		s, err := newRedisStore(redis.NewClient(opt), id, "ws://"+id, beatEvery)
		if err != nil {
			t.Fatalf("new redis store %s: %v", id, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	return mr, newStore("r1"), newStore("r2")
}

// TestRedisStoreRegisterReportsOutageAndRecovers is the core of the fix: a Register
// whose Redis write fails must SAY so (rather than returning as if it had landed),
// and the provider must become visible to peers once Redis recovers — without the
// spoke re-registering.
func TestRedisStoreRegisterReportsOutageAndRecovers(t *testing.T) {
	mr, s1, peer := outageStores(t, time.Hour)

	mr.SetError("LOADING Redis is loading the dataset in memory")
	err := s1.Register("c1", "web", "10.0.0.1:80", "")
	if err == nil {
		t.Fatal("Register must report the failed shared-store write, got nil error")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Fatalf("error should name the service, got %v", err)
	}
	if s1.StoreHealth() == nil {
		t.Fatal("StoreHealth must report the degradation while the write is outstanding")
	}

	// Recovery: the heartbeat's reconcile pass re-asserts the queued write, and the
	// PEER replica can now resolve the service.
	mr.SetError("")
	s1.tick()

	if err := s1.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear after recovery: %v", err)
	}
	tgt, ok := peer.Lookup("web")
	if !ok {
		t.Fatal("peer replica should see the re-registered provider after recovery")
	}
	if tgt.Addr != "10.0.0.1:80" {
		t.Fatalf("peer resolved the wrong target: %+v", tgt)
	}
}

// TestRedisStoreRemoveConnReportsOutageAndRecovers covers the other direction: a
// withdrawal that fails is NOT harmless — this replica keeps heartbeating, so the
// stale record stays live in every peer's view — so it must be reported and retried.
func TestRedisStoreRemoveConnReportsOutageAndRecovers(t *testing.T) {
	mr, s1, peer := outageStores(t, time.Hour)

	if err := s1.Register("c1", "web", "10.0.0.1:80", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := peer.Lookup("web"); !ok {
		t.Fatal("peer should see the provider before the outage")
	}

	mr.SetError("READONLY You can't write against a read only replica")
	if err := s1.RemoveConn("c1"); err == nil {
		t.Fatal("RemoveConn must report the failed withdrawal, got nil error")
	}
	if s1.StoreHealth() == nil {
		t.Fatal("StoreHealth must report the outstanding withdrawal")
	}

	mr.SetError("")
	s1.tick()

	if err := s1.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear after recovery: %v", err)
	}
	if _, ok := peer.Lookup("web"); ok {
		t.Fatal("peer should no longer see the provider after the withdrawal is re-applied")
	}
}

// TestRedisStoreHeartbeatFailureLoggedOnce is the delayed-symptom path: a heartbeat
// that stops landing makes every provider this replica owns vanish from its peers a
// liveness TTL later, far from the cause. It must log at ERROR when it starts
// failing, NOT once per tick (an outage would otherwise flood the log), and log
// again when it recovers.
func TestRedisStoreHeartbeatFailureLoggedOnce(t *testing.T) {
	logs := captureLogs(t)
	mr, s1, _ := outageStores(t, time.Hour)

	mr.SetError("LOADING Redis is loading the dataset in memory")
	s1.tick()
	s1.tick()

	if err := s1.StoreHealth(); err == nil {
		t.Fatal("StoreHealth must report a failing heartbeat")
	} else if !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("StoreHealth should explain the heartbeat failure, got %v", err)
	}
	if n := logs.count(slog.LevelError, "liveness heartbeat failed"); n != 1 {
		t.Fatalf("heartbeat failure should be logged once per outage (transition), got %d", n)
	}

	mr.SetError("")
	s1.tick()

	if err := s1.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear after the heartbeat recovers: %v", err)
	}
	if n := logs.count(slog.LevelInfo, "liveness heartbeat recovered"); n != 1 {
		t.Fatalf("heartbeat recovery should be logged once, got %d", n)
	}
}

// TestRedisStoreHeartbeatGoroutineHeals proves the recovery is automatic: nothing
// calls tick(), the store's own heartbeat goroutine re-applies the write that failed
// during the outage.
func TestRedisStoreHeartbeatGoroutineHeals(t *testing.T) {
	mr, s1, peer := outageStores(t, 20*time.Millisecond)

	mr.SetError("LOADING Redis is loading the dataset in memory")
	if err := s1.Register("c1", "web", "10.0.0.1:80", ""); err == nil {
		t.Fatal("Register must report the failed shared-store write")
	}
	mr.SetError("")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := peer.Lookup("web"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider never re-registered; store health: %v", s1.StoreHealth())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s1.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear once the queue drains: %v", err)
	}
}

// TestRedisStoreHealthyStoreReportsNoError guards the negative: the health signal must
// be quiet in the healthy case, or it is useless as an alert.
func TestRedisStoreHealthyStoreReportsNoError(t *testing.T) {
	_, s1, _ := outageStores(t, time.Hour)
	if err := s1.Register("c1", "web", "10.0.0.1:80", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s1.RemoveConn("c1"); err != nil {
		t.Fatalf("RemoveConn: %v", err)
	}
	s1.tick()
	if err := s1.StoreHealth(); err != nil {
		t.Fatalf("a healthy store must report no degradation, got %v", err)
	}
}

// --- log capture --------------------------------------------------------------

// logRecorder collects slog records so a test can assert on what was logged (and how
// loudly), since "the failure is reported" is half the contract here.
type logRecorder struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
	return nil
}

func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

// count returns how many records at level contain substr in their message.
func (r *logRecorder) count(level slog.Level, substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.recs {
		if rec.Level == level && strings.Contains(rec.Message, substr) {
			n++
		}
	}
	return n
}

// captureLogs redirects the default slog logger (which the store resolves per call)
// into a recorder for the duration of the test.
func captureLogs(t *testing.T) *logRecorder {
	t.Helper()
	prev := slog.Default()
	r := &logRecorder{}
	slog.SetDefault(slog.New(r))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return r
}
