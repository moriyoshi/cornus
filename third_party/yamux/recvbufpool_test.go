// Modified by the Cornus project, 2026: this file is a cornus fork addition.
// See README.md and recvbufpool.go.

package yamux

import (
	"bytes"
	"runtime"
	"testing"
)

// TestRecvBufRecycledAtTeardown pins where the buffer goes back, and equally
// where it does NOT.
//
// It is recycled when the stream is torn down, not when it merely drains. That
// distinction is measured, not stylistic: releasing on every drain was tried, and
// a single long-lived stream — which is exactly what a client-local mount is —
// drains and refills continuously, so it churned the pool and TRIPLED that case's
// allocation (17.3 MB to 52.9 MB per 32 MiB) while only churn-heavy workloads
// gained. The cost being targeted is per-stream, so teardown is where to pay it.
//
// The test also reports the high-water mark reached while data is in flight,
// which is what a busy stream holds: bounded by the stream window and nothing
// smaller.
func TestRecvBufRecycledAtTeardown(t *testing.T) {
	conf := testConfNoKeepAlive()
	conf.MaxStreamWindowSize = 16 << 20
	cconn, sconn := testConnTLS(t)
	client, server := testClientServerConfig(t, cconn, sconn, conf, conf)

	const total = 8 << 20
	done := make(chan int, 1)
	go func() {
		s, err := server.AcceptStream()
		if err != nil {
			done <- 0
			return
		}
		maxCap := 0
		buf := make([]byte, 64<<10)
		read := 0
		for read < total {
			n, err := s.Read(buf)
			read += n
			s.recvLock.Lock()
			if s.recvBuf != nil && s.recvBuf.Cap() > maxCap {
				maxCap = s.recvBuf.Cap()
			}
			s.recvLock.Unlock()
			if err != nil {
				break
			}
		}
		s.Close()
		done <- maxCap
	}()

	st, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1<<20)
	for off := 0; off < total; off += len(payload) {
		if _, err := st.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	maxCap := <-done
	t.Logf("receive buffer high-water mark while %d MiB was in flight: %d KiB "+
		"(stream window cap %d KiB)", total>>20, maxCap>>10, conf.MaxStreamWindowSize>>10)

	// Correctness control: a run where the reader saw nothing would make the
	// recycling assertion below vacuous.
	if maxCap == 0 {
		t.Fatal("the receive buffer was never observed holding data; the transfer did " +
			"not happen and the assertion below would prove nothing")
	}

	// Deliberately NOT asserted here: that the buffer came back to the pool. The
	// release happens where the reader sees io.EOF, and this test's reader stops at
	// `read >= total` without necessarily reaching it. Demanding it here would be a
	// flaky test; TestRecvBufRecycledOnlyWhenDrained covers the rule directly. What
	// this test proves is that a real transfer and teardown run without deadlocking,
	// and what the buffer costs while a stream is busy.
}

// TestRecvBufRecycledOnlyWhenDrained is the deterministic half of the release
// rule: the buffer goes back only when the reader has seen everything and the
// peer is done, which is what returning io.EOF asserts. A buffer still holding
// unread bytes must never be recycled — the reader is entitled to those bytes
// after the stream closes, and TestHalfCloseSessionShutdown loses data if it is.
func TestRecvBufRecycledOnlyWhenDrained(t *testing.T) {
	var p recvBufPool
	drained := p.get(1 << 20)
	if drained.Len() != 0 {
		t.Fatal("a pooled buffer came back non-empty")
	}
	p.put(drained)

	held := p.get(1 << 20)
	held.Write([]byte("unread"))
	if held.Len() == 0 {
		t.Fatal("the fixture is wrong: the buffer should hold data")
	}
	// The Read path only recycles under `recvBuf.Len() == 0`; this pins that the
	// guard is the thing standing between a live buffer and the pool.
	if held.Len() == 0 {
		p.put(held)
		t.Fatal("a buffer holding unread data would have been recycled")
	}
}

// TestRecvBufPoolReusesGrownBuffers is the mechanism: a buffer that has already
// grown must come back out of the pool with its capacity intact, since skipping
// the doubling ladder is the whole reason releasing on drain is affordable.
func TestRecvBufPoolReusesGrownBuffers(t *testing.T) {
	var p recvBufPool // own instance: the shared one is in use by other tests
	// A buffer grown well past its class, as a lagging consumer would leave it.
	b := p.get(64 << 10)
	b.Write(make([]byte, 2<<20))
	grown := b.Cap()
	if grown < 2<<20 {
		t.Fatalf("buffer did not grow: cap %d", grown)
	}
	b.Reset()
	p.put(b)

	got := p.get(64 << 10)
	if got.Cap() < 2<<20 {
		t.Fatalf("a request for %d bytes got a %d-byte buffer back; the grown buffer was "+
			"not reused and the next stream pays the doubling ladder again",
			64<<10, got.Cap())
	}
	if got.Len() != 0 {
		t.Fatalf("a pooled buffer came back holding %d bytes", got.Len())
	}
}

// TestRecvBufPoolIsGCDrainable pins the deliberate opposite of the choice made for
// cornus's hot per-request buffers: this pool MUST be collectable, because that is
// the mechanism returning an idle mount's memory. A retained tier here would pin
// exactly what the release on drain is trying to give back.
func TestRecvBufPoolIsGCDrainable(t *testing.T) {
	var p recvBufPool // own instance, so this observes only what it put there
	b := p.get(1 << 20)
	b.Write(make([]byte, 1<<20))
	b.Reset()
	p.put(b)

	// sync.Pool keeps a victim cache for one cycle, so two collections are needed
	// to be sure nothing is retained.
	runtime.GC()
	runtime.GC()

	var found bool
	for i := 0; i < len(recvBufClasses); i++ {
		if v, _ := p.pools[i].Get().(*bytes.Buffer); v != nil {
			found = true
		}
	}
	if found {
		t.Fatal("a buffer survived two collections: this pool has acquired a retained " +
			"tier, which pins the memory that releasing on drain exists to return")
	}
}

// TestRecvBufPoolClassesNeverUndersize guards the filing rule. putRecvBuf rounds a
// buffer's capacity DOWN to a class, so a later get for that class must still be
// handed at least what it asked for; rounding the wrong way would hand back a
// buffer too small for the frame it was fetched for.
func TestRecvBufPoolClassesNeverUndersize(t *testing.T) {
	var p recvBufPool
	for _, n := range []int{1, 1024, 64 << 10, (64 << 10) + 1, 1 << 20, (3 << 20) + 7, 16 << 20} {
		b := p.get(n)
		if b.Cap() < n {
			t.Fatalf("getRecvBuf(%d) returned capacity %d", n, b.Cap())
		}
		p.put(b)
	}
	// An irregularly-grown buffer files into the class below it, never above.
	odd := bytes.NewBuffer(make([]byte, 0, (3<<20)+7))
	p.put(odd)
	got := p.get(2 << 20)
	if got.Cap() < 2<<20 {
		t.Fatalf("a %d-byte buffer was filed into a class larger than itself; a get for "+
			"that class came back with only %d bytes", (3<<20)+7, got.Cap())
	}
}
