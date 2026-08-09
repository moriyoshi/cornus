package wire

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/hashicorp/yamux"
)

// Does yamux's per-stream receive buffer amortise?
//
// `bytes.growSlice` under `yamux.(*Stream).readData` is the largest single
// allocation source left on the mount path, but the mount benchmark opens a fresh
// stream per iteration, which is the one shape that cannot amortise anything. The
// buffer is allocated just-in-time at the first frame's length and grown by
// io.Copy when the consumer lags; Shrink() would free it and nothing in cornus
// calls it, so it grows ONCE PER STREAM and then stays.
//
// If that reading is right, the cost is a function of how many streams carry the
// bytes, not of how many bytes there are — so holding the total constant and
// varying the stream count separates "a real per-byte cost" from "a per-stream
// cost the mount amortises away". A client-local mount holds ONE stream for its
// whole life; builds, exec sessions and credential fetches open many short ones.
//
//	go test ./pkg/wire/ -run XXX -bench '^BenchmarkYamuxStreamChurn$' -benchtime 3x
func BenchmarkYamuxStreamChurn(b *testing.B) {
	const total = 32 << 20
	for _, streams := range []int{1, 8, 64, 512} {
		perStream := total / streams
		b.Run(fmt.Sprintf("streams=%d/%dKiB-each", streams, perStream>>10), func(b *testing.B) {
			b.SetBytes(total)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				yamuxTransfer(b, streams, perStream)
			}
			b.StopTimer()
			// Allocation per BYTE is the comparable number across rows: the rows move
			// the same total, so a per-stream cost shows up as this rising with the
			// stream count while a per-byte cost stays flat.
			b.ReportMetric(float64(streams), "streams")
		})
	}
}

// yamuxTransfer moves perStream bytes over each of `streams` streams of one
// session, in 1 MiB writes to match the block protocol's frame size.
func yamuxTransfer(tb testing.TB, streams, perStream int) {
	tb.Helper()
	a, c := sockPair(tb)
	srv, err := yamux.Server(a, yamuxConfig())
	if err != nil {
		tb.Fatal(err)
	}
	defer srv.Close()
	cli, err := yamux.Client(c, yamuxConfig())
	if err != nil {
		tb.Fatal(err)
	}
	defer cli.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var inner sync.WaitGroup
		for i := 0; i < streams; i++ {
			s, err := cli.Accept()
			if err != nil {
				return
			}
			inner.Add(1)
			go func(s net.Conn) {
				defer inner.Done()
				defer s.Close()
				_, _ = io.Copy(io.Discard, s)
			}(s)
		}
		inner.Wait()
	}()

	buf := make([]byte, 1<<20)
	for i := 0; i < streams; i++ {
		s, err := srv.Open()
		if err != nil {
			tb.Fatal(err)
		}
		for off := 0; off < perStream; {
			n := min(len(buf), perStream-off)
			if _, err := s.Write(buf[:n]); err != nil {
				tb.Fatal(err)
			}
			off += n
		}
		s.Close()
	}
	wg.Wait()
}
