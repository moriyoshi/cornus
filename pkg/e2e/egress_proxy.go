package e2e

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"go.starlark.net/starlark"
)

// egressProxy is an in-process SOCKS5 proxy the harness runs on the loopback of the
// harness host. It exists so a scenario can point the CLIENT's own egress dialer at
// it (ALL_PROXY=socks5h://<addr>) and then PROVE that client-side egress physically
// left through the client's sanctioned proxy: every destination the client dials is
// recorded here, so the scenario asserts the proxy actually carried the connection.
// It resolves domain names itself (socks5h / remote DNS) and tunnels best-effort —
// recording happens on CONNECT, before the upstream dial, so an unreachable sentinel
// destination still proves the routing without needing a reachable target.
type egressProxy struct {
	ln   net.Listener
	mu   sync.Mutex
	hits []string
}

func (p *egressProxy) record(target string) {
	p.mu.Lock()
	p.hits = append(p.hits, target)
	p.mu.Unlock()
}

func (p *egressProxy) targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.hits...)
}

func (p *egressProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

// handle speaks the no-auth SOCKS5 handshake, records the requested target, then
// tunnels to it best-effort.
func (p *egressProxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	ver, err := br.ReadByte()
	if err != nil || ver != 0x05 {
		return
	}
	nMethods, err := br.ReadByte()
	if err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, br, int64(nMethods)); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil { // no auth
		return
	}
	hdr := make([]byte, 4)                                            // ver, cmd, rsv, atyp
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[1] != 0x01 { // CONNECT only
		return
	}
	// Every address read is checked, and a zero-length domain is rejected.
	//
	// The checks are the cheap part and are here for local soundness; on their own
	// they change nothing observable, because the PORT read immediately below is
	// checked and backstops them — a request too short for its address is also too
	// short for its port, so it never reaches record(). That is worth stating
	// because it is easy to read the unchecked reads as an exploitable hole and
	// then "fix" the wrong thing.
	//
	// The reachable defect is the zero-length domain. A CONNECT with atyp=0x03 and
	// a length byte of 0 is well-formed in every respect the reads can see: the
	// (empty) address read succeeds, the port read succeeds, and the double
	// records net.JoinHostPort("", port) — ":8080" — as a destination the client
	// asked for. A recording double that invents destinations is worse than one
	// that drops them: a scenario asserting on what the client dialed is handed a
	// confident wrong answer instead of a failure.
	var host string
	switch hdr[3] {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 0x03: // domain name (socks5h)
		l, err := br.ReadByte()
		if err != nil || l == 0 {
			return
		}
		b := make([]byte, int(l))
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	var pb [2]byte
	if _, err := io.ReadFull(br, pb[:]); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))
	p.record(target)

	up, err := net.Dial("tcp", target)
	if err != nil {
		c.Write([]byte{0x05, 0x01, 0, 1, 0, 0, 0, 0, 0, 0}) // general failure
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{0x05, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil { // success
		return
	}
	splice(c, up, br)
}

// splice pumps both directions and does not return until both are finished.
//
// The naive `go io.Copy(up, br); io.Copy(c, up)` returns as soon as the UPSTREAM
// direction ends, whereupon handle's deferred Close on both conns kills the
// client->upstream copy mid-flight. For a scenario whose workload writes its
// request and reads a reply that is usually harmless; for one that keeps writing
// it silently truncates. Waiting for both, and half-closing each side as its
// direction drains, is what makes the double faithful enough that a scenario
// failure means the product misbehaved.
func splice(client net.Conn, up net.Conn, clientR io.Reader) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, clientR)
		closeWrite(up)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, up)
		closeWrite(client)
	}()
	wg.Wait()
}

// closeWrite half-closes c if it can, so the peer sees EOF on that direction
// while the other one keeps flowing. A conn that cannot half-close is left alone;
// handle's deferred Close still tears it down once both directions are done.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// bEgressProxy starts an in-process SOCKS5 recording proxy on the harness loopback
// and returns its "host:port" address, for use as the client's ALL_PROXY
// (socks5h://<addr>). Query what it carried with egress_proxy_hits(addr).
func (h *Harness) bEgressProxy(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("egress_proxy", args, kwargs); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("egress_proxy: listen: %w", err)
	}
	p := &egressProxy{ln: ln}
	go p.serve()
	if h.egressProxies == nil {
		h.egressProxies = map[string]*egressProxy{}
	}
	addr := ln.Addr().String()
	h.egressProxies[addr] = p
	h.logf("• egress_proxy listening on socks5h://%s", addr)
	return starlark.String(addr), nil
}

// bEgressProxyHits returns the list of "host:port" destinations the egress_proxy at
// addr has been asked to reach — i.e. the destinations the client dialed through its
// own proxy.
func (h *Harness) bEgressProxyHits(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr string
	if err := starlark.UnpackArgs("egress_proxy_hits", args, kwargs, "addr", &addr); err != nil {
		return nil, err
	}
	p := h.egressProxies[addr]
	if p == nil {
		return nil, fmt.Errorf("egress_proxy_hits: no egress_proxy at %q", addr)
	}
	var elems []starlark.Value
	for _, t := range p.targets() {
		elems = append(elems, starlark.String(t))
	}
	return starlark.NewList(elems), nil
}

// stopEgressProxies closes every in-process egress proxy started this scenario.
func (h *Harness) stopEgressProxies() {
	for _, p := range h.egressProxies {
		_ = p.ln.Close()
	}
	h.egressProxies = nil
}
