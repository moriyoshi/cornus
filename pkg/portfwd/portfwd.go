// Package portfwd binds local listeners for a deployment's published ports
// and tunnels traffic to the workload through a cornus server's port-forward
// endpoint. It is the shared client-side engine behind `cornus port-forward`
// and the automatic forwarding of DeploySpec.Ports in remote deploy/compose
// sessions and the docker proxy. TCP mappings get one tunnel per accepted
// connection; UDP mappings get a local datagram socket with one tunnel per
// client source address, carrying length-prefixed datagram frames
// (wire.WriteDatagram — the hub's framing convention).
package portfwd

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// Dialer opens one raw tunnel to a container port of the named deployment's
// first instance. proto is "tcp" or "udp". A tcp tunnel carries exactly one
// connection's byte stream; a udp tunnel carries framed datagrams for one
// client flow, and the dial fails when the backend cannot forward UDP (the
// kubernetes backend — its pods/portforward subresource is TCP-only).
// *client.Client satisfies this.
type Dialer interface {
	PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error)
}

// Forward is one live local listener. Local is the actually-bound address
// (meaningful when the mapping requested port 0).
type Forward struct {
	Local   string
	Mapping api.PortMapping
}

// udpFlowIdle is the default idle timeout after which a per-source UDP flow's
// tunnel is reclaimed. UDP is connectionless — there is no FIN to end a flow —
// so an idle GC is the only reclaim path. Kept consistent with the hub's
// per-source reach flows (pkg/caretaker udpFlowIdle).
const udpFlowIdle = 60 * time.Second

// Option configures Start.
type Option func(*options)

type options struct {
	bindAddr string
	logf     func(format string, args ...any)
	strict   bool
	udpIdle  time.Duration
}

// WithBindAddress selects the local address listeners bind on (default 127.0.0.1).
func WithBindAddress(addr string) Option { return func(o *options) { o.bindAddr = addr } }

// WithLogf routes skip/failure messages (default: slog warnings).
func WithLogf(logf func(format string, args ...any)) Option {
	return func(o *options) { o.logf = logf }
}

// WithStrictBind makes any listener bind failure fatal (Start cleans up and
// returns the error) instead of warn-and-skip. Unforwardable mappings (UDP
// rejected by the backend) still only warn.
func WithStrictBind() Option { return func(o *options) { o.strict = true } }

// WithUDPIdleTimeout overrides the per-source UDP flow idle timeout (default
// udpFlowIdle, 60s, matching the hub's reach flows). Intended for tests.
func WithUDPIdleTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.udpIdle = d
		}
	}
}

// Group is a set of live forwards sharing one lifetime.
type Group struct {
	cancel    context.CancelFunc
	forwards  []Forward
	listeners []net.Listener
	pconns    []*net.UDPConn
	wg        sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  bool
}

// Start binds one local listener per mapping in ports and forwards traffic to
// the deployment's container ports: a TCP listener serving each accepted
// connection over its own tunnel, or a UDP socket serving per-source datagram
// flows (each source address gets its own tunnel, reclaimed after an idle
// timeout). Each UDP mapping is probed with one tunnel dial up front; when the
// backend rejects UDP forwarding (kubernetes — TCP-only pods/portforward), the
// mapping is skipped with a warning. Local-bind failures are also
// warn-and-skip, or fatal under WithStrictBind. The group closes itself when
// ctx ends; Close tears it down earlier. A mapping with Host 0 binds an
// ephemeral port, reported in Forward.Local.
func Start(ctx context.Context, d Dialer, name string, ports []api.PortMapping, opts ...Option) (*Group, error) {
	log := logging.FromContext(ctx)
	o := options{
		bindAddr: "127.0.0.1",
		logf: func(format string, args ...any) {
			log.WarnContext(ctx, fmt.Sprintf(format, args...))
		},
		udpIdle: udpFlowIdle,
	}
	for _, opt := range opts {
		opt(&o)
	}

	fctx, cancel := context.WithCancel(ctx)
	g := &Group{cancel: cancel, conns: make(map[net.Conn]struct{})}
	for _, m := range ports {
		switch m.Protocol {
		case "", "tcp":
		case "udp":
			if err := g.startUDP(fctx, d, name, m, &o); err != nil {
				g.Close()
				return nil, err
			}
			continue
		default:
			o.logf("port-forward: skipping %d:%d/%s: unsupported protocol", m.Host, m.Container, m.Protocol)
			continue
		}
		addr := net.JoinHostPort(o.bindAddr, strconv.Itoa(m.Host))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if o.strict {
				g.Close()
				return nil, fmt.Errorf("listen on %s: %w", addr, err)
			}
			o.logf("port-forward: skipping %s -> %s:%d: %v", addr, name, m.Container, err)
			continue
		}
		g.listeners = append(g.listeners, ln)
		g.forwards = append(g.forwards, Forward{Local: ln.Addr().String(), Mapping: m})

		g.wg.Add(1)
		go func(ln net.Listener, container int) {
			defer g.wg.Done()
			g.serve(fctx, d, name, ln, container, o.logf)
		}(ln, m.Container)
	}

	// Tie the group's lifetime to ctx so callers holding a session need no
	// explicit Close on the cancel path.
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		<-fctx.Done()
		g.shutdown()
	}()
	return g, nil
}

// startUDP sets up one UDP mapping: it probes the backend with a throwaway
// tunnel dial (error-driven backend detection — the server acks a udp tunnel
// before any frames flow, so a TCP-only backend such as kubernetes fails the
// dial with its rejection and the mapping is warn-and-skipped), then binds the
// local datagram socket and starts the per-source flow loop. Only a local bind
// failure can return an error, and only under strict.
func (g *Group) startUDP(ctx context.Context, d Dialer, name string, m api.PortMapping, o *options) error {
	probe, err := d.PortForward(ctx, name, m.Container, "udp")
	if err != nil {
		o.logf("port-forward: skipping %d:%d/udp: %v", m.Host, m.Container, err)
		return nil
	}
	_ = probe.Close()

	addr := net.JoinHostPort(o.bindAddr, strconv.Itoa(m.Host))
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err == nil {
		var pc *net.UDPConn
		pc, err = net.ListenUDP("udp", udpAddr)
		if err == nil {
			g.pconns = append(g.pconns, pc)
			g.forwards = append(g.forwards, Forward{Local: pc.LocalAddr().String(), Mapping: m})
			g.wg.Add(1)
			go func() {
				defer g.wg.Done()
				g.serveUDP(ctx, d, name, pc, m.Container, o.udpIdle, o.logf)
			}()
			return nil
		}
	}
	if o.strict {
		return fmt.Errorf("listen on udp %s: %w", addr, err)
	}
	o.logf("port-forward: skipping udp %s -> %s:%d: %v", addr, name, m.Container, err)
	return nil
}

// Forwards lists the live local listeners (skipped mappings are absent).
func (g *Group) Forwards() []Forward { return g.forwards }

// Close tears the group down: listeners close, in-flight tunnels are severed,
// and all serving goroutines drain. Idempotent.
func (g *Group) Close() {
	g.cancel()
	g.shutdown()
	g.wg.Wait()
}

// shutdown closes listeners and severs in-flight connections exactly once.
func (g *Group) shutdown() {
	g.mu.Lock()
	if g.done {
		g.mu.Unlock()
		return
	}
	g.done = true
	conns := make([]net.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()

	for _, ln := range g.listeners {
		_ = ln.Close()
	}
	for _, pc := range g.pconns {
		_ = pc.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// track registers an in-flight conn for severing on Close. It reports false —
// and does not register — when the group is already shutting down.
func (g *Group) track(c net.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done {
		return false
	}
	g.conns[c] = struct{}{}
	return true
}

func (g *Group) untrack(c net.Conn) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
}

// serve accepts connections on ln and forwards each over its own tunnel to the
// deployment's container port. It returns when ln is closed.
func (g *Group) serve(ctx context.Context, d Dialer, name string, ln net.Listener, container int, logf func(string, ...any)) {
	for {
		local, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		if !g.track(local) {
			_ = local.Close()
			return
		}
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			defer g.untrack(local)
			defer local.Close()
			tunnel, err := d.PortForward(ctx, name, container, "tcp")
			if err != nil {
				logf("port-forward to %s:%d failed: %v", name, container, err)
				return
			}
			if !g.track(tunnel) {
				_ = tunnel.Close()
				return
			}
			defer g.untrack(tunnel)
			wire.Pipe(local, tunnel)
		}()
	}
}

// udpFlow is one client source address's live path to the workload: the framed
// tunnel opened for it and when it was last active (for idle GC). UDP has no
// connection, so flows are keyed by source address and the same tunnel is
// reused for every datagram from that source, with replies routed back to it —
// the hub's per-source reach flow model (pkg/caretaker runUDPReach).
type udpFlow struct {
	tunnel net.Conn
	last   time.Time
}

// serveUDP is the per-source flow loop for one UDP mapping: every datagram
// arriving on pc finds or creates the flow for its source address (opening a
// udp tunnel and a reply reader that frames tunnel datagrams back to that
// source), then frames the datagram onto the flow's tunnel. An idle GC
// reclaims flows that go quiet. Returns when pc is closed (group shutdown).
func (g *Group) serveUDP(ctx context.Context, d Dialer, name string, pc *net.UDPConn, container int, idle time.Duration, logf func(string, ...any)) {
	var mu sync.Mutex
	flows := map[string]*udpFlow{}
	remove := func(key string, f *udpFlow) {
		mu.Lock()
		if flows[key] == f {
			delete(flows, key)
		}
		mu.Unlock()
		g.untrack(f.tunnel)
		_ = f.tunnel.Close()
	}
	// reclaimIfStale tears a flow down only if it is still the mapped flow AND
	// has not been refreshed since it was marked stale. The staleness recheck
	// happens under the same lock that the receive loop uses to refresh f.last
	// (and to install the flow), so a datagram that arrives after the GC snapshot
	// wins: it refreshes last before we re-read it here, and we leave the flow
	// alone rather than closing a tunnel that is actively in use.
	reclaimIfStale := func(key string, f *udpFlow, now time.Time) {
		mu.Lock()
		if flows[key] != f || now.Sub(f.last) <= idle {
			mu.Unlock()
			return
		}
		delete(flows, key)
		mu.Unlock()
		g.untrack(f.tunnel)
		_ = f.tunnel.Close()
	}

	// Idle GC: reclaim flows whose source has gone quiet (no UDP close exists).
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		t := time.NewTicker(idle)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				mu.Lock()
				var stale []*udpFlow
				var staleKeys []string
				for k, f := range flows {
					if now.Sub(f.last) > idle {
						stale = append(stale, f)
						staleKeys = append(staleKeys, k)
					}
				}
				mu.Unlock()
				for i, f := range stale {
					reclaimIfStale(staleKeys[i], f, now)
				}
			}
		}
	}()

	buf := make([]byte, wire.MaxDatagram)
	for {
		n, src, err := pc.ReadFromUDP(buf)
		if err != nil {
			return // socket closed on shutdown
		}
		key := src.String()
		// Fetch and refresh the flow under one lock: a flow observed non-nil
		// here is marked active (last = now) before we release mu, and the idle
		// GC's reclaim (reclaimIfStale) rechecks last under this same lock, so a
		// datagram racing a pending reclaim wins — the flow is kept alive and its
		// tunnel is not torn down mid-use. If the GC won the race and already
		// removed the flow, we observe nil here and dial a fresh tunnel below.
		mu.Lock()
		f := flows[key]
		if f != nil {
			f.last = time.Now()
		}
		mu.Unlock()
		if f == nil {
			tunnel, derr := d.PortForward(ctx, name, container, "udp")
			if derr != nil {
				logf("port-forward udp to %s:%d failed: %v", name, container, derr)
				continue // transient; the client will retry (UDP)
			}
			if !g.track(tunnel) {
				_ = tunnel.Close()
				return
			}
			f = &udpFlow{tunnel: tunnel, last: time.Now()}
			mu.Lock()
			flows[key] = f
			mu.Unlock()
			src := src
			// Reply reader: each datagram framed back on the tunnel goes to
			// this flow's source.
			g.wg.Add(1)
			go func() {
				defer g.wg.Done()
				for {
					dgram, rerr := wire.ReadDatagram(tunnel)
					if rerr != nil {
						remove(key, f)
						return
					}
					if _, werr := pc.WriteToUDP(dgram, src); werr != nil {
						remove(key, f)
						return
					}
				}
			}()
		}
		if werr := wire.WriteDatagram(f.tunnel, buf[:n]); werr != nil {
			remove(key, f)
		}
	}
}
