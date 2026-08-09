//go:build blitprof

package wire

import (
	"bufio"
	"io"
	"sync/atomic"
)

// Copy ("blit") accounting, compiled in by -tags blitprof. See blitcount.go for
// the wrapper contract and for why this is a build tag rather than an always-on
// counter. Every function here must stay signature-identical to its counterpart
// there, and must count exactly the bytes its builtin moved — never the length it
// was asked for, which for copy() and ReadFull differ precisely in the cases worth
// noticing.

// BlitProfiling reports whether copy accounting is compiled in.
const BlitProfiling = true

type blitKind int

const (
	blitFrameStage blitKind = iota
	blitWireRead
	blitMsgAppend
	blitUserCopy
	blitBufCopy
	numBlitKinds
)

var blitCounters [numBlitKinds]atomic.Int64

func add(k blitKind, n int) {
	if n > 0 {
		blitCounters[k].Add(int64(n))
	}
}

// blitCopy is copy() with accounting.
func blitCopy(k blitKind, dst, src []byte) int {
	n := copy(dst, src)
	add(k, n)
	return n
}

// blitAppend is append(dst, src...) with accounting.
func blitAppend(k blitKind, dst, src []byte) []byte {
	add(k, len(src))
	return append(dst, src...)
}

// blitAppendString is append(dst, s...) with accounting.
func blitAppendString(k blitKind, dst []byte, s string) []byte {
	add(k, len(s))
	return append(dst, s...)
}

// blitReadFull is io.ReadFull with accounting for the bytes that landed.
//
// When the reader is buffered it additionally attributes the SUBSET of those
// bytes that passed through the receive buffer rather than landing directly,
// using bufio's own bypass rule verbatim, so the figure is exact rather than
// estimated. Without this the cost of a receive buffer is invisible: the total
// that lands is the same either way, and a buffer sized at or above a frame would
// silently stage every payload byte.
func blitReadFull(k blitKind, r io.Reader, buf []byte) (int, error) {
	br, buffered := r.(*bufio.Reader)
	if !buffered {
		n, err := io.ReadFull(r, buf)
		add(k, n)
		return n, err
	}
	total := 0
	for total < len(buf) {
		rest := buf[total:]
		staged := !(br.Buffered() == 0 && len(rest) >= br.Size())
		n, err := br.Read(rest)
		total += n
		add(k, n)
		if staged {
			add(blitBufCopy, n)
		}
		if err != nil {
			// Match io.ReadFull's contract exactly; this build must not differ.
			if err == io.EOF && total > 0 {
				err = io.ErrUnexpectedEOF
			}
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

// BlitStats returns per-category copied bytes since the last ResetBlits.
func BlitStats() [numBlitKinds]int64 {
	var out [numBlitKinds]int64
	for i := range blitCounters {
		out[i] = blitCounters[i].Load()
	}
	return out
}

// ResetBlits zeroes the counters.
func ResetBlits() {
	for i := range blitCounters {
		blitCounters[i].Store(0)
	}
}

var blitNames = [numBlitKinds]string{"frame-stage", "wire-read", "msg-append", "user-copy", "buf-copy"}
