package caretaker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"cornus/pkg/logging"
)

// ProxyRole is the caretaker's egress proxy. Mode selects enforcement:
//
//   - "" / "enforcing": the app-container's outbound TCP is redirected here by
//     nftables, and a connection is forwarded ONLY if its (pre-DNAT) destination
//     resolves to one of the Allow peers — real L4 isolation on a flat pod
//     network, independent of the cluster CNI. Allow is a set of peer service
//     names; the proxy resolves them to the set of permitted destination IPs and
//     refreshes it, so scaling a peer up/down is picked up without a restart. The
//     proxy's own upstream dials escape the redirect because the sidecar runs as
//     a dedicated uid the nftables rules exempt. ListenPort is the redirect port.
//
//   - "cooperative": no redirect, no privilege. The backend has pointed each
//     peer's DNS name at a distinct loopback address (Coop[i].Listen) via
//     hostAliases; the proxy listens on each (loopback, port) it is told about
//     and forwards to the peer's real Service (Coop[i].Forward). Soft isolation
//     (an app that dials a raw pod IP bypasses it).
type ProxyRole struct {
	Mode       string         `json:"mode,omitempty"`
	ListenPort int            `json:"listenPort,omitempty"`
	Allow      []string       `json:"allow,omitempty"`
	Coop       []CoopUpstream `json:"coop,omitempty"`
}

// CoopUpstream is one cooperative-mode peer: the sidecar binds Listen (a
// loopback address the peer's name resolves to via hostAliases) on each of
// Ports and forwards accepted connections to Forward (the peer's real
// resolvable Service name) on the same port.
type CoopUpstream struct {
	Listen  string `json:"listen"`
	Forward string `json:"forward"`
	Ports   []int  `json:"ports,omitempty"`
}

// runProxy dispatches to the configured enforcement mode. mark, when non-zero,
// is stamped on the proxy's own upstream dials so the egress redirect exempts
// them (used when the caretaker runs as root alongside mounts and cannot be
// exempted by uid).
func runProxy(ctx context.Context, p ProxyRole, mark int) error {
	if p.Mode == "cooperative" {
		return runCooperative(ctx, p.Coop, mark)
	}
	return runEnforcing(ctx, p, mark)
}

// runEnforcing resolves the allow-set, then accepts redirected connections and
// splices each to its original destination when permitted.
func runEnforcing(ctx context.Context, p ProxyRole, mark int) error {
	ctx = logging.WithAttrs(ctx, slog.String("component", "proxy"))
	as := newAllowSet(p.Allow)
	as.refresh(ctx) // seed before accepting, so early connections are decided correctly
	go as.refreshLoop(ctx)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p.ListenPort))
	if err != nil {
		return fmt.Errorf("proxy listen :%d: %w", p.ListenPort, err)
	}
	logging.FromContext(ctx).InfoContext(ctx, "caretaker proxy listening", "port", p.ListenPort, "allow", p.Allow, "resolved_ips", as.size())
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		tcp, ok := c.(*net.TCPConn)
		if !ok {
			c.Close()
			continue
		}
		go handleProxy(ctx, tcp, as, mark)
	}
}

// handleProxy forwards one redirected connection to its original destination if
// allowed; a denied connection is simply dropped (the app sees a reset/refused).
// The upstream dial is stamped with mark so it escapes the redirect (see
// runProxy).
func handleProxy(ctx context.Context, c *net.TCPConn, as *allowSet, mark int) {
	defer c.Close()
	mt := metrics()
	log := logging.FromContext(ctx)
	dst, err := originalDst(c)
	if err != nil {
		log.WarnContext(ctx, "SO_ORIGINAL_DST failed", "error", err)
		mt.proxyConns.Add(ctx, 1, connAttr("error"))
		return
	}
	host, _, err := net.SplitHostPort(dst)
	if err != nil || !as.allowed(host) {
		log.WarnContext(ctx, "deny", "dst", dst, "allowed", as.snapshot())
		mt.proxyConns.Add(ctx, 1, connAttr("deny"))
		return // deny
	}
	up, err := markDialer(mark).Dial("tcp", dst)
	if err != nil {
		log.WarnContext(ctx, "upstream dial failed", "dst", dst, "error", err)
		mt.proxyConns.Add(ctx, 1, connAttr("error"))
		return
	}
	defer up.Close()
	mt.proxyConns.Add(ctx, 1, connAttr("allow"))
	ab, ba := spliceBidir(c, up)
	recordProxyBytes(mt, ab, ba)
}

// runCooperative binds each peer's loopback address on each of its ports and
// forwards accepted connections to the peer's real Service. It needs no
// privilege: the loopback aliases and the app's own DNS do the interception.
func runCooperative(ctx context.Context, peers []CoopUpstream, mark int) error {
	ctx = logging.WithAttrs(ctx, slog.String("component", "proxy"))
	g, gctx := errgroup.WithContext(ctx)
	var total int
	for _, up := range peers {
		up := up
		for _, port := range up.Ports {
			port := port
			listen := net.JoinHostPort(up.Listen, strconv.Itoa(port))
			forward := net.JoinHostPort(up.Forward, strconv.Itoa(port))
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("proxy listen %s: %w", listen, err)
			}
			total++
			g.Go(func() error {
				<-gctx.Done()
				ln.Close()
				return nil
			})
			g.Go(func() error {
				for {
					c, err := ln.Accept()
					if err != nil {
						if gctx.Err() != nil {
							return nil
						}
						return err
					}
					go forwardCoop(ctx, c, forward, mark)
				}
			})
		}
	}
	logging.FromContext(ctx).InfoContext(ctx, "cooperative mode", "listeners", total, "peers", len(peers))
	return g.Wait()
}

// forwardCoop splices one cooperative connection to the peer's real Service.
func forwardCoop(ctx context.Context, c net.Conn, forward string, mark int) {
	defer c.Close()
	mt := metrics()
	up, err := markDialer(mark).Dial("tcp", forward)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "upstream dial failed", "forward", forward, "error", err)
		mt.proxyConns.Add(ctx, 1, connAttr("error"))
		return
	}
	defer up.Close()
	mt.proxyConns.Add(ctx, 1, connAttr("allow"))
	ab, ba := spliceBidir(c, up)
	recordProxyBytes(mt, ab, ba)
}

// spliceBidir copies both directions between two connections and returns once
// both halves have hit EOF, half-closing each write side as its source drains.
// It returns the bytes copied a->b (ab) and b->a (ba).
func spliceBidir(a, b net.Conn) (ab, ba int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	splice := func(dst, src net.Conn, n *int64) {
		defer wg.Done()
		c, _ := io.Copy(dst, src)
		*n = c
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	go splice(b, a, &ab) // a -> b
	go splice(a, b, &ba) // b -> a
	wg.Wait()
	return ab, ba
}

// allowSet resolves peer service names to the set of destination IPs the proxy
// permits, refreshed periodically. It keeps each name's last successfully
// resolved IPs so a transient per-name lookup failure reuses that name's prior
// IPs rather than dropping a healthy peer (fail-static).
type allowSet struct {
	names []string
	mu    sync.RWMutex
	ips   map[string]bool
	// perName is the last successfully resolved IP set for each name, used to
	// carry a name's addresses across a transient lookup error. Guarded by mu.
	perName map[string]map[string]bool
	// lookupHost resolves a name to its IPs; a field so tests can inject a fake
	// resolver. Defaults to net.DefaultResolver.LookupHost.
	lookupHost func(ctx context.Context, host string) ([]string, error)
}

func newAllowSet(names []string) *allowSet {
	return &allowSet{
		names:      names,
		ips:        map[string]bool{},
		perName:    map[string]map[string]bool{},
		lookupHost: net.DefaultResolver.LookupHost,
	}
}

// refresh resolves every allowed name and replaces the permitted-IP set. On a
// per-name lookup error it reuses that name's previously resolved IPs, so a
// single peer's transient DNS failure does not drop it from the allow-set.
func (a *allowSet) refresh(ctx context.Context) {
	next := map[string]bool{}
	resolved := map[string]map[string]bool{}
	for _, n := range a.names {
		addrs, err := a.lookupHost(ctx, n)
		if err != nil {
			// Transient DNS failure for this name: keep whatever the previous
			// refresh resolved for it, so a healthy peer is not denied.
			a.mu.RLock()
			prev := a.perName[n]
			a.mu.RUnlock()
			resolved[n] = prev
			for ip := range prev {
				next[ip] = true
			}
			continue
		}
		cur := map[string]bool{}
		for _, ip := range addrs {
			cur[ip] = true
			next[ip] = true
		}
		resolved[n] = cur
	}
	// Never shrink to empty on a total DNS failure — keep the prior set so a
	// blip does not open (deny) or, worse, black-hole all peers mid-flight.
	if len(next) == 0 && len(a.names) > 0 {
		return
	}
	a.mu.Lock()
	a.ips = next
	a.perName = resolved
	a.mu.Unlock()
}

func (a *allowSet) refreshLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refresh(ctx)
		}
	}
}

func (a *allowSet) allowed(ip string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ips[ip]
}

func (a *allowSet) size() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.ips)
}

func (a *allowSet) snapshot() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.ips))
	for ip := range a.ips {
		out = append(out, ip)
	}
	return out
}
