package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// installSpanRecorder swaps the global TracerProvider for a recording one for
// the test's duration. It must run before the server under test is built, so
// its tracer (and the otelhttp wrapper) binds to the recorder.
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return sr
}

// serveMountSession attaches a deploy session exporting dir as mount m0 to the
// server at wsBase and returns the session id.
func serveMountSession(t *testing.T, ctx context.Context, fb *fakeMountingBackend, wsBase, dir string) string {
	t.Helper()
	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data", ReadOnly: true}},
		},
		LocalMounts: []deploywire.LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as,
			map[string]string{"m0": dir}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()
	select {
	case mounts := <-fb.mounts:
		return mounts[0].Session
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
		return ""
	}
}

// awaitMountRelaySpan polls the recorder until a cornus.mount.relay span ends
// (the relay finishes asynchronously after the caretaker mux closes).
func awaitMountRelaySpan(t *testing.T, sr *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, s := range sr.Ended() {
			if s.Name() == "cornus.mount.relay" {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("cornus.mount.relay span never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// spanAttrs flattens a span's attributes into a map keyed by attribute name.
func spanAttrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

// TestMountRelaySpanLocal drives the unified caretaker mount relay with a
// recording TracerProvider installed and asserts each relayed stream ends a
// cornus.mount.relay span carrying the session DIGEST (never the raw session
// id, which is a capability), the mount name, transport=local, and the rx/tx
// byte totals — parented to the caretaker attach connection's HTTP span.
func TestMountRelaySpanLocal(t *testing.T) {
	sr := installSpanRecorder(t)

	dir := t.TempDir()
	const marker = "TRACED-9P-DATA" // <= 16 bytes: readMarkerOverMux reads at most 16
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := serveMountSession(t, ctx, fb, wsBase, dir)

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	if got := readMarkerOverMux(t, mux, session, "m0"); got != marker {
		t.Errorf("mount payload = %q, want %q", got, marker)
	}
	mux.Close() // end the mount stream so the relay finishes its span

	span := awaitMountRelaySpan(t, sr)
	attrs := spanAttrs(span)

	wantDigest := strings.TrimPrefix(mountServiceName(session), mountServicePrefix)
	if got := attrs["cornus.mount.session"].AsString(); got != wantDigest {
		t.Errorf("cornus.mount.session = %q, want digest %q", got, wantDigest)
	}
	for k, v := range attrs {
		if v.AsString() == session {
			t.Errorf("raw session id leaked into span attribute %s", k)
		}
	}
	if got := attrs["cornus.mount.name"].AsString(); got != "m0" {
		t.Errorf("cornus.mount.name = %q, want m0", got)
	}
	if got := attrs["cornus.mount.transport"].AsString(); got != "local" {
		t.Errorf("cornus.mount.transport = %q, want local", got)
	}
	if rx := attrs["cornus.mount.bytes.rx"].AsInt64(); rx < int64(len(marker)) {
		t.Errorf("cornus.mount.bytes.rx = %d, want >= %d (payload)", rx, len(marker))
	}
	if tx := attrs["cornus.mount.bytes.tx"].AsInt64(); tx <= 0 {
		t.Errorf("cornus.mount.bytes.tx = %d, want > 0 (9P requests)", tx)
	}
	if span.Status().Code == codes.Error {
		t.Errorf("span status = error (%q), want ok/unset", span.Status().Description)
	}
	if !span.Parent().IsValid() {
		t.Error("cornus.mount.relay span has no parent; want the attach connection's HTTP span")
	}
	if span.EndTime().Before(span.StartTime()) {
		t.Error("span end precedes start")
	}
}

// TestMountRelaySpanForwarded runs the two-replica relay (session on A, the
// caretaker mux on B) and asserts the caretaker-facing replica emits the ONE
// cornus.mount.relay span for the stream, with transport=forwarded and byte
// totals — mirroring the metering rule that the forward hop's owner side stays
// uninstrumented so streams are counted once cluster-wide.
func TestMountRelaySpanForwarded(t *testing.T) {
	sr := installSpanRecorder(t)
	mr := miniredis.RunT(t)

	dir := t.TempDir()
	const marker = "FWD-TRACED-9P"
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	tsA, _ := newMountReplicaServer(t, mr, "replicaA", fb)
	tsB, _ := newMountReplicaServer(t, mr, "replicaB", &fakeBackend{})

	wsA := "ws" + strings.TrimPrefix(tsA.URL, "http")
	wsB := "ws" + strings.TrimPrefix(tsB.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsA)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := serveMountSession(t, ctx, fb, wsA, dir)

	mux, err := wire.Dial(ctx, wsB+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach on B: %v", err)
	}
	if got := readMarkerOverMux(t, mux, session, "m0"); got != marker {
		t.Errorf("mount via wrong replica = %q, want %q", got, marker)
	}
	mux.Close()

	span := awaitMountRelaySpan(t, sr)
	attrs := spanAttrs(span)
	if got := attrs["cornus.mount.transport"].AsString(); got != "forwarded" {
		t.Errorf("cornus.mount.transport = %q, want forwarded", got)
	}
	if got := attrs["cornus.mount.name"].AsString(); got != "m0" {
		t.Errorf("cornus.mount.name = %q, want m0", got)
	}
	wantDigest := strings.TrimPrefix(mountServiceName(session), mountServicePrefix)
	if got := attrs["cornus.mount.session"].AsString(); got != wantDigest {
		t.Errorf("cornus.mount.session = %q, want digest %q", got, wantDigest)
	}
	if rx := attrs["cornus.mount.bytes.rx"].AsInt64(); rx < int64(len(marker)) {
		t.Errorf("cornus.mount.bytes.rx = %d, want >= %d (payload)", rx, len(marker))
	}
	// Exactly one relay span cluster-wide: the owner side of the forward hop
	// must not emit a second one.
	count := 0
	for _, s := range sr.Ended() {
		if s.Name() == "cornus.mount.relay" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("cornus.mount.relay spans = %d, want exactly 1", count)
	}
}

// TestTraceMountRelayDisabledPassthrough proves the zero-cost path: with a
// non-recording tracer (telemetry off) traceMountRelay returns the conn
// UNTOUCHED — no byte-counting wrapper on the relay data path — and a no-op
// finish func.
func TestTraceMountRelayDisabledPassthrough(t *testing.T) {
	s := &Server{tracer: noop.NewTracerProvider().Tracer("test")}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	conn, finish := s.traceMountRelay(context.Background(), "raw-session", "m0", "local", a)
	if conn != a {
		t.Errorf("traceMountRelay wrapped the conn (%T) with tracing disabled; want passthrough", conn)
	}
	finish(nil) // must be safe to call
}
