package kubehub

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// These cover an API-server outage and its recovery: while writes are rejected the
// replica keeps resolving its own providers locally, but peers cannot see them — the
// asymmetry that used to be completely silent (writeCR swallowed every error and the
// Lease renewal was `_ = s.beat(...)`). The store must report it and heal on its own.

// apiOutage toggles a fake-client failure injection on and off.
type apiOutage struct{ on atomic.Bool }

func (o *apiOutage) start() { o.on.Store(true) }
func (o *apiOutage) stop()  { o.on.Store(false) }

// outageStores builds a KubeStore and a PEER KubeStore over the SAME fake clients
// (so the peer's resync sees exactly what the API server holds), plus the outage
// switch. Informers and the heartbeat goroutine are not started: tests drive tick()
// and resync() so every assertion is deterministic.
func outageStores(t *testing.T) (*KubeStore, *KubeStore, *apiOutage) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		hubEndpointGVR: "HubEndpointList",
	})
	cs := fake.NewSimpleClientset()
	o := &apiOutage{}
	fail := func(k8stesting.Action) (bool, runtime.Object, error) {
		if o.on.Load() {
			return true, nil, apierrors.NewServiceUnavailable("kube-apiserver is unavailable")
		}
		return false, nil, nil
	}
	dyn.PrependReactor("*", "*", fail)
	cs.PrependReactor("*", "*", fail)
	return newStore(dyn, cs, "default", "replica-A", "ws://a:5000"),
		newStore(dyn, cs, "default", "replica-B", "ws://b:5000"),
		o
}

// TestKubeRegisterReportsOutageAndRecovers is the core of the fix: a Register whose
// CR write fails must SAY so, must keep serving locally, and must become visible to
// peers once the API server recovers — without the spoke re-registering.
func TestKubeRegisterReportsOutageAndRecovers(t *testing.T) {
	s, peer, outage := outageStores(t)

	outage.start()
	err := s.Register("conn1", "web", "10.0.0.1:80", "")
	if err == nil {
		t.Fatal("Register must report the failed CR write, got nil error")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Fatalf("error should name the service, got %v", err)
	}
	if s.StoreHealth() == nil {
		t.Fatal("StoreHealth must report the degradation while the write is outstanding")
	}
	// The local partition stays authoritative — this is exactly the asymmetry the
	// error exists to announce: we still resolve it, our peers cannot.
	if _, ok := s.Lookup("web"); !ok {
		t.Fatal("the owning replica must keep resolving its own provider during the outage")
	}
	if _, err := getEndpoint(t, s, "replica-A", "conn1", "web"); err == nil {
		t.Fatal("no CR should exist while the API server is down")
	}

	outage.stop()
	s.tick()

	if err := s.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear after recovery: %v", err)
	}
	if _, err := getEndpoint(t, s, "replica-A", "conn1", "web"); err != nil {
		t.Fatalf("the CR should have been re-applied on recovery: %v", err)
	}
	peer.resync(context.Background())
	tgt, ok := peer.Lookup("web")
	if !ok {
		t.Fatal("peer replica should see the re-registered provider after recovery")
	}
	if tgt.Addr != "10.0.0.1:80" {
		t.Fatalf("peer resolved the wrong target: %+v", tgt)
	}
}

// TestKubeRemoveConnReportsOutageAndRecovers covers the withdrawal: a delete that
// fails while this replica keeps renewing its Lease leaves peers routing to a spoke
// that is gone, so it must be reported and retried rather than dropped.
func TestKubeRemoveConnReportsOutageAndRecovers(t *testing.T) {
	s, peer, outage := outageStores(t)

	if err := s.Register("conn1", "web", "10.0.0.1:80", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	s.tick() // renew the Lease so the peer considers replica-A live
	peer.resync(context.Background())
	if _, ok := peer.Lookup("web"); !ok {
		t.Fatal("peer should see the provider before the outage")
	}

	outage.start()
	if err := s.RemoveConn("conn1"); err == nil {
		t.Fatal("RemoveConn must report the failed withdrawal, got nil error")
	}
	if s.StoreHealth() == nil {
		t.Fatal("StoreHealth must report the outstanding withdrawal")
	}

	outage.stop()
	s.tick()

	if err := s.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear after recovery: %v", err)
	}
	if _, err := getEndpoint(t, s, "replica-A", "conn1", "web"); err == nil {
		t.Fatal("the CR should have been deleted on recovery")
	}
	peer.resync(context.Background())
	if _, ok := peer.Lookup("web"); ok {
		t.Fatal("peer should no longer see the provider after the withdrawal is re-applied")
	}
}

// TestKubeLeaseRenewalFailureLoggedOnce is the delayed-symptom path: a Lease that
// stops being renewed makes every provider this replica owns vanish from its peers a
// leaseDuration later, far from the cause. It must log at ERROR when it starts
// failing, once (not per tick), and log again on recovery.
func TestKubeLeaseRenewalFailureLoggedOnce(t *testing.T) {
	logs := captureLogs(t)
	s, _, outage := outageStores(t)

	outage.start()
	s.tick()
	s.tick()

	if err := s.StoreHealth(); err == nil {
		t.Fatal("StoreHealth must report a failing Lease renewal")
	} else if !strings.Contains(err.Error(), "Lease") {
		t.Fatalf("StoreHealth should explain the Lease failure, got %v", err)
	}
	if n := logs.count(slog.LevelError, "Lease renewal failed"); n != 1 {
		t.Fatalf("Lease renewal failure should be logged once per outage (transition), got %d", n)
	}

	outage.stop()
	s.tick()

	if err := s.StoreHealth(); err != nil {
		t.Fatalf("StoreHealth should be clear after the Lease renewal recovers: %v", err)
	}
	if n := logs.count(slog.LevelInfo, "Lease renewal recovered"); n != 1 {
		t.Fatalf("Lease renewal recovery should be logged once, got %d", n)
	}
}

// TestKubeHealthyStoreReportsNoError guards the negative: the health signal must be
// quiet in the healthy case, or it is useless as an alert.
func TestKubeHealthyStoreReportsNoError(t *testing.T) {
	s, _, _ := outageStores(t)
	if err := s.Register("conn1", "web", "10.0.0.1:80", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.RemoveConn("conn1"); err != nil {
		t.Fatalf("RemoveConn: %v", err)
	}
	s.tick()
	if err := s.StoreHealth(); err != nil {
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
