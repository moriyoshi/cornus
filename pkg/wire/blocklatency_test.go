package wire

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hugelgupf/p9/p9"
)

// TestBlockRTTOverWebsocketYamux measures the per-op round-trip latency of the
// block protocol over the REAL production transport (coder/websocket + yamux over
// loopback TCP), which the net.Pipe-based blockHarness cannot expose. The E2E
// async-write run showed ~30ms/op — a latency (not deadlock) problem. This probe
// reproduces it in-process so a fix can be iterated without the container.
func TestBlockRTTOverWebsocketYamux(t *testing.T) {
	if testing.Short() {
		t.Skip("latency probe")
	}
	dir := t.TempDir()

	// Caller endpoint (authoritative export) served in the WS handler.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := accept(w, r)
		if err != nil {
			return
		}
		_, stream, err := acceptTagged(sess)
		if err != nil {
			return
		}
		ServeBlockServer(stream, dir, 1<<20)
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	ctx := context.Background()
	csess, err := dial(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer csess.Close()
	stream, err := openTagged(csess, tagBlockFS)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := newBlockClient(stream, helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: blockMaxInflight})
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()

	// Attach root as handle 1.
	{
		var w msgW
		w.u64(1)
		if _, err := bc.do(ctx, opAttach, w.b); err != nil {
			t.Fatalf("attach: %v", err)
		}
	}

	getattr := func() {
		var w msgW
		w.u64(1)
		putAttrMask(&w, p9.AttrMask{Size: true, Mode: true})
		if _, err := bc.do(ctx, opGetAttr, w.b); err != nil {
			t.Fatalf("getattr: %v", err)
		}
	}

	// Warm up (TLS/ws/yamux window ramp).
	for i := 0; i < 5; i++ {
		getattr()
	}

	const N = 200
	start := time.Now()
	for i := 0; i < N; i++ {
		getattr()
	}
	perOp := time.Since(start) / N
	t.Logf("SERIAL block-proto RTT over ws+yamux: %v/op (%d ops)", perOp, N)

	// Pipelined: fire N concurrently to separate per-op latency from throughput.
	start = time.Now()
	done := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			var w msgW
			w.u64(1)
			putAttrMask(&w, p9.AttrMask{Size: true, Mode: true})
			_, e := bc.do(ctx, opGetAttr, w.b)
			done <- e
		}()
	}
	for i := 0; i < N; i++ {
		if e := <-done; e != nil {
			t.Fatalf("pipelined getattr: %v", e)
		}
	}
	t.Logf("PIPELINED block-proto over ws+yamux: %v total for %d ops (%v/op amortized)", time.Since(start), N, time.Since(start)/N)
}

// netPipeRTT is a baseline: the same op sequence over net.Pipe (what blockHarness
// uses) to contrast with the real transport.
func TestBlockRTTOverNetPipe(t *testing.T) {
	if testing.Short() {
		t.Skip("latency probe")
	}
	dir := t.TempDir()
	rs1, rs2 := net.Pipe()
	go ServeBlockServer(rs2, dir, 1<<20)
	ctx := context.Background()
	bc, err := newBlockClient(rs1, helloParams{version: blockProtoVersion, chunkSize: 1 << 20, maxInflight: blockMaxInflight})
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	{
		var w msgW
		w.u64(1)
		if _, err := bc.do(ctx, opAttach, w.b); err != nil {
			t.Fatalf("attach: %v", err)
		}
	}
	const N = 200
	start := time.Now()
	for i := 0; i < N; i++ {
		var w msgW
		w.u64(1)
		putAttrMask(&w, p9.AttrMask{Size: true, Mode: true})
		if _, err := bc.do(ctx, opGetAttr, w.b); err != nil {
			t.Fatalf("getattr: %v", err)
		}
	}
	t.Logf("SERIAL block-proto RTT over net.Pipe: %v/op (%d ops)", time.Since(start)/N, N)
}
