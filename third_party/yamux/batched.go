// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package yamux

import (
	"sync"
	"sync/atomic"
	"time"
)

// This file is a cornus fork addition: the batched, pipelined send path, an
// alternative to the classic synchronous sendLoop selected by Config.SendMode.
//
// The classic path (SendSync) writes every DATA frame as TWO conn.Write calls
// (header, then body) and blocks the writing stream on an error channel until the
// send loop has put that one frame on the wire. Over the production transport —
// a WebSocket where each conn.Write is one WS message — that is two messages per
// frame and no intra-stream pipelining (frame N+1 is not even queued until frame
// N is on the wire).
//
// The batched-pipelined path (SendBatchedPipelined) instead:
//
//   - Copies each frame's header+body into ONE pooled contiguous buffer at push
//     time (sendDataAsync), so the frame is a single conn.Write == one WS message,
//     and the caller's buffer is decoupled immediately (no wait). Backpressure is
//     the flow-control send window plus the scheduler depth (schedCap), not a
//     per-frame wire round-trip, so a single stream keeps many frames in flight.
//   - Opportunistically coalesces already-queued frames into one write, bounded by
//     one frame cap so the longest uninterruptible write stays ~one frame — the
//     same head-of-line bound the 128 KiB frame cap gives the SendSync path, so
//     strict-priority control frames still interleave promptly.
//
// QoS ordering is unchanged: frames are still pulled through the priority
// scheduler (pickLocked: strict ClassUrgent first, then data-class DWRR); the
// batch is merely how the picked frames are handed to conn.Write.

// SendMode selects how a session serializes frames to the wire (cornus fork).
type SendMode int

const (
	// SendSync is the classic yamux path: two conn.Write calls per DATA frame
	// (header then body) and a synchronous per-frame handshake — the writer blocks
	// until the send loop has written its frame. Kept as the A/B baseline.
	SendSync SendMode = iota
	// SendBatchedPipelined coalesces each frame's header+body into a single
	// conn.Write, pipelines a stream's frames (no per-frame wire round-trip), and
	// batches already-queued frames into one write up to a frame-cap bound.
	SendBatchedPipelined
)

// maxPooledFrame bounds the capacity of buffers kept in frameBufPool. The
// production frame cap is 128 KiB; frames larger than this (e.g. a big custom
// Config.MaxDataFrame) are allocated per-use and not pooled, so a pathological cap
// cannot pin large buffers in the pool for the session's life.
const maxPooledFrame = 128 << 10

// defaultPipelineDepth is the per-stream in-flight DATA-frame cap used when
// Config.PipelineDepth is unset. Depth 1 is ~the synchronous path (no pipelining);
// a small depth (a few frames) hides the per-frame wire round-trip while keeping
// the QoS scheduler — not a fat downstream buffer — the place frames queue, so
// control frames keep interleaving and bulk streams stay fair under saturation.
const defaultPipelineDepth = 4

// frameBufPool recycles the contiguous [header||body] buffers for the
// batched-pipelined path; sendReadyPool recycles the sendReady wrappers. Both cut
// the per-frame allocation the SendSync path pays (a fresh *sendReady each frame).
var (
	frameBufPool  = sync.Pool{New: func() any { b := make([]byte, 0, headerSize+maxPooledFrame); return &b }}
	sendReadyPool = sync.Pool{New: func() any { return new(sendReady) }}
)

func getFrameBuf(n int) *[]byte {
	if n <= headerSize+maxPooledFrame {
		bp := frameBufPool.Get().(*[]byte)
		*bp = (*bp)[:n]
		return bp
	}
	b := make([]byte, n)
	return &b
}

func putFrameBuf(bp *[]byte) {
	if cap(*bp) <= headerSize+maxPooledFrame {
		*bp = (*bp)[:0]
		frameBufPool.Put(bp)
	}
}

// pipelineDepth is Config.PipelineDepth with the default applied — the per-stream
// in-flight frame cap.
func (s *Session) pipelineDepth() int32 {
	d := s.config.PipelineDepth
	if d <= 0 {
		d = defaultPipelineDepth
	}
	return int32(d)
}

// sendDataAsync enqueues one DATA frame from stream st on the batched-pipelined
// path. It first blocks while the stream already has PipelineDepth frames in
// flight (the bufferbloat/burst guard), then copies the header and body into a
// pooled contiguous buffer (so the caller may reuse body immediately) and pushes
// to the scheduler. It does NOT wait for the frame to reach the wire — that is the
// pipelining win — but the per-stream cap keeps a bulk stream from racing far
// enough ahead to bury control frames or starve peers.
func (s *Session) sendDataAsync(st *Stream, flags uint16, body []byte) error {
	depth := s.pipelineDepth()

	t := timerPool.Get()
	timer := t.(*time.Timer)
	timer.Reset(s.config.ConnectionWriteTimeout)
	defer func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		timerPool.Put(t)
	}()

	// Per-stream in-flight bound. Only this stream's Write goroutine pushes here
	// (Stream.Write holds sendLock), so the load-then-increment is race-free against
	// itself; the send loop decrements concurrently via recycle.
	for atomic.LoadInt32(&st.sendInflight) >= depth {
		select {
		case <-st.sendPipeCh:
		case <-s.shutdownCh:
			return ErrSessionShutdown
		case <-timer.C:
			return ErrConnectionWriteTimeout
		}
	}

	n := headerSize + len(body)
	bufp := getFrameBuf(n)
	buf := *bufp
	header(buf[:headerSize]).encode(typeData, flags, st.id, uint32(len(body)))
	copy(buf[headerSize:], body)

	r := sendReadyPool.Get().(*sendReady)
	r.Hdr = buf
	r.Body = nil
	r.Err = nil
	r.class = st.sendClass
	r.bufp = bufp
	r.stream = st

	atomic.AddInt32(&st.sendInflight, 1)
	if err := s.sched.push(r, s.shutdownCh, timer.C); err != nil {
		// The frame never entered a queue; reclaim its buffer and wrapper.
		atomic.AddInt32(&st.sendInflight, -1)
		putFrameBuf(bufp)
		r.Hdr = nil
		r.bufp = nil
		r.stream = nil
		sendReadyPool.Put(r)
		return err
	}
	return nil
}

// recycle returns a pooled (batched-pipelined) frame's buffer and wrapper to their
// pools. Control frames and SendSync frames (bufp == nil) are left for the GC — they
// are not owned by these pools.
func (s *Session) recycle(r *sendReady) {
	if r.bufp == nil {
		return
	}
	if r.stream != nil {
		// The frame is off to the wire: free a per-stream in-flight slot and wake a
		// Write blocked on the PipelineDepth cap.
		atomic.AddInt32(&r.stream.sendInflight, -1)
		asyncNotify(r.stream.sendPipeCh)
	}
	putFrameBuf(r.bufp)
	r.Hdr = nil
	r.Body = nil
	r.Err = nil
	r.bufp = nil
	r.stream = nil
	sendReadyPool.Put(r)
}

// sendLoopBatched is the pipelined send loop (Config.SendMode ==
// SendBatchedPipelined). Each frame is written as a SINGLE conn.Write: pooled DATA
// frames already carry a contiguous [header||body] in Hdr (so one write, not the
// classic path's two), and control frames are header-only. It deliberately does
// NOT coalesce across frames: coalescing a small urgent/high frame with a large
// bulk frame in one write makes the wire (or the peer stack) serialize/deliver the
// whole write as a unit, delaying the latency-sensitive frame by the bulk frame's
// transmission time — which measurably erased the QoS latency win. Frame-level
// priority is preserved by the scheduler; the win here is the single write per
// frame plus pipelining (no per-frame wire round-trip). Errors are signaled to any
// waiting frames (control frames carry an Err channel); pooled frames are recycled.
func (s *Session) sendLoopBatched() error {
	defer close(s.sendDoneCh)
	for {
		ready := s.sched.next(s.shutdownCh)
		if ready == nil {
			return nil
		}
		var err error
		if ready.Hdr != nil {
			_, err = s.conn.Write(ready.Hdr)
		}
		// Pooled DATA frames keep body contiguous in Hdr; only rare control frames
		// with a separate Body reach here, handled defensively.
		if err == nil && ready.Body != nil {
			_, err = s.conn.Write(ready.Body)
		}
		asyncSendErr(ready.Err, err)
		s.recycle(ready)
		if err != nil {
			s.logger.Printf("[ERR] yamux: Failed to write frame: %v", err)
			return err
		}
	}
}
