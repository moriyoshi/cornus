// Copyright 2026 The Cornus Authors.
//
// Cornus fork addition: tests for DialOptions.WriteBufferSize.
//
// `!js` matches every sibling that touches the non-js Conn (conn.go, write.go,
// dial.go, export_test.go, conn_test.go). Without it this file joins the js/wasm
// build, where Conn is the ws_js.go variant and has none of these fields — and
// upstream's own TestWasm compiles the test binary for GOOS=js/GOARCH=wasm, so
// the breakage lands there rather than in any native build. TestWasm skips
// unless $CI is set (conn_test.go:447), which is why a green local `go test ./...`
// says nothing about it. Verify with:
//
//	GOOS=js GOARCH=wasm go vet ./...

//go:build !js

package websocket

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

// countingWriter records how many Write calls reach it and how big each was.
// Every one of these is a write syscall on a real connection, which is the whole
// quantity the option exists to reduce.
type countingWriter struct {
	writes []int
	total  int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, len(p))
	w.total += len(p)
	return len(p), nil
}

func (w *countingWriter) Read([]byte) (int, error) { return 0, io.EOF }
func (w *countingWriter) Close() error             { return nil }

// TestWriteBufferSizeControlsClientFlushes is the reason for the fork: a client
// masks and flushes in chunks bounded by its write buffer, so the buffer size —
// and nothing else, not the frame size, not the message size — decides how many
// writes reach the connection.
func TestWriteBufferSizeControlsClientFlushes(t *testing.T) {
	const payload = 128 << 10
	for _, bufSize := range []int{0 /* default 4096 */, 32 << 10, 132 << 10} {
		want := defaultWriteBufferSize
		if bufSize > 0 {
			want = bufSize
		}
		cw := &countingWriter{}
		bw := getBufioWriter(cw, bufSize)
		if got := bw.Size(); got != want {
			t.Fatalf("WriteBufferSize %d gave a %d-byte buffer, want %d", bufSize, got, want)
		}

		c := &Conn{client: true, bw: bw, rwc: cw}
		c.writeBuf = extractBufioWriterBuf(c.bw, c.rwc)
		c.writeHeader.masked = true
		c.writeHeader.maskKey = 0xDEADBEEF

		if _, err := c.writeFramePayload(bytes.Repeat([]byte{'x'}, payload)); err != nil {
			t.Fatalf("writeFramePayload: %v", err)
		}
		if err := c.bw.Flush(); err != nil {
			t.Fatal(err)
		}

		// The floor is what the buffer forces: a masked client cannot emit more per
		// write than its buffer holds. Asserting the FLOOR rather than an exact
		// count keeps this from breaking on unrelated changes to when bufio flushes.
		minWrites := payload / want
		if len(cw.writes) > minWrites+2 {
			t.Errorf("buffer %d: %d writes for %d bytes, expected about %d — the size "+
				"is not reaching the flush loop", want, len(cw.writes), payload, minWrites)
		}
		if cw.total != payload {
			t.Errorf("buffer %d: wrote %d bytes, want %d", want, cw.total, payload)
		}
		t.Logf("buffer %6d -> %4d writes for %d bytes", want, len(cw.writes), payload)
	}
}

// TestWriteBufferSizeDoesNotChangeTheBytes is the correctness half, and the
// reason the change is only fifteen lines: masking is chunk-size independent
// because writeFramePayload threads the rotated key across chunks. The bytes a
// peer sees must therefore be identical at any buffer size — if they were not,
// this option would be a protocol bug rather than a buffering knob.
func TestWriteBufferSizeDoesNotChangeTheBytes(t *testing.T) {
	const payload = 70<<10 + 3
	src := make([]byte, payload)
	for i := range src {
		src[i] = byte(i * 7)
	}

	// The sizes below are deliberately NOT all multiples of four. maskGo rotates
	// the key by len(chunk) % 4, so with only 4-aligned chunk sizes the rotation is
	// the identity and "carrying the key across chunks" is untestable — a version
	// that dropped the returned key would produce identical bytes and this test
	// would pass while proving nothing. It did, until the neutralization sweep
	// caught it.
	var golden []byte
	for _, bufSize := range []int{0, 1023, 4096, 4099, 33 << 10, 70001, 128 << 10} {
		var out bytes.Buffer
		bw := getBufioWriter(&out, bufSize)
		c := &Conn{client: true, bw: bw, rwc: &countingWriter{}}
		c.writeBuf = extractBufioWriterBuf(c.bw, c.rwc)
		// Reset the destination: extractBufioWriterBuf writes a probe byte.
		out.Reset()
		c.bw.Reset(&out)
		c.writeHeader.masked = true
		c.writeHeader.maskKey = 0x12345678

		if _, err := c.writeFramePayload(src); err != nil {
			t.Fatalf("buffer %d: %v", bufSize, err)
		}
		if err := c.bw.Flush(); err != nil {
			t.Fatal(err)
		}
		if golden == nil {
			golden = append([]byte(nil), out.Bytes()...)
			continue
		}
		if !bytes.Equal(golden, out.Bytes()) {
			t.Fatalf("buffer %d produced different masked bytes than the 4096-byte "+
				"default; the mask key is not being carried across chunks correctly",
				bufSize)
		}
	}
}

// TestCustomWriteBufferNeverEntersTheSharedPool guards the one way this change
// could corrupt an unrelated connection: the pool is not size-segregated, so a
// custom-sized buffer entering it would be handed to the next connection that
// asked for a default one.
func TestCustomWriteBufferNeverEntersTheSharedPool(t *testing.T) {
	const custom = 64 << 10
	var sink bytes.Buffer

	// Drain whatever the pool happens to hold, so the assertion below is about
	// what THIS test put there.
	for i := 0; i < 8; i++ {
		bufioWriterPool.Get()
	}

	big := getBufioWriter(&sink, custom)
	if big.Size() != custom {
		t.Fatalf("got a %d-byte buffer, want %d", big.Size(), custom)
	}
	putBufioWriter(big)

	for i := 0; i < 8; i++ {
		got, ok := bufioWriterPool.Get().(*bufio.Writer)
		if !ok {
			break
		}
		if got.Size() != defaultWriteBufferSize {
			t.Fatalf("the shared pool handed out a %d-byte buffer; a custom-sized writer "+
				"entered it and the next default connection would inherit it", got.Size())
		}
	}
}
