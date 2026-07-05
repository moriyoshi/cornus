package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/obsstore"
	"cornus/pkg/wire"
)

// muxPair returns two connected in-memory endpoints standing in for the two ends
// of one yamux telemetry stream. The tag byte is already consumed by the accept
// loop in production, so these tests start at the signal line — the same place
// acceptTelemetryMuxed does.
func muxPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	c, s := net.Pipe()
	t.Cleanup(func() { c.Close(); s.Close() })
	return c, s
}

// writeTelemetryFrame writes what wire.OpenTelemetry writes after the tag, so the
// server side can be exercised without a real yamux session.
func writeTelemetryFrame(t *testing.T, c net.Conn, signal string, body []byte) {
	t.Helper()
	go func() {
		_, _ = c.Write([]byte(signal + "\n"))
		hdr := []byte{byte(len(body) >> 24), byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
		_, _ = c.Write(hdr)
		_, _ = c.Write(body)
	}()
}

func readAck(t *testing.T, c net.Conn) byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack [1]byte
	if _, err := c.Read(ack[:]); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	return ack[0]
}

// TestMuxTelemetryReachesTheSameSinks is the point of the whole mux path: an
// export arriving over the caretaker connection must be treated exactly as one
// arriving over HTTP — same store, same re-export forwarder. Any divergence here
// would mean a workload's telemetry depended on which transport it took.
func TestMuxTelemetryReachesTheSameSinks(t *testing.T) {
	up := &upstreamRecorder{}
	upSrv := newTestUpstream(t, up)
	store := &queryStore{}

	s := &Server{obs: store, mux: http.NewServeMux()}
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: upSrv})

	client, server := muxPair(t)
	body := sampleOTLPLogs(t)
	writeTelemetryFrame(t, client, obsSignalLogs, body)
	go s.acceptTelemetryMuxed(context.Background(), server)

	if ack := readAck(t, client); ack != wire.TelemetryAckOK {
		t.Fatalf("ack = %q, want OK", ack)
	}
	if len(store.ingested) != 1 {
		t.Errorf("store received %d exports over the mux, want 1", len(store.ingested))
	}
	waitForExport(t, "the mux export to reach the upstream too", func() bool { return up.hits.Load() == 1 })

	// Same bytes, unaltered, as on the HTTP path.
	paths, bodies, _ := up.snapshot()
	if string(bodies[0]) != string(body) {
		t.Error("the mux path altered the payload")
	}
	if paths[0] != "/v1/logs" {
		t.Errorf("upstream path = %q, want the signal preserved across the mux", paths[0])
	}
}

// TestMuxTelemetryBackpressureAsksForRetry mirrors the HTTP receiver's 429: the
// sender still holds the only copy, so shedding it silently would lose data.
func TestMuxTelemetryBackpressureAsksForRetry(t *testing.T) {
	s := &Server{obs: &queryStore{ingestErr: obsstore.ErrBackpressure}, mux: http.NewServeMux()}

	client, server := muxPair(t)
	writeTelemetryFrame(t, client, obsSignalLogs, sampleOTLPLogs(t))
	go s.acceptTelemetryMuxed(context.Background(), server)

	if ack := readAck(t, client); ack != wire.TelemetryAckRetry {
		t.Errorf("ack = %q, want retry under backpressure", ack)
	}
}

// TestMuxTelemetryRejectsUnparseable: a payload that will never parse must be
// rejected, not retried, so the Collector discards it instead of looping.
func TestMuxTelemetryRejectsUnparseable(t *testing.T) {
	s := &Server{obs: &queryStore{ingestErr: errNotOTLP}, mux: http.NewServeMux()}

	client, server := muxPair(t)
	writeTelemetryFrame(t, client, obsSignalLogs, []byte("not protobuf"))
	go s.acceptTelemetryMuxed(context.Background(), server)

	if ack := readAck(t, client); ack != wire.TelemetryAckReject {
		t.Errorf("ack = %q, want reject", ack)
	}
}

// TestMuxTelemetryWithNoSinkRejects: a caretaker wired for the mux against a
// server with nowhere to put telemetry must be told so. Acking OK would look
// healthy while discarding everything — the failure mode this whole feature
// exists to avoid.
func TestMuxTelemetryWithNoSinkRejects(t *testing.T) {
	s := &Server{mux: http.NewServeMux()} // no store, no upstream

	client, server := muxPair(t)
	writeTelemetryFrame(t, client, obsSignalLogs, sampleOTLPLogs(t))
	go s.acceptTelemetryMuxed(context.Background(), server)

	if ack := readAck(t, client); ack != wire.TelemetryAckReject {
		t.Errorf("ack = %q, want reject when the server has nowhere to put telemetry", ack)
	}
}

// TestMuxTelemetryWorksWithExportOnly is the gateway shape over the mux: no
// store at all, an upstream configured, and a pod with no route to that upstream
// still lands its telemetry there. This is the combination the two features were
// built for.
func TestMuxTelemetryWorksWithExportOnly(t *testing.T) {
	up := &upstreamRecorder{}
	upSrv := newTestUpstream(t, up)

	s := &Server{mux: http.NewServeMux()} // deliberately no store
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: upSrv})

	client, server := muxPair(t)
	writeTelemetryFrame(t, client, obsSignalLogs, sampleOTLPLogs(t))
	go s.acceptTelemetryMuxed(context.Background(), server)

	if ack := readAck(t, client); ack != wire.TelemetryAckOK {
		t.Fatalf("ack = %q, want OK with an upstream but no store", ack)
	}
	waitForExport(t, "the forwarded export", func() bool { return up.hits.Load() == 1 })
	paths, _, _ := up.snapshot()
	if paths[0] != "/v1/logs" {
		t.Errorf("upstream path = %q, want the signal preserved across the mux", paths[0])
	}
}

// TestMuxTelemetryGatewayValidates: with no store there is nothing in the path
// that would otherwise parse the payload, so the mux arm has to validate too —
// otherwise it becomes a way to launder garbage past the check the HTTP arm does.
func TestMuxTelemetryGatewayValidates(t *testing.T) {
	up := &upstreamRecorder{}
	upSrv := newTestUpstream(t, up)

	s := &Server{mux: http.NewServeMux()} // no store
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: upSrv})

	client, server := muxPair(t)
	writeTelemetryFrame(t, client, obsSignalLogs, []byte("definitely not protobuf"))
	go s.acceptTelemetryMuxed(context.Background(), server)

	if ack := readAck(t, client); ack != wire.TelemetryAckReject {
		t.Errorf("ack = %q, want reject for an unparseable export in gateway mode", ack)
	}
	time.Sleep(50 * time.Millisecond)
	if up.hits.Load() != 0 {
		t.Errorf("an unparseable export was forwarded upstream (%d requests)", up.hits.Load())
	}
}

// TestMuxTelemetryRejectsAGarbledStream: a stream that does not carry a readable
// frame must be refused rather than hanging or being read as an empty export.
func TestMuxTelemetryRejectsAGarbledStream(t *testing.T) {
	s := &Server{obs: &queryStore{}, mux: http.NewServeMux()}

	client, server := muxPair(t)
	go func() {
		_, _ = client.Write([]byte("logs\n\x00\x00")) // truncated length prefix
		client.Close()
	}()
	done := make(chan struct{})
	go func() { defer close(done); s.acceptTelemetryMuxed(context.Background(), server) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a garbled telemetry stream hung the handler")
	}
}

// TestLogOptionsInstanceCrossesTheWire is the regression guard for a bug an E2E
// caught after the interface, all five backends, and the recorder were already
// correct: LogOptions travels to the server as query parameters, and `instance`
// was not among them. Every local path worked; every client->server path silently
// streamed replica 0. The failure looked like the feature working — three tails,
// three prefixes — while carrying one container's output three times.
func TestLogOptionsInstanceCrossesTheWire(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("instance")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	if err := c.Logs(context.Background(), "web", api.LogOptions{Instance: 2}, io.Discard); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if got != "2" {
		t.Errorf("instance query parameter = %q, want \"2\"", got)
	}

	// The default stays off the wire, so a request for the common case is
	// byte-identical to what it was before the field existed.
	if err := c.Logs(context.Background(), "web", api.LogOptions{}, io.Discard); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if got != "" {
		t.Errorf("instance = %q for the default; it must not be sent", got)
	}
}

// TestServerParsesLogInstance is the other half of the same round trip.
func TestServerParsesLogInstance(t *testing.T) {
	cases := map[string]int{"": 0, "0": 0, "3": 3, "-1": 0, "nonsense": 0}
	for in, want := range cases {
		if got := atoiOrZero(in); got != want {
			t.Errorf("atoiOrZero(%q) = %d, want %d", in, got, want)
		}
	}
}
