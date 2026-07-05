//go:build !blitprof

package wire

import "io"

// Copy ("blit") accounting for the block protocol's data path, compiled OUT by
// default. Each function below is a thin wrapper that the compiler inlines back to
// the builtin it wraps, so a normal build carries no counter, no atomic, and no
// shared cache line.
//
// The accounting lives INSIDE the wrapper rather than beside each call, so a
// counted copy and its count cannot drift apart: there is no way to write the copy
// and forget the tally, and no way to change the length one is called with without
// changing the other. Call sites read as what they are — a copy, an append, a
// read — with the category as the first argument.
//
// Scope: the categories track BULK movement and framing — payload-sized copies,
// staged frame headers, and what lands off the wire. Scalar field encoding
// (msgW.u8/u16/u32/u64, a few bytes per message) is not counted, so a total here
// is "bytes moved in blocks", not "every byte the encoder touched".
//
// Build with -tags blitprof to turn it on; blitcount_prof.go then keeps a
// per-category byte counter that the mount benchmarks report. It is a build tag
// rather than an always-on atomic because the thing being measured is a hot path
// with up to 16 concurrent handlers per side: a global atomic add per copy would
// contend on one cache line and distort exactly the numbers it is there to
// produce.

// BlitProfiling reports whether copy accounting is compiled in.
const BlitProfiling = false

type blitKind int

const (
	// blitFrameStage: bytes copied while staging a frame's header + metadata (and
	// a small bulk body) into one buffer for a single Write.
	blitFrameStage blitKind = iota
	// blitWireRead: bytes read off a connection into a Go buffer. Not a memcpy this
	// package performs, but a copy it CHOOSES the destination of — the difference
	// between landing in a scratch buffer and landing in the caller's.
	blitWireRead
	// blitMsgAppend: bytes appended into a msgW payload.
	blitMsgAppend
	// blitUserCopy: bytes copied into or out of a buffer a caller supplied.
	blitUserCopy
	numBlitKinds
)

// blitCopy is copy() with accounting.
func blitCopy(_ blitKind, dst, src []byte) int { return copy(dst, src) }

// blitAppend is append(dst, src...) with accounting.
func blitAppend(_ blitKind, dst, src []byte) []byte { return append(dst, src...) }

// blitAppendString is append(dst, s...) with accounting.
func blitAppendString(_ blitKind, dst []byte, s string) []byte { return append(dst, s...) }

// blitReadFull is io.ReadFull with accounting for the bytes that landed.
func blitReadFull(_ blitKind, r io.Reader, buf []byte) (int, error) { return io.ReadFull(r, buf) }

// BlitStats returns per-category copied bytes since the last ResetBlits. Always
// zero unless built with -tags blitprof.
func BlitStats() [numBlitKinds]int64 { return [numBlitKinds]int64{} }

// ResetBlits zeroes the counters.
func ResetBlits() {}

// blitNames labels the categories for reporting.
var blitNames = [numBlitKinds]string{"frame-stage", "wire-read", "msg-append", "user-copy"}
