package ingressmux

import (
	"net/http"
	"testing"

	"cornus/pkg/ingressroute"
)

// TestProxyUsesTheBoundedTransport is the ingressmux half of the same guard the
// emulation side carries (pkg/ingressemu). Both proxies front workloads through a
// port-forward bridge, both must build their transport through
// ingressroute.BridgeTransport, and the helper's own test proves only that the
// helper is correct — not that either caller still calls it.
//
// This is the server-side proxy, so the resource an unbounded pool leaks is the
// server's: one Kubernetes SPDY stream / Docker hijack / containerd exec per idle
// connection, held open forever, one per workload port.
//
// Expected values are read from BridgeTransport rather than restated, so this test
// tracks a re-tuning of the shared bounds instead of pinning today's numbers.
func TestProxyUsesTheBoundedTransport(t *testing.T) {
	want := ingressroute.BridgeTransport(nil)

	p := NewProxy(NewTable(), nil)
	got, ok := p.proxy.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the mux proxy's Transport is %T, not *http.Transport; it is no longer built through "+
			"ingressroute.BridgeTransport", p.proxy.Transport)
	}
	if got.IdleConnTimeout != want.IdleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", got.IdleConnTimeout, want.IdleConnTimeout)
	}
	if got.MaxIdleConns != want.MaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d (0 means unlimited)", got.MaxIdleConns, want.MaxIdleConns)
	}
	if got.MaxIdleConnsPerHost != want.MaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", got.MaxIdleConnsPerHost, want.MaxIdleConnsPerHost)
	}
	if got.DialContext == nil {
		t.Error("DialContext is nil, so the proxy cannot reach the workload it routes to")
	}
}
