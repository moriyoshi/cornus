package ingressroute

import (
	"context"
	"net"
	"net/http"
	"time"
)

// IdleBridgeTimeout is how long an idle pooled connection — and therefore the
// port-forward bridge behind it — is kept before being reclaimed. It matches
// http.DefaultTransport's IdleConnTimeout.
const IdleBridgeTimeout = 90 * time.Second

// Idle-connection bounds for a bridge transport, matching http.DefaultTransport.
const (
	maxIdleBridges        = 64
	maxIdleBridgesPerHost = 4
)

// BridgeTransport builds the http.Transport a workload-fronting reverse proxy
// must use: dial goes through the caller's port-forward, and the idle-connection
// pool is BOUNDED.
//
// The bounds are the whole point of this constructor. An idle connection here is
// not the cheap thing it is for an ordinary HTTP client: every pooled connection
// holds a live port-forward bridge open — a goroutine plus a real backend
// resource (a Kubernetes SPDY stream, a Docker hijack, a containerd exec). The
// zero values mean "never expire" and "unlimited", so an unbounded transport
// accumulates one bridge per workload port and never reclaims any of them.
//
// It lives here, in the leaf package both proxies already depend on, because
// they are near-identical code that drifted: the server-side mux (pkg/ingressmux)
// set these three fields and the client-side emulation (pkg/ingressemu) did not,
// so the same leak was fixed on one path and left on the other. The proxies
// genuinely differ in how they resolve a target and word their errors — those
// stay theirs — but there is no version of "how long may a bridge idle" that
// should be answered twice.
func BridgeTransport(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		DialContext:         dial,
		IdleConnTimeout:     IdleBridgeTimeout,
		MaxIdleConns:        maxIdleBridges,
		MaxIdleConnsPerHost: maxIdleBridgesPerHost,
	}
}
