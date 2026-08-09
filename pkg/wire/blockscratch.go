package wire

import "sync"

// scratchList is a bounded, GC-PROOF free list of fixed-size buffers, with a
// sync.Pool as the overflow tier.
//
// Why not a plain sync.Pool, which is what this replaces: the GC empties one
// completely on every cycle, and the block protocol's write path has something
// else on it allocating at roughly the rate we want to reuse at — the p9 server's
// per-Twrite payload buffer, which an allocation profile put at 69% of the write
// leg's bytes. So the pool was cleared between one request and the next and
// essentially never hit, measured at 4.7 MB of garbage per 16 MiB written. A
// buffer parked in a channel is reachable, so no collection can take it.
//
// Why keep is 2, and what that number is and is not backed by. It is NOT tuned:
// keep=1 and keep=4 measured identically — within noise on allocated bytes — on
// every workload the in-process harness can produce, sequential and WebSocket
// alike. What the evidence supports is "GC-proof", not "deep".
//
// 2 is the smallest depth that covers the structure of loop(): it dispatches a
// write to a bounded goroutine and immediately reads the next frame, so the loop
// can legitimately hold request N+1's buffer while the handler for N still holds
// N's. That is readable in the code rather than inferred from a measurement. Any
// depth beyond 2 would be a guess, and each slot pins a whole frame (~1 MiB) per
// writable mount for the life of the stream.
//
// The full concurrency bound (16 writers + 16 readers + the one in hand) is
// deliberately NOT retained: that would pin ~17 MiB per mount, and a caller can
// hold many. third_party/yamux (batched.go) legislates the same hazard for its
// frame pool — a pathological size must not pin large buffers for a session's
// life. A genuine wide writeback burst falls through to the sync.Pool, which
// under sustained load is being hit between collections anyway.
//
// Caveat for whoever revisits this: the byte-ceiling test cannot tell these
// depths apart, because it drives writes synchronously and never has two
// requests in flight. Deciding this properly needs a pipelining client — the real
// kernel under cache=mmap writeback, which TestKernelMountModes exercises and
// the in-process harness structurally cannot.
type scratchList struct {
	free chan *[]byte // GC cannot empty this
	pool sync.Pool    // overflow beyond free; GC may drain it, which is fine
	size int
}

// blockScratchKeep is how many buffers a scratchList holds against the collector.
// See the type comment: 2 is what loop()'s structure needs, not a tuned value.
const blockScratchKeep = 2

func newScratchList(size, keep int) *scratchList {
	l := &scratchList{free: make(chan *[]byte, keep), size: size}
	l.pool.New = func() any { b := make([]byte, size); return &b }
	return l
}

// get returns a buffer of exactly the list's size. It never blocks: concurrency
// is bounded by the caller's semaphores, not by this list, so running out of
// retained buffers must degrade to allocation rather than to waiting.
func (l *scratchList) get() *[]byte {
	select {
	case bp := <-l.free:
		return bp
	default:
	}
	return l.pool.Get().(*[]byte)
}

// put returns a buffer to the list. A buffer of the wrong size is dropped rather
// than retained: a later get() must be able to assume the full size, since the
// whole point is reading a frame-sized request into it.
func (l *scratchList) put(bp *[]byte) {
	if bp == nil || cap(*bp) != l.size {
		return
	}
	*bp = (*bp)[:l.size]
	select {
	case l.free <- bp:
	default:
		l.pool.Put(bp)
	}
}
