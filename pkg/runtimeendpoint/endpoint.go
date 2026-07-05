// Package runtimeendpoint resolves the address of a container runtime's REST
// API and builds the transports that reach it.
//
// It exists because the same DOCKER_HOST parsing had been written three times —
// pkg/deploy/dockerhost's engine client, pkg/registry's daemon re-export source,
// and pkg/build/builderctr's delegated-builder client — each with its own
// spelling of the same switch, its own default, and its own error message. Two
// of the three carried a comment saying they were mirroring the first, which is
// the shape a fact takes just before the copies stop agreeing.
//
// Scope is deliberately narrow: endpoint RESOLUTION and DIALERS, nothing else.
// The three HTTP clients above stay separate — they need genuinely different
// things (streaming timeouts, OpenTelemetry instrumentation, hijack support),
// and merging them would trade a duplicated switch for a shared type with three
// sets of conditionals in it.
package runtimeendpoint

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Endpoint is a resolved runtime API address.
//
// The zero value is not usable; build one with Parse.
type Endpoint struct {
	// network is "unix" or "tcp", as net.Dial spells it.
	network string
	// address is the unix socket path or the tcp host:port.
	address string
	// baseURL is the scheme+authority to put in request URLs. For a unix socket
	// it is a fixed placeholder that the dialer ignores — the URL still has to
	// parse, and the host in it never reaches DNS.
	baseURL string
	// hostHeader is what a hand-rolled request writes as `Host:`.
	hostHeader string
	// custom, when set, replaces net.Dial entirely (see FromDialer).
	custom func(ctx context.Context) (net.Conn, error)
	// remote is the caller's assertion for a custom dialer; the network-derived
	// answer is used otherwise.
	remote bool
}

// Parse resolves raw (a DOCKER_HOST / CONTAINER_HOST-style value) into an
// Endpoint, falling back to def when raw is empty.
//
// Only unix:// and tcp:// are accepted. An unrecognized scheme is an error
// rather than a silent fallback to the default: a typo that quietly redirected
// cornus to some other daemon would be far worse than a refusal to start, and
// the caller has no way to notice the substitution.
func Parse(raw, def string) (Endpoint, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		v = def
	}
	switch {
	case strings.HasPrefix(v, "unix://"):
		sock := strings.TrimPrefix(v, "unix://")
		if sock == "" {
			return Endpoint{}, fmt.Errorf("endpoint %q names no socket path", v)
		}
		return Endpoint{
			network:    "unix",
			address:    sock,
			baseURL:    "http://docker",
			hostHeader: "docker",
		}, nil
	case strings.HasPrefix(v, "tcp://"):
		addr := strings.TrimPrefix(v, "tcp://")
		if addr == "" {
			return Endpoint{}, fmt.Errorf("endpoint %q names no address", v)
		}
		return Endpoint{
			network:    "tcp",
			address:    addr,
			baseURL:    "http://" + addr,
			hostHeader: addr,
		}, nil
	default:
		return Endpoint{}, fmt.Errorf("unsupported endpoint %q (want unix:// or tcp://)", v)
	}
}

// BaseURL is the scheme+authority to prefix request paths with.
func (e Endpoint) BaseURL() string { return e.baseURL }

// HostHeader is the `Host:` value for a hand-rolled request.
func (e Endpoint) HostHeader() string { return e.hostHeader }

// NonLocal reports that the endpoint MAY name a runtime on another machine, so
// a workload's container IP need not be routable from this process.
//
// It does not prove remoteness — tcp://127.0.0.1:2375 is local — and must not be
// used to decide behaviour. Its only job is to let an error that has already
// happened say why it might have happened.
func (e Endpoint) NonLocal() bool { return e.network == "tcp" || e.remote }

// UnixSocketPath is the socket path for a unix endpoint, or "" for any other
// kind. Callers that must hand the socket to something else as a FILE — a bind
// mount into a container, say — use this to detect the case they cannot serve.
func (e Endpoint) UnixSocketPath() string {
	if e.network != "unix" {
		return ""
	}
	return e.address
}

// Dial opens a raw connection to the runtime. Used both as an http.Transport
// DialContext (which supplies network/addr arguments this ignores, since the
// endpoint already fixes them) and directly for hijacked bidirectional streams.
func (e Endpoint) Dial(ctx context.Context) (net.Conn, error) {
	if e.custom != nil {
		return e.custom(ctx)
	}
	return (&net.Dialer{}).DialContext(ctx, e.network, e.address)
}

// DialContext adapts Dial to http.Transport's signature.
func (e Endpoint) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return e.Dial(ctx)
}

// Transport returns the RoundTripper for this endpoint, or **nil** meaning
// http.DefaultTransport.
//
// The nil is deliberate and load-bearing. A unix endpoint needs a custom dialer,
// so it gets a bare Transport. A tcp endpoint does not — its address is already
// in the request URL — and returning a bare Transport for it would silently drop
// everything DefaultTransport brings (connection pooling limits, idle timeouts,
// HTTP/2 negotiation, proxy handling). http.Client treats a nil Transport as
// DefaultTransport, so `&http.Client{Transport: ep.Transport()}` is correct for
// both without the caller branching.
func (e Endpoint) Transport() http.RoundTripper {
	if e.network != "unix" && e.custom == nil {
		return nil
	}
	return &http.Transport{DialContext: e.DialContext}
}

// FromDialer builds an Endpoint around a caller-supplied dialer.
//
// It exists so a transport this package must not know about can still produce an
// Endpoint. The ssh:// case is the motivating one: reaching a runtime socket on
// another machine needs an SSH client, and pkg/runtimeendpoint sits below
// pkg/sshclient in the import graph — having it dial SSH itself would invert
// that. The caller builds the connection and hands the result in.
//
// nonLocal is the caller's assertion, not an inference: a dialer's transport is
// opaque here, so only the caller knows whether the runtime's container IPs
// could be routable from this process.
//
// The dialer's LIFECYCLE stays with the caller. An Endpoint is a value passed
// around freely and has no Close; whatever the dialer holds open is closed by
// whoever created it.
func FromDialer(dial func(ctx context.Context) (net.Conn, error), baseURL, hostHeader string, nonLocal bool) Endpoint {
	return Endpoint{
		network:    "custom",
		baseURL:    baseURL,
		hostHeader: hostHeader,
		custom:     dial,
		remote:     nonLocal,
	}
}
