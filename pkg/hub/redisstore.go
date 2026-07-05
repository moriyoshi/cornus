package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/redis/go-redis/v9"
)

// RedisStore is a distributed hub.Store backed by Redis: the shared service
// registry that makes the hub multi-replica. Every replica writes its own
// providers into Redis and reads the merged view, so a dial-direct service
// registered on any replica is reachable from any other (shared metadata, no
// forwarding). A DELIVERY service, however, holds a process-local *yamux.Session
// to the hosting spoke — only the owning replica can open the ingress stream — so
// a Lookup on a peer replica returns a ForwardAddr disposition instead, and the
// caller (server.hubRelay) forwards the relay to the owner (see /.cornus/v1/hub/forward).
//
// Liveness is a per-replica TTL key refreshed by a heartbeat goroutine: Lookup and
// Catalog drop providers whose owner replica's alive key is absent (the replica
// died), so a crashed replica's whole partition disappears without any explicit
// cleanup. See .agents/docs/ARCHITECTURE.md ("Multi-replica hub").
type RedisStore struct {
	rdb         *redis.Client
	replicaID   string
	forwardAddr string

	// ctx governs every Redis operation and the heartbeat; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc

	// beatEvery is the heartbeat/reconcile period (heartbeatEvery; shortened by tests).
	beatEvery time.Duration

	// wmu serializes every SHARED-STATE mutation — the inline HSet/HDEL of
	// Register/RegisterDeliver/RemoveConn and the reconcile pass — so the order in
	// which ops are enqueued into pending is the order in which they hit Redis.
	// Without that total order a retried write could land after the RemoveConn that
	// superseded it and resurrect a dead provider. It also means a struggling Redis
	// sees at most one outstanding write from this replica instead of one per spoke.
	// The read path (Lookup/Catalog) never takes it.
	wmu sync.Mutex

	mu sync.Mutex
	// owned tracks the providerIDs this replica registered so RemoveConn/Close can
	// HDEL them: connID -> providerID -> service name.
	owned map[string]map[string]string
	// muxes holds this replica's own delivery sessions keyed by providerID (the
	// process-local state a peer replica cannot see and must forward to reach).
	muxes map[string]*yamux.Session
	// rr is the per-name round-robin cursor across live providers.
	rr map[string]int
	// pending is the reconcile queue: providerID -> the DESIRED shared state whose
	// write has not landed yet (blob nil means "absent"). An entry is added by every
	// mutation and cleared as soon as its write succeeds, so in the healthy case it
	// is empty; a Redis outage leaves it populated and the heartbeat loop re-drives
	// it until it drains. It is what makes a failed Register recoverable rather than
	// lost: the local view stays authoritative and is re-asserted on reconnect.
	pending map[string]pendingOp
	// seq numbers pending ops so a write that was superseded while in flight clears
	// nothing (the newer op owns the entry).
	seq uint64
	// beatErr is the last heartbeat outcome (nil = the liveness key is fresh).
	beatErr error
	// peerKey is this replica's public verification key. The private half never
	// enters Redis; beat refreshes this value under the same TTL as liveness.
	peerKey []byte
}

// pendingOp is the desired shared-store state for one providerID: blob is the record
// to HSet, or nil to HDEL. name is the service (the Redis hash key).
type pendingOp struct {
	name string
	blob []byte
	seq  uint64
}

const (
	svcPrefix   = "hub:svc:"
	alivePrefix = "hub:alive:"
	peerPrefix  = "hub:peer-key:"

	// aliveTTL is how long a replica is considered live after a heartbeat; the
	// heartbeat interval must be comfortably shorter so a live replica never lapses.
	aliveTTL       = 15 * time.Second
	heartbeatEvery = 5 * time.Second

	// writeTimeout bounds ONE shared-store operation. Every write is serialized
	// (wmu), so an unbounded op during an outage would stall every other spoke's
	// registration behind a client-level dial timeout.
	//
	// It is bounded above by the invariant the beatAttempts comment below states in
	// words — beatAttempts*writeTimeout + backoffSum < aliveTTL — so one exhausted
	// heartbeat tick can never outlive the liveness key it is refreshing. Here that
	// is 9.3s against 15s. pkg/kubehub carries the same invariant with a longer
	// per-write bound (an API-server write goes through admission and etcd);
	// TestHeartbeatRetryFitsInsideLivenessWindow enforces it in both packages.
	writeTimeout = 3 * time.Second

	// beatAttempts is the bounded retry for one heartbeat tick: the heartbeat is the
	// write whose silent failure has the most delayed symptom (every provider this
	// replica owns disappears from its peers aliveTTL later), so a single blip must
	// not consume a whole liveness window.
	beatAttempts = 3
	beatBackoff  = 100 * time.Millisecond
)

func svcKey(name string) string        { return svcPrefix + name }
func aliveKey(replica string) string   { return alivePrefix + replica }
func peerKeyKey(replica string) string { return peerPrefix + replica }

// providerRecord is the JSON value stored in the per-service Redis hash under a
// providerID. mode is "direct" (dial Addr) or "deliver" (open an ingress stream to
// owner's spoke). owner is the replica that registered it; forwardAddr is that
// replica's inter-replica base URL a peer dials for a remote delivery.
type providerRecord struct {
	ConnID      string `json:"connID"`
	Mode        string `json:"mode"`
	Addr        string `json:"addr,omitempty"`
	Owner       string `json:"owner"`
	ForwardAddr string `json:"forwardAddr,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
}

// NewRedisStore connects to redisURL and returns a distributed Store for this
// replica. replicaID uniquely names the replica (its provider partition and its
// liveness key); forwardAddr is this replica's inter-replica base URL (e.g.
// "ws://podIP:5000") that peers dial to forward a remote delivery to it. An
// unreachable or malformed Redis URL is a hard error (fail closed at startup). The
// constructor writes the first heartbeat and starts the refresh goroutine.
func NewRedisStore(ctx context.Context, redisURL, replicaID, forwardAddr string) (*RedisStore, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("hub: parse redis url: %w", err)
	}
	rdb := redis.NewClient(opt)
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("hub: redis ping: %w", err)
	}
	return newRedisStore(rdb, replicaID, forwardAddr, heartbeatEvery)
}

// newRedisStore builds the store over a live client and starts the heartbeat. It is
// the seam the tests use to drive a short reconcile period.
func newRedisStore(rdb *redis.Client, replicaID, forwardAddr string, beatEvery time.Duration) (*RedisStore, error) {
	// The store outlives the constructor ctx; its lifetime is bounded by Close.
	sctx, cancel := context.WithCancel(context.Background())
	s := &RedisStore{
		rdb:         rdb,
		replicaID:   replicaID,
		forwardAddr: forwardAddr,
		ctx:         sctx,
		cancel:      cancel,
		beatEvery:   beatEvery,
		owned:       map[string]map[string]string{},
		muxes:       map[string]*yamux.Session{},
		rr:          map[string]int{},
		pending:     map[string]pendingOp{},
	}
	// The first heartbeat is a hard startup error: without a liveness key nothing
	// this replica registers is ever visible to a peer, so failing here is far
	// better than serving a hub whose providers no peer will ever see (it matches
	// kubehub's initial-Lease behaviour, and the fail-closed Ping above).
	if err := s.beat(s.ctx); err != nil {
		cancel()
		_ = rdb.Close()
		return nil, fmt.Errorf("hub: redis initial heartbeat: %w", err)
	}
	go s.heartbeat()
	return s, nil
}

// logger is resolved per call so a logging.Init (or a test's slog.SetDefault) that
// runs after construction still takes effect.
func (s *RedisStore) logger() *slog.Logger {
	return slog.Default().With("component", "hub", "store", "redis", "replica", s.replicaID)
}

// beat refreshes this replica's liveness and, once published, public peer key
// under the same TTL. A departed replica therefore loses both records together.
func (s *RedisStore) beat(ctx context.Context) error {
	s.mu.Lock()
	peerKey := append([]byte(nil), s.peerKey...)
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	pipe := s.rdb.Pipeline()
	pipe.Set(cctx, aliveKey(s.replicaID), "1", aliveTTL)
	if len(peerKey) > 0 {
		pipe.Set(cctx, peerKeyKey(s.replicaID), peerKey, aliveTTL)
	}
	_, err := pipe.Exec(cctx)
	return err
}

// beatBounded retries one heartbeat a bounded number of times before giving up for
// this tick. aliveTTL is three tick periods, so a tick that burns a little on retries
// still leaves the liveness key comfortably fresh.
func (s *RedisStore) beatBounded(ctx context.Context) error {
	var err error
	for i := 0; i < beatAttempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * beatBackoff):
			}
		}
		if err = s.beat(ctx); err == nil {
			return nil
		}
	}
	return err
}

// heartbeat refreshes the liveness key and re-drives failed provider writes until
// Close cancels s.ctx.
func (s *RedisStore) heartbeat() {
	t := time.NewTicker(s.beatEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick is one heartbeat period: refresh liveness (bounded retry, reporting a change
// of state), then reconcile any provider write that has not landed. The heartbeat
// runs FIRST and the reconcile is deadline-bounded, so a large backlog can never
// starve the liveness key that every one of those providers depends on.
func (s *RedisStore) tick() {
	s.noteBeat(s.beatBounded(s.ctx))
	s.reconcile(time.Now().Add(s.beatEvery / 2))
}

// noteBeat records the heartbeat outcome and logs the TRANSITIONS only (failing,
// recovered) rather than every tick: a Redis outage would otherwise produce a log
// line every heartbeat for its whole duration, and the persisting condition is
// already readable from StoreHealth (which the server exposes as a gauge and on
// /readyz). The failure is logged at ERROR because its consequence is not local: once
// the TTL lapses, every provider this replica owns vanishes from every peer, with no
// other event to explain it.
func (s *RedisStore) noteBeat(err error) {
	s.mu.Lock()
	prev := s.beatErr
	s.beatErr = err
	s.mu.Unlock()
	switch {
	case err != nil && prev == nil:
		s.logger().Error("hub: liveness heartbeat failed; this replica's registered services disappear from peer replicas once the liveness TTL lapses",
			"ttl", aliveTTL, "error", err)
	case err == nil && prev != nil:
		s.logger().Info("hub: liveness heartbeat recovered; this replica is visible to peer replicas again")
	}
}

// reconcile re-drives every pending op (a write whose Redis call failed) until the
// queue drains or the deadline passes; whatever is left is retried next tick. It is
// the recovery half of the contract: a Register that failed during an outage becomes
// visible cluster-wide again on its own, without the spoke re-registering.
func (s *RedisStore) reconcile(deadline time.Time) {
	s.mu.Lock()
	pids := make([]string, 0, len(s.pending))
	for pid := range s.pending {
		pids = append(pids, pid)
	}
	s.mu.Unlock()
	if len(pids) == 0 {
		return
	}
	sort.Strings(pids)
	var healed, failed int
	for _, pid := range pids {
		if time.Now().After(deadline) {
			break
		}
		applied, err := s.reconcileOne(pid)
		switch {
		case err != nil:
			failed++
		case applied:
			healed++
		}
	}
	if healed > 0 {
		s.logger().Info("hub: re-applied hub registry writes that had failed; they are visible to peer replicas again",
			"reapplied", healed, "still.pending", failed)
	}
}

// StoreHealth reports whether this replica's shared state is trustworthy: nil when
// the liveness key is fresh and every provider write has landed. See hub.HealthReporter.
func (s *RedisStore) StoreHealth() error {
	s.mu.Lock()
	beatErr, pending := s.beatErr, len(s.pending)
	s.mu.Unlock()
	if beatErr != nil {
		return fmt.Errorf("hub: liveness heartbeat failing (peers drop this replica's services %s after the last good beat): %w", aliveTTL, beatErr)
	}
	if pending > 0 {
		return fmt.Errorf("hub: %d hub registry write(s) not yet visible to peer replicas", pending)
	}
	return nil
}

func (s *RedisStore) providerID(connID, name string) string {
	return s.replicaID + ":" + connID + ":" + name
}

// Register adds a dial-direct provider (any replica can dial Addr) for name. The
// error reports that the provider is not visible to peer replicas YET; it stays in
// the reconcile queue and the heartbeat re-drives it.
func (s *RedisStore) Register(connID, name, addr, protocol string) error {
	return s.put(connID, name, providerRecord{
		ConnID:      connID,
		Mode:        "direct",
		Addr:        addr,
		Owner:       s.replicaID,
		ForwardAddr: s.forwardAddr,
		Protocol:    protocol,
	}, nil)
}

// RegisterDeliver adds a delivery provider for name and keeps the spoke's mux
// process-local (only this replica can open the ingress stream; peers forward). See
// Register for the error's meaning.
func (s *RedisStore) RegisterDeliver(connID, name string, mux *yamux.Session) error {
	return s.put(connID, name, providerRecord{
		ConnID:      connID,
		Mode:        "deliver",
		Owner:       s.replicaID,
		ForwardAddr: s.forwardAddr,
	}, mux)
}

// put records the provider locally (authoritative, and what the reconcile loop
// replays from) and writes it to Redis. The local record is kept even when the write
// fails — that is precisely what lets the provider be re-asserted on recovery.
func (s *RedisStore) put(connID, name string, rec providerRecord, mux *yamux.Session) error {
	pid := s.providerID(connID, name)
	blob, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("hub: encode provider record for %q: %w", name, err)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	if s.owned[connID] == nil {
		s.owned[connID] = map[string]string{}
	}
	s.owned[connID][pid] = name
	if mux != nil {
		s.muxes[pid] = mux
	}
	op := s.enqueueLocked(pid, pendingOp{name: name, blob: blob})
	s.mu.Unlock()
	if err := s.applyOpLocked(pid, op); err != nil {
		return fmt.Errorf("hub: register service %q in the shared registry (queued for retry): %w", name, err)
	}
	return nil
}

// enqueueLocked records op as the desired shared state for pid and stamps it with the
// next sequence number. s.mu must be held; the caller must hold wmu too, so enqueue
// order and apply order agree.
func (s *RedisStore) enqueueLocked(pid string, op pendingOp) pendingOp {
	s.seq++
	op.seq = s.seq
	s.pending[pid] = op
	return op
}

// applyOpLocked performs one pending op against Redis and clears it from the queue if
// it succeeded and was not superseded in the meantime. wmu must be held.
func (s *RedisStore) applyOpLocked(pid string, op pendingOp) error {
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	var err error
	if op.blob == nil {
		err = s.rdb.HDel(ctx, svcKey(op.name), pid).Err()
	} else {
		err = s.rdb.HSet(ctx, svcKey(op.name), pid, op.blob).Err()
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	if cur, ok := s.pending[pid]; ok && cur.seq == op.seq {
		delete(s.pending, pid)
	}
	s.mu.Unlock()
	return nil
}

// reconcileOne re-drives the pending op for pid, if one is still queued. Reading the
// queue under wmu (not just mu) is what keeps a retry from overtaking a mutation that
// was enqueued after it.
func (s *RedisStore) reconcileOne(pid string) (applied bool, err error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	op, ok := s.pending[pid]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := s.applyOpLocked(pid, op); err != nil {
		return false, err
	}
	return true, nil
}

// Lookup returns one live provider for name, round-robin across the merged view of
// all replicas' providers, dropping any whose owner replica is dead (liveness TTL
// lapsed). A remote delivery resolves to a ForwardAddr/ForwardName disposition.
func (s *RedisStore) Lookup(name string) (Target, bool) {
	live, recs := s.liveProviders(name)
	if len(live) == 0 {
		return Target{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Iterate live providers round-robin starting at the cursor, skipping any that
	// are momentarily unusable, and only miss after all are exhausted. A local
	// delivery whose mux is nil is such a case: RemoveConn deletes the in-memory mux
	// under the lock BEFORE issuing the Redis HDEL, so there is a window where the
	// provider is still in the merged Redis view but its session is already gone.
	// Returning not-found on it would spuriously fail a dial even though sibling
	// providers for the same name are healthy, so fall through to them instead.
	start := s.rr[name]
	for off := 0; off < len(live); off++ {
		i := (start + off) % len(live)
		pid := live[i]
		rec := recs[pid]
		switch rec.Mode {
		case "direct":
			s.rr[name] = i + 1
			return Target{Addr: rec.Addr, Protocol: rec.Protocol}, true
		case "deliver":
			if rec.Owner == s.replicaID {
				mux := s.muxes[pid]
				if mux == nil {
					continue // local mux removed but HDEL not yet applied; try next
				}
				s.rr[name] = i + 1
				return Target{Mux: mux}, true
			}
			// Remote delivery: the owner replica holds the spoke session; the relay
			// forwards to owner.ForwardAddr, which opens the ingress stream there.
			s.rr[name] = i + 1
			return Target{ForwardAddr: rec.ForwardAddr, ForwardName: name}, true
		}
	}
	return Target{}, false
}

// liveProviders returns the sorted providerIDs for name whose owner replica is
// live, plus the decoded records. Ordering is stable (sorted providerIDs) so the
// round-robin cursor is meaningful across calls.
func (s *RedisStore) liveProviders(name string) ([]string, map[string]providerRecord) {
	all, err := s.rdb.HGetAll(s.ctx, svcKey(name)).Result()
	if err != nil || len(all) == 0 {
		return nil, nil
	}
	pids := make([]string, 0, len(all))
	recs := make(map[string]providerRecord, len(all))
	owners := map[string]struct{}{}
	for pid, blob := range all {
		var rec providerRecord
		if json.Unmarshal([]byte(blob), &rec) != nil {
			continue
		}
		pids = append(pids, pid)
		recs[pid] = rec
		owners[rec.Owner] = struct{}{}
	}
	sort.Strings(pids)
	liveOwner := s.liveOwners(owners)
	live := make([]string, 0, len(pids))
	for _, pid := range pids {
		if _, ok := liveOwner[recs[pid].Owner]; ok {
			live = append(live, pid)
		}
	}
	return live, recs
}

// liveOwners returns the subset of owners whose liveness key still exists, tested
// in a single pipelined EXISTS batch.
func (s *RedisStore) liveOwners(owners map[string]struct{}) map[string]struct{} {
	if len(owners) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make(map[string]*redis.IntCmd, len(owners))
	for o := range owners {
		cmds[o] = pipe.Exists(s.ctx, aliveKey(o))
	}
	if _, err := pipe.Exec(s.ctx); err != nil && err != redis.Nil {
		return nil
	}
	live := make(map[string]struct{}, len(cmds))
	for o, cmd := range cmds {
		if n, err := cmd.Result(); err == nil && n > 0 {
			live[o] = struct{}{}
		}
	}
	return live
}

// Catalog returns the sorted service names that currently have at least one live
// provider anywhere in the cluster.
func (s *RedisStore) Catalog() []string {
	iter := s.rdb.Scan(s.ctx, 0, svcPrefix+"*", 0).Iterator()
	var names []string
	for iter.Next(s.ctx) {
		name := strings.TrimPrefix(iter.Val(), svcPrefix)
		if live, _ := s.liveProviders(name); len(live) > 0 {
			names = append(names, name)
		}
	}
	if err := iter.Err(); err != nil {
		return nil
	}
	sort.Strings(names)
	return names
}

// RemoveConn drops every provider this replica registered under connID (HDEL) and
// forgets its local delivery muxes. Called when the spoke's hub connection drops.
//
// A failed HDEL is NOT harmless and is not simply dropped: this replica keeps
// heartbeating, so the stale record stays "live" in every peer's merged view and peers
// keep routing to a spoke that is gone (unlike a replica crash, which takes the whole
// partition with it). The removal is therefore queued like a write and re-driven by
// the heartbeat; the error tells the caller the withdrawal has not landed yet.
func (s *RedisStore) RemoveConn(connID string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	pids := s.owned[connID]
	delete(s.owned, connID)
	ops := make(map[string]pendingOp, len(pids))
	for pid, name := range pids {
		delete(s.muxes, pid)
		ops[pid] = s.enqueueLocked(pid, pendingOp{name: name})
	}
	s.mu.Unlock()
	var errs []error
	for pid, op := range ops {
		if err := s.applyOpLocked(pid, op); err != nil {
			errs = append(errs, fmt.Errorf("hub: withdraw service %q from the shared registry (queued for retry): %w", op.name, err))
		}
	}
	return errors.Join(errs...)
}

// PublishPeerKey publishes only this store's replica public key. The key is
// retained locally before the write so the heartbeat can re-publish it after a
// transient Redis failure; the error still reports that peers cannot verify yet.
func (s *RedisStore) PublishPeerKey(replicaID string, publicKeyPEM []byte) error {
	if replicaID == "" || replicaID != s.replicaID {
		return fmt.Errorf("hub: cannot publish peer key for replica %q from replica %q", replicaID, s.replicaID)
	}
	if len(publicKeyPEM) == 0 {
		return errors.New("hub: cannot publish an empty peer key")
	}
	key := append([]byte(nil), publicKeyPEM...)
	s.mu.Lock()
	s.peerKey = key
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	if err := s.rdb.Set(ctx, peerKeyKey(replicaID), key, aliveTTL).Err(); err != nil {
		return fmt.Errorf("hub: publish peer key for %q: %w", replicaID, err)
	}
	return nil
}

// PeerKey returns a live replica's published public key. Redis expiry is the
// liveness check: the key carries the same TTL and heartbeat as aliveKey.
func (s *RedisStore) PeerKey(replicaID string) ([]byte, bool, error) {
	if replicaID == "" {
		return nil, false, errors.New("hub: empty peer replica id")
	}
	raw, err := s.rdb.Get(s.ctx, peerKeyKey(replicaID)).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("hub: read peer key for %q: %w", replicaID, err)
	}
	return append([]byte(nil), raw...), true, nil
}

// Close stops the heartbeat and best-effort removes this replica's providers and
// liveness key, then closes the Redis client. Safe to call once.
//
// The cleanup writes stay deliberately best-effort (errors dropped, nothing queued):
// deleting the liveness key is what actually retires this replica's whole partition
// from every peer's view, and if even that fails the TTL retires it aliveTTL later.
// There is no later tick to retry in, and nothing a caller could do with the error.
func (s *RedisStore) Close() error {
	s.cancel()

	s.mu.Lock()
	s.pending = map[string]pendingOp{}
	type del struct{ name, pid string }
	var dels []del
	for _, pids := range s.owned {
		for pid, name := range pids {
			dels = append(dels, del{name: name, pid: pid})
		}
	}
	s.owned = map[string]map[string]string{}
	s.muxes = map[string]*yamux.Session{}
	s.mu.Unlock()

	// s.ctx is cancelled; use a fresh short-lived ctx for the cleanup writes.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, d := range dels {
		s.rdb.HDel(ctx, svcKey(d.name), d.pid)
	}
	s.rdb.Del(ctx, aliveKey(s.replicaID), peerKeyKey(s.replicaID))
	return s.rdb.Close()
}
