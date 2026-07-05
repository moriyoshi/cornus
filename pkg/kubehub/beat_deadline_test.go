package kubehub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// hangingAPIServer answers nothing: it accepts the request and holds it until the
// test ends. That is the failure the deadline exists for, and it is NOT what the
// existing outage tests cover — a fake clientset reactor returning
// ServiceUnavailable models a server that REFUSES, which returns promptly and needs
// no deadline at all. A server that accepts and never answers is the one that hangs
// a caller forever, and it is the ordinary shape of an overloaded or partitioned
// kube-apiserver.
func hangingAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	return srv
}

// TestBeatAbandonsAHangingAPIServer pins the deadline on KubeStore.beat.
//
// The audit found that removing beat's context.WithTimeout left every existing
// test green, and the reason is instructive: the outage tests use a fake clientset
// whose reactor returns an error immediately, so they exercise a server that says
// no, never one that says nothing. Nothing in the suite could tell a bounded call
// from an unbounded one.
//
// What an unbounded beat costs: the heartbeat goroutine blocks in the Lease
// renewal, so beatBounded never retries and noteBeat is never called — no ERROR is
// logged and StoreHealth keeps reporting the last good result. leaseDuration later
// the Lease goes stale and every provider this replica owns disappears from every
// peer. There is no event anywhere connecting the disappearance to the stall; the
// replica looks healthy and its services simply stop resolving elsewhere.
//
// A real client-go clientset over an httptest server is used rather than the fake,
// because the property under test is that the CONTEXT cancels an in-flight request
// — and the fake clientset's reactors never see a context, so with the fake the
// deadline would be untestable in either direction.
func TestBeatAbandonsAHangingAPIServer(t *testing.T) {
	srv := hangingAPIServer(t)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}

	// Shrink the deadline so the test spends milliseconds, not seconds, reaching it.
	prev := writeTimeout
	writeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { writeTimeout = prev })

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{hubEndpointGVR: "HubEndpointList"})
	s := newStore(dyn, cs, "default", "replica-A", "ws://a:5000")

	// The bound is enforced by this test rather than by beat returning, so that
	// REMOVING the deadline fails in seconds instead of hanging the suite until the
	// go test timeout — a neutralization that wedges the run is not a useful one.
	type result struct {
		err error
		dur time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		err := s.beat(context.Background())
		done <- result{err, time.Since(start)}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("beat succeeded against a server that never answered, in %v", got.dur)
		}
		if !errors.Is(got.err, context.DeadlineExceeded) {
			t.Errorf("beat failed with %v, want a deadline: any other error means it stopped for some "+
				"reason other than the bound this test guards", got.err)
		}
		// Generously above writeTimeout: the assertion is "bounded", not "precise".
		if got.dur > 3*time.Second {
			t.Errorf("beat took %v to give up on a hung API server, far beyond writeTimeout=%v",
				got.dur, writeTimeout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("beat never returned against an API server that accepts requests and never answers. " +
			"Its context.WithTimeout is gone, so the heartbeat goroutine blocks here forever: " +
			"beatBounded never retries, noteBeat never records a failure, no ERROR is logged, and " +
			"leaseDuration later every provider this replica owns vanishes from every peer with " +
			"nothing to explain it.")
	}
}

// TestBeatBoundedStopsAtTheAttemptLimit pins the OTHER half of the bound. beat's
// deadline caps one attempt; beatAttempts caps how many. Both are needed for the
// invariant the constants document — beatAttempts*writeTimeout + backoffSum <
// leaseDuration — and a retry loop that ignored its ctx would satisfy the
// per-attempt deadline while still outliving the Lease it renews.
func TestBeatBoundedStopsAtTheAttemptLimit(t *testing.T) {
	srv := hangingAPIServer(t)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	prev := writeTimeout
	writeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { writeTimeout = prev })

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{hubEndpointGVR: "HubEndpointList"})
	s := newStore(dyn, cs, "default", "replica-A", "ws://a:5000")

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = s.beatBounded(context.Background())
		done <- time.Since(start)
	}()

	// Ceiling from the documented invariant, with room for scheduling: the whole
	// retry sequence must fit inside one liveness window.
	select {
	case dur := <-done:
		if dur >= leaseDuration {
			t.Errorf("beatBounded took %v against a hung API server, which is at or beyond leaseDuration "+
				"(%v): one exhausted heartbeat tick can outlive the very Lease it was renewing",
				dur, leaseDuration)
		}
	case <-time.After(leaseDuration):
		t.Fatalf("beatBounded did not finish within leaseDuration (%v) against a hung API server", leaseDuration)
	}
}
