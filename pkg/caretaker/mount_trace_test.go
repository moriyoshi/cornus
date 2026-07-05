package caretaker

import (
	"context"
	"io"
	"net"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestTraceMountBytes verifies traceMountBytes stamps the caretaker.mount
// span, on finish, with the stream's byte totals: bytes read from the server
// (delivered into the pod) as rx and bytes written to it as tx. Hermetic.
func TestTraceMountBytes(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	_, span := tp.Tracer("test").Start(context.Background(), "caretaker.mount")

	podSide, serverSide := net.Pipe()
	defer podSide.Close()
	defer serverSide.Close()
	traced, finish := traceMountBytes(span, podSide)
	if traced == podSide {
		t.Fatal("recording span must wrap the stream to count bytes")
	}

	const in = "server-to-pod-bytes" // read by the pod = rx
	const out = "pod-req"            // written to the server = tx
	// net.Pipe is synchronous: the server side must consume the pod's request
	// before writing its response, or both writers deadlock.
	go func() {
		buf := make([]byte, len(out))
		_, _ = io.ReadFull(serverSide, buf)
		_, _ = io.WriteString(serverSide, in)
		serverSide.Close()
	}()
	if _, err := io.WriteString(traced, out); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := io.ReadAll(traced)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != in {
		t.Fatalf("payload = %q, want %q", got, in)
	}

	finish()
	span.End()

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	attrs := map[string]attribute.Value{}
	for _, kv := range spans[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value
	}
	if rx := attrs["caretaker.mount.bytes.rx"].AsInt64(); rx != int64(len(in)) {
		t.Errorf("caretaker.mount.bytes.rx = %d, want %d", rx, len(in))
	}
	if tx := attrs["caretaker.mount.bytes.tx"].AsInt64(); tx != int64(len(out)) {
		t.Errorf("caretaker.mount.bytes.tx = %d, want %d", tx, len(out))
	}
}

// TestTraceMountBytesDisabledPassthrough proves the zero-cost path: a
// non-recording span (telemetry off) gets the stream back untouched — no
// byte-counting wrapper on the mount data path — and a no-op finish.
func TestTraceMountBytesDisabledPassthrough(t *testing.T) {
	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "caretaker.mount")
	podSide, serverSide := net.Pipe()
	defer podSide.Close()
	defer serverSide.Close()

	traced, finish := traceMountBytes(span, podSide)
	if traced != podSide {
		t.Errorf("traceMountBytes wrapped the stream (%T) with tracing disabled; want passthrough", traced)
	}
	finish() // must be safe to call
}
