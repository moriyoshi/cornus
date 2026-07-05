package wire

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hugelgupf/p9/linux"
)

func TestBlockFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := []frame{
		{op: opGetAttr, flags: flagFinal, reqID: 1, payload: []byte("hello")},
		{op: opRead, reqID: 2, payload: bytes.Repeat([]byte{7}, 4096)},
		{op: opWrite, flags: flagErr, reqID: 3, payload: []byte{0, 0, 0, 13}},
		{op: opClunk, flags: flagFinal | flagEOF, reqID: 4},
	}
	for _, f := range in {
		if err := writeFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range in {
		got, err := readFrame(&buf, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if got.op != want.op || got.flags != want.flags || got.reqID != want.reqID || !bytes.Equal(got.payload, want.payload) {
			t.Fatalf("frame mismatch: got %+v want %+v", got, want)
		}
	}
}

func TestMsgCursorRoundTrip(t *testing.T) {
	var w msgW
	w.u8(0xAB)
	w.u16(0x1234)
	w.u32(0xDEADBEEF)
	w.u64(0x0102030405060708)
	w.i64(-42)
	w.boolean(true)
	w.str("path/to/file")
	w.blob([]byte{1, 2, 3})

	r := newMsgR(w.b)
	if r.u8() != 0xAB || r.u16() != 0x1234 || r.u32() != 0xDEADBEEF || r.u64() != 0x0102030405060708 {
		t.Fatal("int fields mismatch")
	}
	if r.i64() != -42 || !r.boolean() || r.str() != "path/to/file" || !bytes.Equal(r.blob(), []byte{1, 2, 3}) {
		t.Fatal("tail fields mismatch")
	}
	if r.error() != nil {
		t.Fatalf("cursor error: %v", r.error())
	}
	// Underflow sets an error rather than panicking.
	r2 := newMsgR([]byte{1, 2})
	_ = r2.u32()
	if r2.error() == nil {
		t.Fatal("expected underflow error")
	}
}

func TestHelloMarshal(t *testing.T) {
	h := helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: 64, features: 0}
	got, err := unmarshalHello(h.marshal())
	if err != nil || got != h {
		t.Fatalf("hello round trip: got %+v err %v", got, err)
	}
}

func TestErrnoRoundTrip(t *testing.T) {
	for _, e := range []linux.Errno{linux.EACCES, linux.ENOENT, linux.EROFS, linux.ENODATA} {
		wrapped := errnoError(uint32(e))
		if errnoOf(wrapped) != uint32(e) {
			t.Fatalf("errno %d did not round-trip (got %d)", e, errnoOf(wrapped))
		}
	}
	if errnoOf(nil) != 0 {
		t.Fatal("nil error should map to errno 0")
	}
}

// mockBlockServer runs a minimal caller-side responder over conn for the mux
// test: it does the handshake (read peer HELLO, then send ours), then serves
// requests with handle. Responses are serialized under a send lock.
func mockBlockServer(t *testing.T, conn net.Conn, handle func(f frame) frame) {
	t.Helper()
	maxFrame := (1 << 20) + blockFrameSlack
	req, err := readFrame(conn, maxFrame)
	if err != nil || req.op != opHello {
		return
	}
	local := helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: 64}
	if err := writeFrame(conn, frame{op: opHello, flags: flagFinal, payload: local.marshal()}); err != nil {
		return
	}
	var sendMu sync.Mutex
	for {
		f, err := readFrame(conn, maxFrame)
		if err != nil {
			return
		}
		resp := handle(f)
		resp.reqID = f.reqID
		sendMu.Lock()
		_ = writeFrame(conn, resp)
		sendMu.Unlock()
	}
}

func TestBlockClientOutOfOrderAndError(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	// The server echoes GETATTR payloads and returns EROFS for WRITE.
	go mockBlockServer(t, c2, func(f frame) frame {
		switch f.op {
		case opWrite:
			var w msgW
			w.u32(uint32(linux.EROFS))
			return frame{op: f.op, flags: flagFinal | flagErr, payload: w.b}
		default:
			return frame{op: f.op, flags: flagFinal, payload: append([]byte("echo:"), f.payload...)}
		}
	})

	bc, err := newBlockClient(c1, helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()

	// Two concurrent requests: routing must deliver each its own response.
	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	var gotA, gotB []frame
	go func() { defer wg.Done(); gotA, errA = bc.do(context.Background(), opGetAttr, []byte("A")) }()
	go func() { defer wg.Done(); gotB, errB = bc.do(context.Background(), opGetAttr, []byte("B")) }()
	wg.Wait()
	if errA != nil || len(gotA) != 1 || !bytes.Equal(gotA[0].payload, []byte("echo:A")) {
		t.Fatalf("request A: err=%v frames=%v", errA, gotA)
	}
	if errB != nil || len(gotB) != 1 || !bytes.Equal(gotB[0].payload, []byte("echo:B")) {
		t.Fatalf("request B: err=%v frames=%v", errB, gotB)
	}

	// An ERR frame surfaces as the precise errno.
	_, err = bc.do(context.Background(), opWrite, nil)
	if !errors.Is(err, linux.EROFS) {
		t.Fatalf("expected EROFS, got %v", err)
	}
}

func TestBlockClientFailsOnClose(t *testing.T) {
	c1, c2 := net.Pipe()
	go mockBlockServer(t, c2, func(f frame) frame {
		// Never respond to data ops, so the client is waiting when the conn drops.
		select {}
	})
	bc, err := newBlockClient(c1, helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: 64})
	if err != nil {
		t.Fatal(err)
	}
	go func() { c2.Close(); c1.Close() }()
	if _, err := bc.do(context.Background(), opGetAttr, nil); err == nil {
		t.Fatal("expected error after connection close")
	}
}

// TestBlockClientCancellation proves a parked request releases on ctx cancel, its
// waiter is drained (no leak), a late response for the abandoned reqID is dropped,
// and the client keeps serving subsequent requests.
func TestBlockClientCancellation(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()

	reqReceived := make(chan struct{})
	release := make(chan struct{})
	go func() {
		maxFrame := (1 << 20) + blockFrameSlack
		if req, err := readFrame(c2, maxFrame); err != nil || req.op != opHello {
			return
		}
		local := helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: 64}
		if err := writeFrame(c2, frame{op: opHello, flags: flagFinal, payload: local.marshal()}); err != nil {
			return
		}
		req1, err := readFrame(c2, maxFrame) // the request that will be cancelled
		if err != nil {
			return
		}
		close(reqReceived)
		<-release
		// A late response for the abandoned reqID: the client must drop it.
		_ = writeFrame(c2, frame{op: req1.op, flags: flagFinal, reqID: req1.reqID, payload: []byte("late")})
		// A second request must still be served normally.
		req2, err := readFrame(c2, maxFrame)
		if err != nil {
			return
		}
		_ = writeFrame(c2, frame{op: req2.op, flags: flagFinal, reqID: req2.reqID, payload: []byte("ok2")})
	}()

	bc, err := newBlockClient(c1, helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := bc.do(ctx, opGetAttr, nil); done <- e }()
	<-reqReceived // the caller has the request; do is parked
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", e)
	}
	bc.mu.Lock()
	n := len(bc.pending)
	bc.mu.Unlock()
	if n != 0 {
		t.Fatalf("pending waiters not drained after cancel: %d", n)
	}

	// The late frame for the abandoned reqID is dropped; a fresh request still works.
	close(release)
	frames, err := bc.do(context.Background(), opGetAttr, []byte("2"))
	if err != nil || len(frames) != 1 || !bytes.Equal(frames[0].payload, []byte("ok2")) {
		t.Fatalf("client unusable after dropping a late frame: err=%v frames=%v", err, frames)
	}
}

func TestCancelConnTripsOnce(t *testing.T) {
	c1, c2 := net.Pipe()
	var tripped atomic.Int32
	cc := newCancelConn(c1, func() { tripped.Add(1) })
	c2.Close()
	buf := make([]byte, 4)
	if _, err := cc.Read(buf); err == nil {
		t.Fatal("expected read error after peer close")
	}
	_, _ = cc.Read(buf) // a second read error must not trip again
	_ = cc.Close()      // nor Close
	if got := tripped.Load(); got != 1 {
		t.Fatalf("onGone tripped %d times, want exactly 1", got)
	}
}
