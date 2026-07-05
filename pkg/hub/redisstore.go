package hub

import (
	"context"
	"encoding/json"
	"fmt"
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

	mu sync.Mutex
	// owned tracks the providerIDs this replica registered so RemoveConn/Close can
	// HDEL them: connID -> providerID -> service name.
	owned map[string]map[string]string
	// muxes holds this replica's own delivery sessions keyed by providerID (the
	// process-local state a peer replica cannot see and must forward to reach).
	muxes map[string]*yamux.Session
	// rr is the per-name round-robin cursor across live providers.
	rr map[string]int
}

const (
	svcPrefix   = "hub:svc:"
	alivePrefix = "hub:alive:"

	// aliveTTL is how long a replica is considered live after a heartbeat; the
	// heartbeat interval must be comfortably shorter so a live replica never lapses.
	aliveTTL       = 15 * time.Second
	heartbeatEvery = 5 * time.Second
)

func svcKey(name string) string      { return svcPrefix + name }
func aliveKey(replica string) string { return alivePrefix + replica }

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
	// The store outlives the constructor ctx; its lifetime is bounded by Close.
	sctx, cancel := context.WithCancel(context.Background())
	s := &RedisStore{
		rdb:         rdb,
		replicaID:   replicaID,
		forwardAddr: forwardAddr,
		ctx:         sctx,
		cancel:      cancel,
		owned:       map[string]map[string]string{},
		muxes:       map[string]*yamux.Session{},
		rr:          map[string]int{},
	}
	s.beat()
	go s.heartbeat()
	return s, nil
}

// beat refreshes this replica's liveness key with a fresh TTL.
func (s *RedisStore) beat() { s.rdb.Set(s.ctx, aliveKey(s.replicaID), "1", aliveTTL) }

// heartbeat refreshes the liveness key until Close cancels s.ctx.
func (s *RedisStore) heartbeat() {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.beat()
		}
	}
}

func (s *RedisStore) providerID(connID, name string) string {
	return s.replicaID + ":" + connID + ":" + name
}

// Register adds a dial-direct provider (any replica can dial Addr) for name.
func (s *RedisStore) Register(connID, name, addr, protocol string) {
	s.put(connID, name, providerRecord{
		ConnID:      connID,
		Mode:        "direct",
		Addr:        addr,
		Owner:       s.replicaID,
		ForwardAddr: s.forwardAddr,
		Protocol:    protocol,
	}, nil)
}

// RegisterDeliver adds a delivery provider for name and keeps the spoke's mux
// process-local (only this replica can open the ingress stream; peers forward).
func (s *RedisStore) RegisterDeliver(connID, name string, mux *yamux.Session) {
	s.put(connID, name, providerRecord{
		ConnID:      connID,
		Mode:        "deliver",
		Owner:       s.replicaID,
		ForwardAddr: s.forwardAddr,
	}, mux)
}

func (s *RedisStore) put(connID, name string, rec providerRecord, mux *yamux.Session) {
	pid := s.providerID(connID, name)
	blob, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s.rdb.HSet(s.ctx, svcKey(name), pid, blob)
	s.mu.Lock()
	if s.owned[connID] == nil {
		s.owned[connID] = map[string]string{}
	}
	s.owned[connID][pid] = name
	if mux != nil {
		s.muxes[pid] = mux
	}
	s.mu.Unlock()
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
func (s *RedisStore) RemoveConn(connID string) {
	s.mu.Lock()
	pids := s.owned[connID]
	delete(s.owned, connID)
	toDel := make(map[string]string, len(pids))
	for pid, name := range pids {
		toDel[pid] = name
		delete(s.muxes, pid)
	}
	s.mu.Unlock()
	for pid, name := range toDel {
		s.rdb.HDel(s.ctx, svcKey(name), pid)
	}
}

// Close stops the heartbeat and best-effort removes this replica's providers and
// liveness key, then closes the Redis client. Safe to call once.
func (s *RedisStore) Close() error {
	s.cancel()

	s.mu.Lock()
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
	s.rdb.Del(ctx, aliveKey(s.replicaID))
	return s.rdb.Close()
}
