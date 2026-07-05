package qosab

import (
	"io"
	"net"
	"sync"
	"time"
)

// This file is a lightweight in-process link simulator for A/B testing the yamux
// QoS decision without containers or a real network. newLink returns a duplex
// net.Conn pair whose bytes are delivered with a configurable one-way bandwidth
// (serialization delay) and latency (propagation delay), so yamux — and traffic
// shaped like the block/9P mount protocol — can be driven over emulated LAN/WAN
// conditions and measured. It imports only stdlib, so (like the rest of qosab) it
// compiles against both stock yamux and the in-repo fork for the replace toggle.

// LinkProfile describes an emulated link.
type LinkProfile struct {
	Name        string
	BytesPerSec float64       // one-way bandwidth; 0 = unlimited
	Latency     time.Duration // one-way propagation delay
}

// newLink returns two endpoints of a duplex link with the given per-direction
// bandwidth and latency. Close either end to tear the link down.
func newLink(p LinkProfile) (net.Conn, net.Conn) {
	ab := newHalfPipe(p.BytesPerSec, p.Latency) // a -> b
	ba := newHalfPipe(p.BytesPerSec, p.Latency) // b -> a
	return &linkConn{r: ba, w: ab}, &linkConn{r: ab, w: ba}
}

// halfPipe is one direction of the link. Two stages model a real link correctly:
// the SERIALIZER occupies the "wire" sequentially at the bandwidth rate (so
// throughput is capped and the writer backpressures), and the DELAY LINE adds the
// propagation latency in PARALLEL (many chunks in flight at once — latency does
// NOT serialize). The `in` buffer is deliberately tiny (a couple of frames, like
// a bounded socket send buffer) so that when the link is busy the sender's writes
// block and frames queue in yamux's own scheduler — which is where the QoS
// priority must take effect, not in a fat downstream buffer (bufferbloat would
// hide it).
type halfPipe struct {
	in   chan []byte
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	dead bool
}

type timedChunk struct {
	at   time.Time
	data []byte
}

func newHalfPipe(bw float64, latency time.Duration) *halfPipe {
	h := &halfPipe{in: make(chan []byte, 2)}
	h.cond = sync.NewCond(&h.mu)
	deliver := make(chan timedChunk, 64)
	go h.serialize(bw, latency, deliver)
	go h.deliver(deliver)
	return h
}

// serialize paces chunks at the bandwidth rate (sequential wire occupancy), then
// stamps each with its arrival time (now + latency) and hands it to the delay
// line. The bandwidth sleep here is what caps throughput and backpressures the
// writer; latency is applied downstream, in parallel.
func (h *halfPipe) serialize(bw float64, latency time.Duration, out chan<- timedChunk) {
	for chunk := range h.in {
		if bw > 0 {
			time.Sleep(time.Duration(float64(len(chunk)) / bw * float64(time.Second)))
		}
		out <- timedChunk{at: time.Now().Add(latency), data: chunk}
	}
	close(out)
}

// deliver waits until each chunk's arrival time then makes it readable. Because
// serialize feeds at the bandwidth rate and stamps a constant latency, arrival
// times are monotonic and only the first chunk pays the full latency; the rest
// pipeline (latency is parallel).
func (h *halfPipe) deliver(in <-chan timedChunk) {
	for tc := range in {
		if d := time.Until(tc.at); d > 0 {
			time.Sleep(d)
		}
		h.mu.Lock()
		h.buf = append(h.buf, tc.data...)
		h.cond.Broadcast()
		h.mu.Unlock()
	}
	h.mu.Lock()
	h.dead = true
	h.cond.Broadcast()
	h.mu.Unlock()
}

func (h *halfPipe) write(b []byte) (int, error) {
	c := make([]byte, len(b))
	copy(c, b)
	defer func() { _ = recover() }() // send on closed channel after Close -> treat as EOF
	h.in <- c
	return len(b), nil
}

func (h *halfPipe) read(b []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for len(h.buf) == 0 && !h.dead {
		h.cond.Wait()
	}
	if len(h.buf) == 0 && h.dead {
		return 0, io.EOF
	}
	n := copy(b, h.buf)
	h.buf = h.buf[n:]
	return n, nil
}

func (h *halfPipe) close() {
	defer func() { _ = recover() }()
	close(h.in)
}

// linkConn is one endpoint: it reads from direction r and writes to direction w.
type linkConn struct {
	r, w    *halfPipe
	closeMu sync.Once
}

func (c *linkConn) Read(b []byte) (int, error)  { return c.r.read(b) }
func (c *linkConn) Write(b []byte) (int, error) { return c.w.write(b) }
func (c *linkConn) Close() error {
	c.closeMu.Do(func() { c.w.close() })
	return nil
}
func (c *linkConn) LocalAddr() net.Addr                { return emuAddr{} }
func (c *linkConn) RemoteAddr() net.Addr               { return emuAddr{} }
func (c *linkConn) SetDeadline(t time.Time) error      { return nil }
func (c *linkConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *linkConn) SetWriteDeadline(t time.Time) error { return nil }

type emuAddr struct{}

func (emuAddr) Network() string { return "emu" }
func (emuAddr) String() string  { return "emu" }
