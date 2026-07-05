package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/config"
	"cornus/pkg/storage"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestGCIntervalFromEnv covers the CORNUS_GC_INTERVAL knob directly: unset/empty
// disables (zero, no error), a valid Go duration parses, and malformed or
// non-positive values are hard errors (fail closed).
func TestGCIntervalFromEnv(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	if d, err := gcIntervalFromEnv(); err != nil || d != 0 {
		t.Fatalf("unset env: got (%v, %v), want (0, nil)", d, err)
	}

	t.Setenv("CORNUS_GC_INTERVAL", "90m")
	if d, err := gcIntervalFromEnv(); err != nil || d != 90*time.Minute {
		t.Fatalf("90m: got (%v, %v), want (90m, nil)", d, err)
	}

	for _, bad := range []string{"banana", "1x", "-5m", "0"} {
		t.Setenv("CORNUS_GC_INTERVAL", bad)
		if _, err := gcIntervalFromEnv(); err == nil {
			t.Fatalf("CORNUS_GC_INTERVAL=%q should be a hard error", bad)
		}
	}
}

// TestNewRejectsMalformedGCInterval proves a malformed CORNUS_GC_INTERVAL is a
// hard startup error at New, matching the fail-closed policy-env precedent.
func TestNewRejectsMalformedGCInterval(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "not-a-duration")
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(config.Config{DataDir: dir}, st); err == nil {
		t.Fatal("New with malformed CORNUS_GC_INTERVAL should fail, got nil error")
	}
}

// TestPeriodicGCDisabledWhenUnset proves the zero-cost-when-off requirement:
// with CORNUS_GC_INTERVAL unset, New leaves the interval zero and
// startPeriodicGC starts nothing (no ticker, no supervised child).
func TestPeriodicGCDisabledWhenUnset(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	s := newTestServerObj(t)
	if s.gcInterval != 0 {
		t.Fatalf("gcInterval = %v, want 0 when env unset", s.gcInterval)
	}
	s.startPeriodicGC()
	if s.gcTicker != nil {
		t.Fatal("startPeriodicGC started a loop despite the feature being off")
	}
	// Shutdown must still be safe with no loop running.
	s.closeResources()
}

// TestPeriodicGCRunsAndStopsOnClose starts the scheduler with a tiny interval,
// asserts the GC core runs at least once, then proves closeResources stops the
// loop: no further run happens after it returns.
func TestPeriodicGCRunsAndStopsOnClose(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	s := newTestServerObj(t)

	var runs atomic.Int64
	s.gcRun = func(context.Context) (gcResponse, error) {
		runs.Add(1)
		if runs.Load()%2 == 0 {
			return gcResponse{}, errors.New("synthetic failure") // errors must not stop the loop
		}
		return gcResponse{}, nil
	}
	s.gcInterval = 2 * time.Millisecond
	s.startPeriodicGC()

	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() < 3 { // >1 run also proves an error does not kill the loop
		if time.Now().After(deadline) {
			t.Fatalf("periodic GC ran %d times, want >= 3", runs.Load())
		}
		time.Sleep(time.Millisecond)
	}

	s.closeResources() // waits for the loop to exit
	after := runs.Load()
	time.Sleep(20 * time.Millisecond) // several would-be ticks
	if got := runs.Load(); got != after {
		t.Fatalf("GC ran after Close: %d -> %d", after, got)
	}
	// Idempotent: a second shutdown is a no-op.
	s.closeResources()
}

// TestPeriodicGCSupervisedAcrossPanic proves the GC loop is a supervised child
// (pkg/supervisor) of s.sup: a panic in one run is recovered and the loop is
// relaunched (Restart, capped backoff) rather than crashing the test process,
// AND gcRunning is correctly reset across that panic (runGCTick's defer) so
// the relaunched loop does not permanently think a run is still in flight.
func TestPeriodicGCSupervisedAcrossPanic(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	s := newTestServerObj(t)

	var calls atomic.Int64
	s.gcRun = func(context.Context) (gcResponse, error) {
		n := calls.Add(1)
		if n == 1 {
			panic("synthetic panic on the first tick")
		}
		return gcResponse{}, nil
	}
	s.gcInterval = 2 * time.Millisecond
	s.startPeriodicGC()
	defer s.closeResources()

	// A second (post-panic) run proves the supervisor recovered the panic,
	// restarted the loop, and gcRunning did not get stuck at true.
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("GC ran %d times after a panic, want >= 2 (supervisor should have restarted the loop)", calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if s.gcRunning.Load() {
		t.Error("gcRunning left true after the panicking run — a future tick would wrongly skip as \"already running\"")
	}
}

// TestPeriodicGCSkipsOverlappingTick drives the loop with an injected tick
// channel and proves a tick is skipped (no core invocation) while a previous
// run is still marked in flight, then processed again once it clears.
func TestPeriodicGCSkipsOverlappingTick(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	s := newTestServerObj(t)

	ran := make(chan struct{}, 8)
	s.gcRun = func(context.Context) (gcResponse, error) {
		ran <- struct{}{}
		return gcResponse{}, nil
	}

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.periodicGCLoop(ctx, ticks)
	}()

	// A run marked in flight: the next tick must be skipped.
	s.gcRunning.Store(true)
	ticks <- time.Now()
	select {
	case <-ran:
		t.Fatal("tick was not skipped while a run was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	// Cleared: the next tick runs.
	s.gcRunning.Store(false)
	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("tick after clearing the guard did not run")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit on stop")
	}
}

// TestPeriodicGCRenewsLeaseDuringRun proves the lease is renewed for the whole
// duration of a sweep, not just at the tick instant: while a single run is
// blocked (a sweep longer than the lease window), the gate must be called
// repeatedly so the lease cannot lapse and let a second replica sweep
// concurrently.
func TestPeriodicGCRenewsLeaseDuringRun(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	t.Setenv("CORNUS_GC_LEASE", "")
	s := newTestServerObj(t)
	s.gcInterval = 5 * time.Millisecond // renewal cadence during the run

	var gateCalls atomic.Int64
	s.gcGate = func(context.Context) (bool, error) {
		gateCalls.Add(1)
		return true, nil
	}
	started := make(chan struct{})
	release := make(chan struct{})
	s.gcRun = func(context.Context) (gcResponse, error) {
		close(started)
		<-release // hold the sweep open so the renewal ticker fires during it
		return gcResponse{}, nil
	}

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.periodicGCLoop(ctx, ticks)
	}()

	ticks <- time.Now() // one tick -> one (blocked) run
	<-started

	// The tick itself calls the gate once; renewals during the blocked run must
	// add more. Wait for >= 3 to prove renewal is happening mid-sweep.
	deadline := time.Now().Add(2 * time.Second)
	for gateCalls.Load() < 3 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("gate called %d times during one run; want >= 3 (lease not renewed mid-sweep)", gateCalls.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit on stop")
	}
}

// TestParseGCLease covers the CORNUS_GC_LEASE value grammar: "kube" with the
// default lease name, explicit "kube:<name>" and "kube:<namespace>/<name>"
// overrides, and every malformed shape as a hard error.
func TestParseGCLease(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		ns, nm  string
		wantErr bool
	}{
		{raw: "kube", ns: "", nm: "cornus-gc"},
		{raw: "kube:my-lease", ns: "", nm: "my-lease"},
		{raw: "kube:infra/cornus-gc-prod", ns: "infra", nm: "cornus-gc-prod"},
		{raw: "banana", wantErr: true},
		{raw: "kubernetes", wantErr: true},
		{raw: "kube:", wantErr: true},
		{raw: "kube:/name", wantErr: true},
		{raw: "kube:ns/", wantErr: true},
		{raw: "kube:a/b/c", wantErr: true},
	} {
		ns, nm, err := parseGCLease(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseGCLease(%q): want error, got (%q, %q)", tc.raw, ns, nm)
			}
			continue
		}
		if err != nil || ns != tc.ns || nm != tc.nm {
			t.Errorf("parseGCLease(%q) = (%q, %q, %v), want (%q, %q, nil)", tc.raw, ns, nm, err, tc.ns, tc.nm)
		}
	}
}

// TestGCLeaseGateFromEnv covers the env-level contract: unset is a strict
// no-op (nil gate, nil error — zero cost), a malformed value is a hard error,
// and a lease configured without CORNUS_GC_INTERVAL is a hard error (the lease
// only gates the scheduler, so that combination is a misconfiguration).
func TestGCLeaseGateFromEnv(t *testing.T) {
	t.Setenv("CORNUS_GC_LEASE", "")
	if gate, err := gcLeaseGateFromEnv(time.Hour); gate != nil || err != nil {
		t.Fatalf("unset env: got (gate non-nil=%v, %v), want (nil, nil)", gate != nil, err)
	}
	if gate, err := gcLeaseGateFromEnv(0); gate != nil || err != nil {
		t.Fatalf("unset env, no interval: got (gate non-nil=%v, %v), want (nil, nil)", gate != nil, err)
	}

	t.Setenv("CORNUS_GC_LEASE", "not-a-scheme")
	if _, err := gcLeaseGateFromEnv(time.Hour); err == nil {
		t.Fatal("malformed CORNUS_GC_LEASE should be a hard error")
	}

	t.Setenv("CORNUS_GC_LEASE", "kube")
	if _, err := gcLeaseGateFromEnv(0); err == nil {
		t.Fatal("CORNUS_GC_LEASE without CORNUS_GC_INTERVAL should be a hard error")
	}
}

// TestNewRejectsMalformedGCLease proves the fail-closed contract at New: a
// malformed CORNUS_GC_LEASE, or one set without CORNUS_GC_INTERVAL, is a hard
// startup error (the same precedent as the policy envs and the interval).
func TestNewRejectsMalformedGCLease(t *testing.T) {
	newServer := func(t *testing.T) error {
		dir := t.TempDir()
		st, err := storage.Open(context.Background(), dir, dir+"/uploads")
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(config.Config{DataDir: dir}, st)
		return err
	}

	t.Run("malformed value", func(t *testing.T) {
		t.Setenv("CORNUS_GC_INTERVAL", "1h")
		t.Setenv("CORNUS_GC_LEASE", "banana")
		if err := newServer(t); err == nil {
			t.Fatal("New with malformed CORNUS_GC_LEASE should fail, got nil error")
		}
	})
	t.Run("lease without interval", func(t *testing.T) {
		t.Setenv("CORNUS_GC_INTERVAL", "")
		t.Setenv("CORNUS_GC_LEASE", "kube")
		if err := newServer(t); err == nil {
			t.Fatal("New with CORNUS_GC_LEASE but no CORNUS_GC_INTERVAL should fail, got nil error")
		}
	})
}

// gcLeaseSpec reads back the fake-clientset Lease for assertions.
func gcLeaseSpec(t *testing.T, cs *fake.Clientset, namespace, name string) coordinationv1.LeaseSpec {
	t.Helper()
	l, err := cs.CoordinationV1().Leases(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	return l.Spec
}

// TestGCLeaseDurationClamp checks the interval→lease-duration derivation: 2x
// the interval, floored at gcLeaseMinDuration and capped at gcLeaseMaxDuration.
func TestGCLeaseDurationClamp(t *testing.T) {
	cs := fake.NewSimpleClientset()
	for _, tc := range []struct {
		interval, want time.Duration
	}{
		{time.Second, gcLeaseMinDuration},
		{10 * time.Minute, 20 * time.Minute},
		{24 * time.Hour, gcLeaseMaxDuration},
	} {
		if g := newGCLeaseGate(cs, "default", "cornus-gc", "id", tc.interval); g.duration != tc.want {
			t.Errorf("interval %v: lease duration = %v, want %v", tc.interval, g.duration, tc.want)
		}
	}
}

// TestGCLeaseAcquireRefuseTakeover is the core election contract against the
// fake clientset: the first claimant acquires; a second identity is refused
// while the lease is fresh; the holder renews; once the window lapses the
// second identity takes the lease over and the old holder is then refused.
func TestGCLeaseAcquireRefuseTakeover(t *testing.T) {
	cs := fake.NewSimpleClientset()
	interval := 10 * time.Minute // lease duration 20m
	a := newGCLeaseGate(cs, "default", "cornus-gc", "replica-a", interval)
	b := newGCLeaseGate(cs, "default", "cornus-gc", "replica-b", interval)
	clock := time.Now()
	a.now = func() time.Time { return clock }
	b.now = func() time.Time { return clock }
	ctx := context.Background()

	// First claimant creates the Lease and holds it.
	if held, err := a.tryAcquire(ctx); err != nil || !held {
		t.Fatalf("a first acquire = (%v, %v), want (true, nil)", held, err)
	}
	if spec := gcLeaseSpec(t, cs, "default", "cornus-gc"); *spec.HolderIdentity != "replica-a" {
		t.Fatalf("holder = %q, want replica-a", *spec.HolderIdentity)
	}

	// A different identity is refused while the lease is unexpired.
	if held, err := b.tryAcquire(ctx); err != nil || held {
		t.Fatalf("b acquire against fresh lease = (%v, %v), want (false, nil)", held, err)
	}

	// The holder renews on its next tick (RenewTime moves forward).
	clock = clock.Add(interval)
	if held, err := a.tryAcquire(ctx); err != nil || !held {
		t.Fatalf("a renew = (%v, %v), want (true, nil)", held, err)
	}
	renewedAt := gcLeaseSpec(t, cs, "default", "cornus-gc").RenewTime.Time
	if !renewedAt.Equal(clock) {
		t.Fatalf("renewTime = %v, want %v", renewedAt, clock)
	}
	// Still refused right after the renewal.
	if held, err := b.tryAcquire(ctx); err != nil || held {
		t.Fatalf("b acquire after renewal = (%v, %v), want (false, nil)", held, err)
	}

	// The holder goes away; past the lease window the other replica takes over.
	clock = clock.Add(20*time.Minute + time.Second)
	if held, err := b.tryAcquire(ctx); err != nil || !held {
		t.Fatalf("b takeover of expired lease = (%v, %v), want (true, nil)", held, err)
	}
	if spec := gcLeaseSpec(t, cs, "default", "cornus-gc"); *spec.HolderIdentity != "replica-b" {
		t.Fatalf("holder after takeover = %q, want replica-b", *spec.HolderIdentity)
	}
	// The old holder is now the refused one.
	if held, err := a.tryAcquire(ctx); err != nil || held {
		t.Fatalf("a acquire after losing lease = (%v, %v), want (false, nil)", held, err)
	}
}

// TestGCLeaseLostRaceIsNotAnError proves the two optimistic-concurrency
// outcomes are clean refusals, not errors: an AlreadyExists on create (another
// replica created the Lease between our Get and Create) and a Conflict on
// update (another replica renewed between our Get and Update).
func TestGCLeaseLostRaceIsNotAnError(t *testing.T) {
	gr := schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}

	t.Run("create already exists", func(t *testing.T) {
		cs := fake.NewSimpleClientset()
		cs.PrependReactor("create", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewAlreadyExists(gr, "cornus-gc")
		})
		g := newGCLeaseGate(cs, "default", "cornus-gc", "replica-a", time.Hour)
		if held, err := g.tryAcquire(context.Background()); err != nil || held {
			t.Fatalf("lost create race = (%v, %v), want (false, nil)", held, err)
		}
	})

	t.Run("update conflict", func(t *testing.T) {
		holder := "replica-a"
		dur := int32(60)
		stale := metav1.NewMicroTime(time.Now().Add(-time.Hour)) // expired: claimable
		cs := fake.NewSimpleClientset(&coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: "cornus-gc", Namespace: "default"},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &dur,
				RenewTime:            &stale,
			},
		})
		cs.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(gr, "cornus-gc", errors.New("stale resourceVersion"))
		})
		g := newGCLeaseGate(cs, "default", "cornus-gc", "replica-b", time.Hour)
		if held, err := g.tryAcquire(context.Background()); err != nil || held {
			t.Fatalf("lost update race = (%v, %v), want (false, nil)", held, err)
		}
	})

	t.Run("other error propagates", func(t *testing.T) {
		cs := fake.NewSimpleClientset()
		cs.PrependReactor("get", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("apiserver unreachable")
		})
		g := newGCLeaseGate(cs, "default", "cornus-gc", "replica-a", time.Hour)
		if held, err := g.tryAcquire(context.Background()); err == nil || held {
			t.Fatalf("apiserver error = (%v, %v), want (false, non-nil)", held, err)
		}
	})
}

// TestPeriodicGCLeaseGatesTicks drives periodicGCLoop with an injected tick
// channel and a stubbed gate: a non-holder tick skips the GC core, a gate
// error skips it too (fail closed), and a holder tick runs it.
func TestPeriodicGCLeaseGatesTicks(t *testing.T) {
	t.Setenv("CORNUS_GC_INTERVAL", "")
	t.Setenv("CORNUS_GC_LEASE", "")
	s := newTestServerObj(t)

	ran := make(chan struct{}, 8)
	s.gcRun = func(context.Context) (gcResponse, error) {
		ran <- struct{}{}
		return gcResponse{}, nil
	}
	var held atomic.Bool
	var gateErr atomic.Bool
	s.gcGate = func(context.Context) (bool, error) {
		if gateErr.Load() {
			return false, errors.New("synthetic lease failure")
		}
		return held.Load(), nil
	}

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.periodicGCLoop(ctx, ticks)
	}()

	// Non-holder: the tick is skipped.
	ticks <- time.Now()
	select {
	case <-ran:
		t.Fatal("GC ran on a tick where the lease was held by another replica")
	case <-time.After(50 * time.Millisecond):
	}

	// Gate error: skipped too (fail closed).
	gateErr.Store(true)
	ticks <- time.Now()
	select {
	case <-ran:
		t.Fatal("GC ran on a tick where the lease check failed")
	case <-time.After(50 * time.Millisecond):
	}
	gateErr.Store(false)

	// Holder: the tick runs.
	held.Store(true)
	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("GC did not run on a tick where this replica held the lease")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit on stop")
	}
}
