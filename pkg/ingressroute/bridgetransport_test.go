package ingressroute

import (
	"context"
	"net"
	"testing"
)

// TestBridgeTransportIsBounded pins that a bridge transport never comes back
// with the zero-value pool, which means "never expire" and "unlimited".
//
// The regression this guards: pkg/ingressmux bounded its transport and
// pkg/ingressemu — the same reverse proxy on the client side — did not, so every
// idle connection on the emulated path held a port-forward bridge (a goroutine
// plus a Kubernetes SPDY stream, Docker hijack, or containerd exec) open forever,
// accumulating one per workload port. Both now build their transport here.
func TestBridgeTransportIsBounded(t *testing.T) {
	dialed := false
	tr := BridgeTransport(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = true
		return nil, context.Canceled
	})

	if tr.DialContext == nil {
		t.Fatal("DialContext is nil: the transport would dial the synthetic upstream authority directly")
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "workload:8080"); err == nil || !dialed {
		t.Error("DialContext did not delegate to the supplied dialer")
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout = 0: an idle port-forward bridge would never be reclaimed")
	}
	if tr.MaxIdleConns == 0 {
		t.Error("MaxIdleConns = 0 (unlimited): bridges accumulate without bound")
	}
	if tr.MaxIdleConnsPerHost == 0 {
		t.Error("MaxIdleConnsPerHost = 0: one workload port could hold open unlimited bridges")
	}
}
