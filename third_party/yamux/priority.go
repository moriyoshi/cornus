package yamux

import (
	"sync"
	"time"
)

// SchedulerMode selects the send scheduler (cornus fork), set per session via
// Config.SchedulerMode. It replaces the earlier env-var knobs so behavior is
// configured programmatically (and can be varied as a matrix dimension in tests).
type SchedulerMode int

const (
	// SchedPriorityWRR is the default: strict priority for control frames, then
	// deficit weighted round-robin among the data classes.
	SchedPriorityWRR SchedulerMode = iota
	// SchedUrgentOnly keeps strict priority for control frames but collapses all
	// data to one class (no data-class WRR).
	SchedUrgentOnly
	// SchedFIFO routes every frame through a single FIFO queue (no priority / WRR)
	// — stock-yamux-like ordering; the frame cap still applies.
	SchedFIFO
)

// This file is a cornus fork addition: a QoS send scheduler that replaces yamux's
// single FIFO sendCh. Session CONTROL frames (window updates, FIN/RST, ping,
// goaway, SYN/ACK) are ClassUrgent and go out strictly ahead of all data; DATA
// frames are scheduled by deficit weighted round-robin across the remaining
// classes, so a bulk block/9P mount stream cannot starve the latency-sensitive
// control channel or the other mounts sharing one multiplexed session.
//
// Backpressure is preserved: the scheduler is bounded (schedCap), and push()
// blocks (honoring the connection write timeout / shutdown) when full, exactly as
// the old bounded sendCh did.

// Send priority classes. Streams default to ClassNormal; the application assigns
// a stream's data class with (*Stream).SetPriority. ClassUrgent is reserved for
// session control frames and is always strict-priority — do not assign it to a
// stream's data.
const (
	ClassBulk   uint8 = iota // 0: bulk data (block/9P mounts) — lowest WRR weight
	ClassNormal              // 1: default data streams — medium weight
	ClassHigh                // 2: latency-sensitive data (e.g. the control channel)
	ClassUrgent              // 3: session control frames — STRICT priority above all data
	numSendClasses
)

// numDataClasses is the count of weighted (non-strict) data classes.
const numDataClasses = int(ClassUrgent)

// wrrWeights is the byte quantum each DATA class earns per deficit round
// (Bulk : Normal : High = 1 : 4 : 16). ClassUrgent is strict, not weighted.
var wrrWeights = [numSendClasses]int64{
	ClassBulk:   64 << 10,
	ClassNormal: 256 << 10,
	ClassHigh:   1 << 20,
}

// schedCap bounds the number of frames queued in the scheduler before push()
// blocks, providing backpressure (the old sendCh buffered 64; per-stream
// synchronous sends keep the real depth near the active-writer count).
const schedCap = 256

// defaultMaxDataFrame is the default single-DATA-frame payload cap (cornus fork),
// independent of the (possibly large) send window, so the send loop never spends
// a long uninterruptible conn.Write on one frame — bounding head-of-line delay
// for the urgent control frames that must interleave. A larger Write simply
// fragments into several frames. Overridable per session via Config.MaxDataFrame.
const defaultMaxDataFrame uint32 = 128 << 10

// sched is the session send scheduler: for the default mode, strict priority for
// ClassUrgent, then deficit weighted round-robin among the data classes.
type sched struct {
	mode SchedulerMode

	mu      sync.Mutex
	q       [numSendClasses][]*sendReady
	deficit [numSendClasses]int64
	rr      int // round-robin cursor across data classes
	n       int // total queued frames

	notify chan struct{} // signaled when a frame is queued (readers wait on it)
	space  chan struct{} // signaled when a frame is dequeued (blocked pushers wait on it)
}

func newSched(mode SchedulerMode) *sched {
	return &sched{
		mode:   mode,
		notify: make(chan struct{}, 1),
		space:  make(chan struct{}, 1),
	}
}

// push enqueues r by its class, blocking when the scheduler is full until space
// frees up, the session shuts down, or the write timeout fires. It mirrors the
// old `select { case s.sendCh <- ready ... }` backpressure.
func (sc *sched) push(r *sendReady, shutdownCh <-chan struct{}, timeoutCh <-chan time.Time) error {
	c := r.class
	switch {
	case sc.mode == SchedFIFO:
		c = ClassNormal // all frames share one queue -> served in arrival order (FIFO)
	case sc.mode == SchedUrgentOnly && c != ClassUrgent:
		c = ClassNormal // strict-urgent kept; all data collapses to one class (no WRR)
	case c >= numSendClasses:
		c = ClassNormal
	}
	for {
		sc.mu.Lock()
		if sc.n < schedCap {
			sc.q[c] = append(sc.q[c], r)
			sc.n++
			sc.mu.Unlock()
			asyncNotify(sc.notify)
			return nil
		}
		sc.mu.Unlock()
		select {
		case <-sc.space:
		case <-shutdownCh:
			return ErrSessionShutdown
		case <-timeoutCh:
			return ErrConnectionWriteTimeout
		}
	}
}

// next blocks until a frame is available and returns it, or returns nil when the
// session shuts down.
func (sc *sched) next(shutdownCh <-chan struct{}) *sendReady {
	for {
		sc.mu.Lock()
		r := sc.pickLocked()
		sc.mu.Unlock()
		if r != nil {
			asyncNotify(sc.space)
			return r
		}
		select {
		case <-sc.notify:
		case <-shutdownCh:
			return nil
		}
	}
}

// pickLocked returns the next frame to send, or nil if empty. Caller holds mu.
func (sc *sched) pickLocked() *sendReady {
	if sc.n == 0 {
		return nil
	}
	// Strict priority: urgent control frames first.
	if len(sc.q[ClassUrgent]) > 0 {
		return sc.popLocked(ClassUrgent)
	}
	// Deficit weighted round-robin among the data classes: two passes so a
	// replenish can happen between them.
	for pass := 0; pass < 2; pass++ {
		for i := 0; i < numDataClasses; i++ {
			c := (sc.rr + i) % numDataClasses
			if len(sc.q[c]) > 0 && sc.deficit[c] > 0 {
				sc.deficit[c] -= frameCost(sc.q[c][0])
				sc.rr = (c + 1) % numDataClasses
				return sc.popLocked(uint8(c))
			}
		}
		// No eligible class; grant each non-empty data class its quantum.
		for c := 0; c < numDataClasses; c++ {
			if len(sc.q[c]) > 0 {
				sc.deficit[c] += wrrWeights[c]
			}
		}
	}
	// Fallback (should be unreachable given n>0): serve any non-empty data class.
	for c := 0; c < numDataClasses; c++ {
		if len(sc.q[c]) > 0 {
			return sc.popLocked(uint8(c))
		}
	}
	return nil
}

func (sc *sched) popLocked(c uint8) *sendReady {
	r := sc.q[c][0]
	sc.q[c] = sc.q[c][1:]
	sc.n--
	if len(sc.q[c]) == 0 {
		sc.q[c] = nil     // release the backing array
		sc.deficit[c] = 0 // DRR: reset an idle class so it cannot burst later
	}
	return r
}

// frameCost is the on-wire byte cost of a frame for WRR accounting. For a data
// frame the header's Length field equals the body length; reading it avoids
// touching r.Body under a lock.
func frameCost(r *sendReady) int64 {
	if len(r.Hdr) < headerSize {
		return 1
	}
	return int64(headerSize) + int64(header(r.Hdr).Length())
}
