// Copyright 2026 The Cornus Authors.
//
// Cornus fork addition: tests for serving Twrite payloads from the receiving
// connection's buffer pool (see twrite.allocPayload).

package p9

import (
	"bytes"
	"io"
	"runtime"
	"sync"
	"testing"

	"github.com/hugelgupf/socketpair"
	"github.com/u-root/uio/ulog/ulogtest"
)

// newTestConnState builds the part of a connState that a completed Tversion
// would have built: the msize-shaped buffer pool allocPayload borrows from, and
// the zero buffer whose length doubles as the negotiated-size marker.
func newTestConnState(msize uint32) *connState {
	cs := &connState{}
	cs.readBufPool = sync.Pool{New: func() any { b := make([]byte, msize); return &b }}
	cs.pristineZeros = make([]byte, msize)
	return cs
}

// recvOne runs one recv against a connection and returns the decoded message.
func recvOneTwrite(t *testing.T, l ulogtest.Logger, cs *connState, r io.ReadWriter) *twrite {
	t.Helper()
	_, m, err := recv(l, r, maximumLength, cs.lookup)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	w, ok := m.(*twrite)
	if !ok {
		t.Fatalf("recv returned %T, want *twrite", m)
	}
	return w
}

// TestTwritePayloadIsExactlyTheBytesSent is the correctness net under payload
// reuse: whatever a Twrite carried is what the message exposes, with nothing left
// over from the request that used the buffer before it.
//
// The sizes descend deliberately. A large write followed by a small one is the
// shape that exposes a reused buffer: if the payload were handed out with the
// previous request's length or capacity, the small write would carry a tail of
// the large one's bytes. Each request uses a distinct filler byte so such a tail
// is identifiable rather than merely wrong.
func TestTwritePayloadIsExactlyTheBytesSent(t *testing.T) {
	server, client, err := socketpair.TCPPair()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer client.Close()

	l := ulogtest.Logger{TB: t}
	// Stand in for a completed Tversion: the pool and its size marker are what
	// allocPayload keys on (see the tversion handler).
	const msize = 64 << 10
	cs := newTestConnState(msize)

	sizes := []int{msize - 100, 4096, 1, 8192, 3, msize - 100, 17}
	go func() {
		for i, n := range sizes {
			payload := bytes.Repeat([]byte{byte('A' + i)}, n)
			if err := send(l, client, tag(i), &twrite{fid: 1, Offset: uint64(i), Data: payload}); err != nil {
				t.Errorf("send %d: %v", i, err)
				return
			}
		}
	}()

	for i, n := range sizes {
		w := recvOneTwrite(t, l, cs, server)
		want := bytes.Repeat([]byte{byte('A' + i)}, n)
		if len(w.Data) != n {
			t.Fatalf("request %d: payload is %d bytes, want %d — a reused buffer is being "+
				"handed out with the wrong length", i, len(w.Data), n)
		}
		if !bytes.Equal(w.Data, want) {
			t.Fatalf("request %d (%d bytes of %q): payload does not match what was sent; "+
				"bytes from an earlier request are leaking through the reused buffer",
				i, n, string(rune('A'+i)))
		}
		// The payload must not be reslicable past its own length: a three-index
		// slice is what stops a File implementation reaching bytes an earlier
		// request left behind.
		if cap(w.Data) != len(w.Data) {
			t.Fatalf("request %d: payload cap %d != len %d — the bound that keeps an "+
				"earlier request's bytes unreachable is missing", i, cap(w.Data), len(w.Data))
		}
		msgDotLRegistry.put(w)
	}
}

// TestTwritePayloadReleaseDropsConnection pins the property that makes reuse safe
// across connections: the message cache is process-global, so a message returning
// to it must carry nothing connection-scoped. If it kept its connState, a Twrite
// cached from one connection would hand the NEXT connection a buffer from the
// first one's pool.
func TestTwritePayloadReleaseDropsConnection(t *testing.T) {
	cs := newTestConnState(64 << 10)
	w := &twrite{cs: cs}
	w.Data = w.allocPayload(1024)
	if w.payloadBuf == nil {
		t.Fatal("allocPayload did not borrow from the connection pool")
	}
	msgDotLRegistry.put(w)
	if w.cs != nil {
		t.Fatal("a Twrite returned to the process-global cache still references the " +
			"connection it was received on; the next connection would draw buffers " +
			"from the wrong pool")
	}
	if w.payloadBuf != nil || w.Data != nil {
		t.Fatal("a Twrite returned to the cache still references its payload buffer")
	}
}

// TestTwritePayloadDoesNotAllocatePerMessage is the reason the fork exists. It
// asserts BYTES rather than allocation counts: removing the reuse changes the
// count by one per message, which hides inside variance, and changes the bytes by
// a whole payload, which does not.
func TestTwritePayloadDoesNotAllocatePerMessage(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation allocates 200-365 KB/op here, which swamps the " +
			"figure being measured; see race_off_test.go")
	}
	server, client, err := socketpair.TCPPair()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer client.Close()

	l := ulogtest.Logger{TB: t}
	const msize = 1 << 20
	cs := newTestConnState(msize)

	const (
		size = msize - 1024
		ops  = 32
	)
	payload := bytes.Repeat([]byte{0xA5}, size)
	go func() {
		for i := 0; i < ops+1; i++ {
			if err := send(l, client, tag(i), &twrite{fid: 1, Data: payload}); err != nil {
				t.Errorf("send %d: %v", i, err)
				return
			}
		}
	}()

	// One warm-up so the pool is populated before measuring.
	msgDotLRegistry.put(recvOneTwrite(t, l, cs, server))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := 0
	for i := 0; i < ops; i++ {
		w := recvOneTwrite(t, l, cs, server)
		got += len(w.Data)
		msgDotLRegistry.put(w)
	}
	runtime.ReadMemStats(&after)

	// Positive control: the payloads actually arrived. A recv that failed early
	// allocates nothing and would satisfy any ceiling.
	if want := ops * size; got != want {
		t.Fatalf("received %d payload bytes, want %d — the measurement below describes "+
			"work that did not happen", got, want)
	}

	perOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(ops)
	t.Logf("Twrite recv: %.0f bytes allocated per %d-byte payload", perOp, size)
	if ceiling := float64(size / 4); perOp > ceiling {
		t.Fatalf("recv allocated %.0f bytes per %d-byte Twrite (ceiling %.0f). That is "+
			"payload-scale, which means every message allocates a fresh payload buffer "+
			"instead of borrowing one from the connection's pool", perOp, size, ceiling)
	}
}

// TestReadBufHotSlotSurvivesGC is the MECHANISM test for the GC-proof cache in
// front of readBufPool, and the two runtime.GC() calls are the whole assertion.
//
// The allocation ceiling above cannot see this: it runs with a warm pool and one
// buffer in flight, so it passes with or without the hot slot. What distinguishes
// them is only observable across a collection — a sync.Pool is emptied by every
// one, and that is the defect the slot exists to fix. Asserting on the BACKING
// ARRAY rather than on capacity is deliberate: a fresh buffer of the right size
// would satisfy any size check.
func TestReadBufHotSlotSurvivesGC(t *testing.T) {
	const msize = 1 << 20
	cs := newTestConnState(msize)

	bp := cs.getReadBuf()
	if len(*bp) != msize {
		t.Fatalf("got a %d-byte buffer, want %d", len(*bp), msize)
	}
	(*bp)[0] = 0x5A
	cs.putReadBuf(bp)

	runtime.GC()
	runtime.GC()

	got := cs.getReadBuf()
	if (*got)[0] != 0x5A {
		t.Fatal("the buffer returned after two collections is not the one that went " +
			"back (the marker byte is gone): the reuse is GC-drainable, which is the " +
			"exact failure the hot slot exists to fix")
	}

	// And the slot must be bounded: a second buffer put back has nowhere to go but
	// the pool, so the connection pins one msize buffer, not an unbounded number.
	a, b := cs.getReadBuf(), cs.getReadBuf()
	cs.putReadBuf(a)
	cs.putReadBuf(b)
	if cs.readBufHot.Load() == nil {
		t.Fatal("the hot slot is empty after two puts")
	}
	if cs.readBufHot.Load() != a {
		t.Fatal("the hot slot took the second buffer as well; it must hold exactly one")
	}
}
