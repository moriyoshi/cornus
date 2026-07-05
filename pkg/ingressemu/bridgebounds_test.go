package ingressemu

import (
	"net/http"
	"testing"

	"cornus/pkg/ingressroute"
)

// TestBridgeProxyUsesTheBoundedTransport guards this CALLER's adoption of
// ingressroute.BridgeTransport.
//
// ingressroute's own TestBridgeTransportIsBounded proves the shared constructor
// returns bounded values. It says nothing about whether anyone calls it — and the
// defect being guarded is precisely a caller reverting to its own
// `&http.Transport{DialContext: ...}`, which is what this package had before the
// extraction and which left every test green.
//
// The bound matters because an idle connection here is not cheap: each one holds
// a live port-forward bridge open — a goroutine plus a real backend resource (a
// Kubernetes SPDY stream, a Docker hijack, a containerd exec). http.Transport's
// zero values mean "never expire" and "unlimited", so an inline transport
// accumulates one per workload port and never reclaims any of them. Nothing
// errors; the process just holds more and more open until something else breaks.
//
// The expected values come from calling BridgeTransport rather than being written
// down here, so re-tuning the shared bounds does not require editing this test and
// cannot be satisfied by a caller that merely happens to match today's numbers.
func TestBridgeProxyUsesTheBoundedTransport(t *testing.T) {
	want := ingressroute.BridgeTransport(nil)

	proxy := newBridgeProxy(nil, "web", 8080)
	got, ok := proxy.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the emulated-ingress proxy's Transport is %T, not *http.Transport; it is no longer built "+
			"through ingressroute.BridgeTransport", proxy.Transport)
	}
	if got.IdleConnTimeout != want.IdleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v: an idle bridge that never expires holds a port-forward "+
			"goroutine and a backend stream open indefinitely", got.IdleConnTimeout, want.IdleConnTimeout)
	}
	if got.MaxIdleConns != want.MaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d (0 means unlimited)", got.MaxIdleConns, want.MaxIdleConns)
	}
	if got.MaxIdleConnsPerHost != want.MaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d (0 means net/http's default of 2 per host, which is "+
			"not the bound this transport is meant to carry)", got.MaxIdleConnsPerHost, want.MaxIdleConnsPerHost)
	}
	if got.DialContext == nil {
		t.Error("DialContext is nil, so the proxy would dial the synthetic upstream authority directly " +
			"instead of the workload's port-forward")
	}
}
