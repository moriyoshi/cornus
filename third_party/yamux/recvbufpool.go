// Modified by the Cornus project, 2026: this file is a cornus fork addition —
// a size-classed pool for per-stream receive buffers. See README.md.

package yamux

import (
	"bytes"
	"sync"
)

// A stream's receive buffer is allocated just-in-time at the first frame's length
// and grown by io.Copy whenever the consumer lags, so it reaches whatever is in
// flight — bounded only by the stream window, which cornus sets to 16 MiB. Two
// costs follow, and they were measured before this pool was written:
//
//   - The growth is a doubling ladder, paid ONCE PER STREAM. Holding the bytes
//     moved constant and varying the stream count, allocation went from 0.52x per
//     byte on one stream to 3.26x on 512 — a 6.3x spread at identical traffic.
//   - Nothing released the buffer. Shrink() exists for it and was never called, so
//     a stream kept its high-water mark for life: measured at 8 MiB after an 8 MiB
//     transfer. For a client-local mount the bulk direction is read replies
//     arriving at the cornus SERVER, so that is one such buffer per mount,
//     resident whether the mount is busy or not.
//
// Pooling fixes both at once, and it is what makes releasing the buffer on drain
// affordable: a stream that goes quiet gives its buffer back instead of pinning
// it, and a stream that fills again takes a grown buffer from the pool instead of
// climbing the ladder. bytes.Buffer.Reset keeps capacity, so the ladder is paid
// once per process rather than once per stream.
//
// THIS POOL IS DELIBERATELY GC-DRAINABLE, which is the opposite of the choice
// made for the hot per-request buffers elsewhere in cornus. There, a collection
// emptying the pool caused misses that allocated a megabyte and drove the next
// collection; here, the collector emptying the pool is precisely the mechanism
// that returns an idle mount's memory to the process. A retained tier would pin
// exactly the memory this is trying to release.
//
// Size classes are powers of two, and that is safe here for a reason it would not
// be for the frame buffers: recvBuf's size is set by TRAFFIC, not by a protocol
// constant. The send-side frame buffer is MaxDataFrame+headerSize — just over a
// power of two — where a power-of-two ladder would round up and double the pinned
// memory. Nothing rounds up here.
var recvBufClasses = [...]int{
	64 << 10, 128 << 10, 256 << 10, 512 << 10,
	1 << 20, 2 << 20, 4 << 20, 8 << 20, 16 << 20,
}

// recvBufPool is a value rather than package state so a test can use its own
// instance. Sharing one made the pool tests depend on whatever the rest of the
// suite's live sessions happened to be doing with it, and they failed about three
// runs in five for that reason alone.
type recvBufPool struct {
	pools [len(recvBufClasses)]sync.Pool
}

var defaultRecvBufPool recvBufPool

// getRecvBuf returns an empty buffer with capacity for at least n bytes. A buffer
// from the pool keeps whatever capacity it grew to, which is the point.
func (p *recvBufPool) get(n int) *bytes.Buffer {
	for i, size := range recvBufClasses {
		if n > size {
			continue
		}
		// Search UPWARD from the fitting class, not just in it. A stream asks by its
		// FIRST frame's length, which is small, while the buffer it eventually grew
		// was filed by its final capacity, which is large — so looking only in the
		// fitting class would leave every grown buffer stranded and make a stream
		// that goes quiet and busy again re-climb the whole ladder. That is the
		// thrash releasing on drain would otherwise cause, and it is what this loop
		// prevents. Handing a larger buffer to a small stream costs nothing new: the
		// pool only ever holds buffers some stream already grew, and this one gives
		// it back on its own next drain.
		for j := i; j < len(p.pools); j++ {
			if b, _ := p.pools[j].Get().(*bytes.Buffer); b != nil {
				b.Reset()
				return b
			}
		}
		return bytes.NewBuffer(make([]byte, 0, size))
	}
	// Larger than the largest class (a big custom MaxStreamWindowSize): allocate
	// exactly, and putRecvBuf will decline to keep it.
	return bytes.NewBuffer(make([]byte, 0, n))
}

// putRecvBuf returns a drained buffer to the pool. It files by the capacity the
// buffer actually has, rounding DOWN, so a later get for that class is always
// handed at least what it asked for. Buffers below the smallest class are dropped
// — they cost nothing to remake — and so are any above the largest, which must
// not be pinned.
func (p *recvBufPool) put(b *bytes.Buffer) {
	if b == nil {
		return
	}
	c := b.Cap()
	if c < recvBufClasses[0] || c > recvBufClasses[len(recvBufClasses)-1] {
		return
	}
	for i := len(recvBufClasses) - 1; i >= 0; i-- {
		if c >= recvBufClasses[i] {
			b.Reset()
			p.pools[i].Put(b)
			return
		}
	}
}

// getRecvBuf and putRecvBuf use the process-wide pool. The stream code goes
// through these; tests that need determinism construct their own recvBufPool.
func getRecvBuf(n int) *bytes.Buffer { return defaultRecvBufPool.get(n) }
func putRecvBuf(b *bytes.Buffer)     { defaultRecvBufPool.put(b) }
