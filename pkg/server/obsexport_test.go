package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// upstreamRecorder stands in for the operator's real OTLP backend.
type upstreamRecorder struct {
	mu       sync.Mutex
	paths    []string
	bodies   [][]byte
	headers  []http.Header
	status   int
	statusFn func(n int) int
	hits     atomic.Int64
	release  chan struct{} // when non-nil, requests block until it is closed
}

func (u *upstreamRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := int(u.hits.Add(1))
		if u.release != nil {
			<-u.release
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.paths = append(u.paths, r.URL.Path)
		u.bodies = append(u.bodies, body)
		u.headers = append(u.headers, r.Header.Clone())
		u.mu.Unlock()

		code := u.status
		if u.statusFn != nil {
			code = u.statusFn(n)
		}
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	}
}

func (u *upstreamRecorder) snapshot() (paths []string, bodies [][]byte, headers []http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.paths...), append([][]byte(nil), u.bodies...), append([]http.Header(nil), u.headers...)
}

// newTestUpstream starts a stand-in OTLP backend and returns its base URL.
func newTestUpstream(t *testing.T, up *upstreamRecorder) string {
	t.Helper()
	srv := httptest.NewServer(up.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func startExporter(t *testing.T, cfg obsExportConfig) *obsExporter {
	t.Helper()
	e := newObsExporter(context.Background(), cfg)
	if e == nil {
		t.Fatal("newObsExporter returned nil for an active config")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = e.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return e
}

func waitForExport(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestExportForwardsUnchanged is the core contract: the bytes cornus received
// are the bytes the upstream gets, at the standard OTLP path for the signal.
// Re-encoding anywhere in this path would be both wasteful and a chance to
// corrupt a payload cornus never needed to understand.
func TestExportForwardsUnchanged(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	e := startExporter(t, obsExportConfig{
		Endpoint: srv.URL,
		Headers:  map[string]string{"authorization": "Bearer hunter2"},
	})
	payload := []byte("opaque-otlp-bytes")
	e.Enqueue(obsSignalTraces, payload)

	waitForExport(t, "the upstream to receive the export", func() bool { return up.hits.Load() == 1 })
	paths, bodies, headers := up.snapshot()

	if paths[0] != "/v1/traces" {
		t.Errorf("upstream path = %q, want /v1/traces", paths[0])
	}
	if string(bodies[0]) != string(payload) {
		t.Errorf("payload was altered in transit: %q", bodies[0])
	}
	if got := headers[0].Get("Authorization"); got != "Bearer hunter2" {
		t.Errorf("configured header not sent: %q", got)
	}
	if got := headers[0].Get("Content-Type"); got != protobufContentType {
		t.Errorf("content type = %q, want %q", got, protobufContentType)
	}

	sent, dropped, failed := e.Stats()
	if sent != 1 || dropped != 0 || failed != 0 {
		t.Errorf("stats = sent %d, dropped %d, failed %d; want 1/0/0", sent, dropped, failed)
	}
}

// TestExportTrimsTrailingSlash keeps a configured endpoint like
// "https://otlp.example.com/" from composing "//v1/traces".
func TestExportTrimsTrailingSlash(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	e := startExporter(t, obsExportConfig{Endpoint: srv.URL + "/"})
	e.Enqueue(obsSignalLogs, []byte("x"))
	waitForExport(t, "the export", func() bool { return up.hits.Load() == 1 })

	paths, _, _ := up.snapshot()
	if paths[0] != "/v1/logs" {
		t.Errorf("path = %q, want /v1/logs with no doubled slash", paths[0])
	}
}

// TestExportNeverBlocksIngest is the design constraint the whole forwarder is
// built around: a wedged upstream must not become the server's problem. With the
// upstream held open, Enqueue must still return immediately, and the overflow
// must be counted rather than silently absorbed.
func TestExportNeverBlocksIngest(t *testing.T) {
	up := &upstreamRecorder{release: make(chan struct{})}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()
	defer close(up.release)

	e := startExporter(t, obsExportConfig{Endpoint: srv.URL})

	// Far more than the queue holds, from the caller's goroutine, with the
	// upstream refusing to complete a single request.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < obsExportQueue*3; i++ {
			e.Enqueue(obsSignalLogs, []byte("payload"))
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Enqueue blocked while the upstream was stalled; ingest would have stalled with it")
	}

	waitForExport(t, "drops to be counted", func() bool {
		_, dropped, _ := e.Stats()
		return dropped > 0
	})
	_, dropped, _ := e.Stats()
	if dropped == 0 {
		t.Error("overflow was absorbed silently instead of counted")
	}
}

// TestExportRetriesTransientFailures: a 5xx is the backend deserving another
// try, and one retry is what the bounded queue can afford.
func TestExportRetriesTransientFailures(t *testing.T) {
	up := &upstreamRecorder{statusFn: func(n int) int {
		if n == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	e := startExporter(t, obsExportConfig{Endpoint: srv.URL})
	e.Enqueue(obsSignalMetrics, []byte("x"))

	waitForExport(t, "the retry to succeed", func() bool {
		sent, _, _ := e.Stats()
		return sent == 1
	})
	if up.hits.Load() != 2 {
		t.Errorf("upstream saw %d requests, want 2 (original + one retry)", up.hits.Load())
	}
	_, _, failed := e.Stats()
	if failed != 0 {
		t.Errorf("failed = %d after a successful retry, want 0", failed)
	}
}

// TestExportDoesNotRetryPermanentRejections: a 4xx means this payload will never
// be accepted, so retrying it only spends queue the next payload needs.
func TestExportDoesNotRetryPermanentRejections(t *testing.T) {
	up := &upstreamRecorder{status: http.StatusBadRequest}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	e := startExporter(t, obsExportConfig{Endpoint: srv.URL})
	e.Enqueue(obsSignalLogs, []byte("x"))

	waitForExport(t, "the rejection to be counted", func() bool {
		_, _, failed := e.Stats()
		return failed == 1
	})
	if got := up.hits.Load(); got != 1 {
		t.Errorf("upstream saw %d requests, want 1 (a 4xx must not be retried)", got)
	}
}

// TestExportRetriesRateLimits: 429 is explicitly the backend asking for another
// try, unlike the rest of 4xx.
func TestExportRetriesRateLimits(t *testing.T) {
	up := &upstreamRecorder{statusFn: func(n int) int {
		if n == 1 {
			return http.StatusTooManyRequests
		}
		return http.StatusOK
	}}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	e := startExporter(t, obsExportConfig{Endpoint: srv.URL})
	e.Enqueue(obsSignalLogs, []byte("x"))

	waitForExport(t, "the retry after 429", func() bool {
		sent, _, _ := e.Stats()
		return sent == 1
	})
	if up.hits.Load() != 2 {
		t.Errorf("upstream saw %d requests, want 2", up.hits.Load())
	}
}

// TestExportDisabledIsANilNoop keeps every call site free of "is it configured"
// branching — the common case is that it is not.
func TestExportDisabledIsANilNoop(t *testing.T) {
	if e := newObsExporter(context.Background(), obsExportConfig{}); e != nil {
		t.Fatal("an empty endpoint produced an exporter")
	}
	if e := newObsExporter(context.Background(), obsExportConfig{Endpoint: "   "}); e != nil {
		t.Fatal("a blank endpoint produced an exporter")
	}
	var nilExporter *obsExporter
	nilExporter.Enqueue(obsSignalLogs, []byte("x")) // must not panic
	if sent, dropped, failed := nilExporter.Stats(); sent|dropped|failed != 0 {
		t.Errorf("nil exporter reported stats %d/%d/%d", sent, dropped, failed)
	}
}

func TestExportIgnoresEmptyPayloads(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	e := startExporter(t, obsExportConfig{Endpoint: srv.URL})
	e.Enqueue(obsSignalLogs, nil)
	e.Enqueue(obsSignalLogs, []byte{})
	time.Sleep(50 * time.Millisecond)
	if up.hits.Load() != 0 {
		t.Errorf("an empty payload was forwarded (%d requests)", up.hits.Load())
	}
}

// --- integration with the receive path --------------------------------------

// TestAcceptOTLPStoresAndForwards proves the two sinks are independent and both
// run: this is the "keep a local copy AND ship it upstream" configuration.
func TestAcceptOTLPStoresAndForwards(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	store := &queryStore{}
	s := &Server{obs: store, mux: http.NewServeMux()}
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: srv.URL})
	s.routes()

	body := sampleOTLPLogs(t)
	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", body, protobufContentType)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if len(store.ingested) != 1 {
		t.Errorf("store received %d exports, want 1", len(store.ingested))
	}
	waitForExport(t, "the upstream to receive it too", func() bool { return up.hits.Load() == 1 })
}

// TestReceiveWorksWithNoStore is the gateway configuration: no store compiled in
// or enabled, an upstream configured, and the receive routes still present. This
// is what lets a pure-Go build participate — and, with telemetry over the mux,
// what lets a workload with no egress reach a SaaS backend.
func TestReceiveWorksWithNoStore(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	s := &Server{mux: http.NewServeMux()} // no store at all
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: srv.URL})
	s.routes()

	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/traces", sampleOTLPLogs(t), protobufContentType)
	if rec.Code != http.StatusOK {
		t.Fatalf("receive with no store = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	waitForExport(t, "the forwarded export", func() bool { return up.hits.Load() == 1 })

	// The QUERY surface must stay absent — there is nothing to read back.
	q := httptest.NewRecorder()
	s.mux.ServeHTTP(q, httptest.NewRequest(http.MethodGet, "/.cornus/v1/obs/logs", nil))
	if q.Code != http.StatusNotFound {
		t.Errorf("query route answered %d with no store, want 404", q.Code)
	}
}

// TestReceiveAbsentWithNeitherSink: with no store and no upstream there is
// nowhere to put telemetry, so accepting it would be a lie.
func TestReceiveAbsentWithNeitherSink(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", sampleOTLPLogs(t), protobufContentType)
	if rec.Code != http.StatusNotFound {
		t.Errorf("receive with no sink = %d, want 404", rec.Code)
	}
}

// TestObsStatusReportsExport surfaces the forwarder where an operator looks when
// telemetry reaches cornus but not the backend.
func TestObsStatusReportsExport(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()

	s := &Server{obs: &queryStore{}, mux: http.NewServeMux()}
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: srv.URL})
	s.routes()

	rec := doObs(t, s, "/.cornus/v1/obs/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "\"export\"") || !strings.Contains(body, srv.URL) {
		t.Errorf("status omits the re-export block: %s", body)
	}
}

// TestRecordedLogsAreAlsoForwarded is the regression guard for a bug an E2E
// caught and every unit test missed: the log recorder wrote straight to the
// store, so the zero-touch feed — by far the largest source of records — silently
// skipped re-export while the OTLP receiver's path worked fine.
//
// The fix routes the recorder through the same acceptOTLP as every other feed,
// so this asserts on the wiring: what the recorder produces must reach BOTH
// sinks, exactly as a received export does.
func TestRecordedLogsAreAlsoForwarded(t *testing.T) {
	up := &upstreamRecorder{}
	upSrv := newTestUpstream(t, up)

	store := &queryStore{}
	s := &Server{obs: store, mux: http.NewServeMux()}
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: upSrv})

	// Build the recorder the way the server does, then drive one batch through it.
	rec := newLogRecorder(s.obs, s.acceptOTLP, func() (logSource, error) { return nil, nil })
	sink := newLineSink(rec, map[string]string{"service.name": "web"}, "stdout", 0)
	sink.Write([]byte("2026-01-02T03:04:05Z recorded line\n"))
	sink.Close(context.Background())

	if len(store.ingested) != 1 {
		t.Fatalf("store received %d batches from the recorder, want 1", len(store.ingested))
	}
	waitForExport(t, "the recorded batch to reach the upstream too", func() bool { return up.hits.Load() == 1 })
	paths, _, _ := up.snapshot()
	if paths[0] != "/v1/logs" {
		t.Errorf("recorded batch forwarded to %q, want /v1/logs", paths[0])
	}
}

// TestGatewayValidatesPayloads is the second bug an E2E caught: with no store in
// the path there is nothing that would otherwise parse an export, so garbage got
// a 200 and was handed to the upstream. The sender, told it succeeded, discards
// its only copy — data lost, and the failure surfaces far from its cause.
func TestGatewayValidatesPayloads(t *testing.T) {
	up := &upstreamRecorder{}
	upSrv := newTestUpstream(t, up)

	s := &Server{mux: http.NewServeMux()} // gateway: no store
	s.obsExport = startExporter(t, obsExportConfig{Endpoint: upSrv})
	s.routes()

	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", []byte("definitely not protobuf"), protobufContentType)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("gateway accepted an unparseable export: %d", rec.Code)
	}
	// And it must not have been forwarded — laundering it upstream is the harm.
	time.Sleep(50 * time.Millisecond)
	if up.hits.Load() != 0 {
		t.Errorf("an unparseable export was forwarded upstream (%d requests)", up.hits.Load())
	}

	// A well-formed export still passes straight through, unparsed beyond the check.
	good := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", sampleOTLPLogs(t), protobufContentType)
	if good.Code != http.StatusOK {
		t.Fatalf("gateway rejected a valid export: %d (%s)", good.Code, good.Body)
	}
	waitForExport(t, "the valid export to be forwarded", func() bool { return up.hits.Load() == 1 })
}
