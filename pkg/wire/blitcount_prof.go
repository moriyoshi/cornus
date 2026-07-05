//go:build blitprof

package wire

import (
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
func blitReadFull(k blitKind, r io.Reader, buf []byte) (int, error) {
	n, err := io.ReadFull(r, buf)
	add(k, n)
	return n, err
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

var blitNames = [numBlitKinds]string{"frame-stage", "wire-read", "msg-append", "user-copy"}
