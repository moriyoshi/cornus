package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/hub"
)

// These cover the SERVER's half of the shared-store contract: what each call site
// does when a distributed hub store reports that a write did not land. The store
// itself (retry, recovery) is covered in pkg/hub and pkg/kubehub.

// outageStore is a distributed-looking hub.Store whose writes fail while its outage
// switch is on. It embeds *hub.Registry (not the interface) so lookups keep working
// during the outage — the real asymmetry — and so it is NOT a *hub.Registry itself,
// which is how the server decides a store is distributed (hubDistributed).
type outageStore struct {
	inner *hub.Registry

	mu     sync.Mutex
	outage bool
}

func newOutageStore() *outageStore { return &outageStore{inner: hub.NewRegistry()} }

func (o *outageStore) setOutage(on bool) {
	o.mu.Lock()
	o.outage = on
	o.mu.Unlock()
}

func (o *outageStore) failing() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.outage
}

var errStoreOutage = errors.New("shared store unavailable")

func (o *outageStore) Register(connID, name, addr, protocol string) error {
	_ = o.inner.Register(connID, name, addr, protocol)
	if o.failing() {
		return errStoreOutage
	}
	return nil
}

func (o *outageStore) RegisterDeliver(connID, name string, mux *yamux.Session) error {
	_ = o.inner.RegisterDeliver(connID, name, mux)
	if o.failing() {
		return errStoreOutage
	}
	return nil
}

func (o *outageStore) RemoveConn(connID string) error {
	_ = o.inner.RemoveConn(connID)
	if o.failing() {
		return errStoreOutage
	}
	return nil
}

func (o *outageStore) Lookup(name string) (hub.Target, bool) { return o.inner.Lookup(name) }
func (o *outageStore) Catalog() []string                     { return o.inner.Catalog() }

func (o *outageStore) PublishPeerKey(replicaID string, publicKeyPEM []byte) error {
	if o.failing() {
		return errStoreOutage
	}
	return o.inner.PublishPeerKey(replicaID, publicKeyPEM)
}

func (o *outageStore) PeerKey(replicaID string) ([]byte, bool, error) {
	if o.failing() {
		return nil, false, errStoreOutage
	}
	return o.inner.PeerKey(replicaID)
}

// StoreHealth makes it a hub.HealthReporter, like the Redis and kube stores.
func (o *outageStore) StoreHealth() error {
	if o.failing() {
		return errStoreOutage
	}
	return nil
}

// TestHubControlLogsFailedSharedRegistration pins the loudest decision: a
// registration that lands locally but not in the shared registry is an ERROR, because
// the service is then reachable through THIS replica and silently unreachable through
// every other one. The connection is deliberately not torn down.
func TestHubControlLogsFailedSharedRegistration(t *testing.T) {
	logs := captureServerLogs(t)
	store := newOutageStore()
	s := &Server{hub: store}
	store.setOutage(true)

	spoke, server := net.Pipe()
	defer spoke.Close()
	go s.hubControl(context.Background(), newHubConn("conn1"), nil, server)

	if err := json.NewEncoder(spoke).Encode(hub.Registration{
		Services: []hub.Service{{Name: "web", Addr: "10.0.0.1:80"}},
	}); err != nil {
		t.Fatalf("encode registration: %v", err)
	}

	if !logs.waitFor(t, slog.LevelError, "not in the shared registry") {
		t.Fatal("a registration that failed to reach the shared registry must be logged at ERROR")
	}
	// The local registration still stands: relays landing on this replica work, which
	// is why the connection is not failed.
	if _, ok := store.Lookup("web"); !ok {
		t.Fatal("the service must stay registered locally after a shared-store failure")
	}
}

// TestHubControlQuietOnHealthyRegistration is the negative: no ERROR when the write
// lands, or the log is noise.
func TestHubControlQuietOnHealthyRegistration(t *testing.T) {
	logs := captureServerLogs(t)
	store := newOutageStore()
	s := &Server{hub: store}

	spoke, server := net.Pipe()
	defer spoke.Close()
	go s.hubControl(context.Background(), newHubConn("conn1"), nil, server)

	if err := json.NewEncoder(spoke).Encode(hub.Registration{
		Services: []hub.Service{{Name: "web", Addr: "10.0.0.1:80"}},
	}); err != nil {
		t.Fatalf("encode registration: %v", err)
	}
	// Wait until the registration is visible, then assert nothing was logged.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := store.Lookup("web"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("registration never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := logs.count(slog.LevelError, "shared registry"); n != 0 {
		t.Fatalf("healthy registration must log nothing, got %d ERROR records", n)
	}
}

// TestMountSessionAdvertiseReportsOutage covers the mount-session routing record: on
// a distributed store the advertise/withdraw errors must reach the caller (which logs
// them), since a lost record shows up much later as a mount reset blamed on "no
// replica owns this session".
func TestMountSessionAdvertiseReportsOutage(t *testing.T) {
	store := newOutageStore()
	s := &Server{hub: store}

	if err := s.registerMountSession("session-1"); err != nil {
		t.Fatalf("healthy advertise should succeed: %v", err)
	}
	store.setOutage(true)
	if err := s.registerMountSession("session-2"); err == nil {
		t.Fatal("a failed mount-session advertise must be reported")
	}
	if err := s.unregisterMountSession("session-1"); err == nil {
		t.Fatal("a failed mount-session withdrawal must be reported")
	}

	// The single-replica in-memory store has no shared state and never reports.
	local := &Server{hub: hub.NewRegistry()}
	if err := local.registerMountSession("session-3"); err != nil {
		t.Fatalf("single-replica advertise must stay a no-op: %v", err)
	}
	if err := local.unregisterMountSession("session-3"); err != nil {
		t.Fatalf("single-replica withdrawal must stay a no-op: %v", err)
	}
}

// TestReadyzSurfacesHubStoreDegradation pins the readiness contract: a degraded hub
// store is REPORTED on /readyz but must not flip the probe red — a 503 would pull
// this replica (and, during a shared-store outage, every replica) out of its Service
// endpoints, taking down registry/build/deploy traffic that is perfectly healthy.
func TestReadyzSurfacesHubStoreDegradation(t *testing.T) {
	store := newOutageStore()
	s := &Server{hub: store}
	s.ready.Store(true)

	body := func() map[string]string {
		rec := httptest.NewRecorder()
		s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("readyz = %d, want 200 (a degraded hub store must not fail readiness)", rec.Code)
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode readyz body: %v", err)
		}
		return out
	}

	if got := body(); got["status"] != "ready" || got["hub"] != "" {
		t.Fatalf("healthy readyz body = %v, want a plain ready status", got)
	}

	store.setOutage(true)
	got := body()
	if got["status"] != "ready" || got["hub"] != "degraded" {
		t.Fatalf("degraded readyz body = %v, want status=ready hub=degraded", got)
	}
	if !strings.Contains(got["hubError"], errStoreOutage.Error()) {
		t.Fatalf("readyz body should explain the degradation, got %v", got)
	}

	// Recovery clears it: the signal must be a live view, not a latch.
	store.setOutage(false)
	if got := body(); got["hub"] != "" {
		t.Fatalf("readyz body after recovery = %v, want no degradation", got)
	}
}

// TestReadyzIgnoresNonReportingStore keeps the single-replica default untouched: the
// in-memory Registry reports no health, so the body is byte-identical to before.
func TestReadyzIgnoresNonReportingStore(t *testing.T) {
	s := &Server{hub: hub.NewRegistry()}
	s.ready.Store(true)
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ready"`) {
		t.Fatalf("readyz = %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hub") {
		t.Fatalf("single-replica readyz body must not mention the hub store: %q", rec.Body.String())
	}
}

// --- log capture --------------------------------------------------------------

type serverLogRecorder struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (r *serverLogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *serverLogRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
	return nil
}

func (r *serverLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *serverLogRecorder) WithGroup(string) slog.Handler      { return r }

func (r *serverLogRecorder) count(level slog.Level, substr string) int {
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

// waitFor polls for a matching record, since the handler under test runs in its own
// goroutine.
func (r *serverLogRecorder) waitFor(t *testing.T, level slog.Level, substr string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.count(level, substr) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func captureServerLogs(t *testing.T) *serverLogRecorder {
	t.Helper()
	prev := slog.Default()
	r := &serverLogRecorder{}
	slog.SetDefault(slog.New(r))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return r
}
