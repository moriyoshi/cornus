package wire

import (
	"encoding/binary"
	"io"
	"testing"
)

// How much does rewriting ownership in the frame stream cost, versus the blind
// splice it replaces?
//
// This is the number that decides whether id mapping on the raw 9P path is worth
// having at all: the alternative — terminating 9P in the block proxy — measured at
// ~12-15% throughput and ~20% fsync latency on the real mount. If the splice
// rewrite were anywhere near that, it would not be an improvement.
//
// The stream is shaped like a real read-heavy mount: bulk Rread frames near the
// 1 MiB msize, with an Rgetattr interleaved (a workload stats far less often than
// it reads).

func ninepFrames(nread, readLen, ngetattr int) []byte {
	var out []byte
	frame := func(typ byte, body int) {
		size := ninepHeaderLen + body
		hdr := make([]byte, ninepHeaderLen)
		binary.LittleEndian.PutUint32(hdr, uint32(size))
		hdr[4] = typ
		out = append(out, hdr...)
		out = append(out, make([]byte, body)...)
	}
	for i := 0; i < nread; i++ {
		frame(117, readLen) // Rread
		if ngetattr > 0 && i%(nread/ngetattr+1) == 0 {
			frame(msgRgetattr, rgetattrMinLen-ninepHeaderLen+120)
		}
	}
	return out
}

type repeatReader struct {
	buf []byte
	off int
	n   int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	if r.off >= len(r.buf) {
		r.off = 0
		r.n--
	}
	return n, nil
}

func benchStream(b *testing.B, mapped bool) {
	frames := ninepFrames(64, 1<<20-ninepHeaderLen, 8)
	b.SetBytes(int64(len(frames)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &repeatReader{buf: frames, n: 1}
		if mapped {
			_ = mapOwnerStream(io.Discard, src, 1001, 1001)
		} else {
			_, _ = io.Copy(io.Discard, src)
		}
	}
}

func BenchmarkNinePBlindSplice(b *testing.B)   { benchStream(b, false) }
func BenchmarkNinePMappingSplice(b *testing.B) { benchStream(b, true) }
