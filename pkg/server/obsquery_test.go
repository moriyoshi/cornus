package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"cornus/pkg/obsstore"
)

// queryStore records the query it was handed so the tests can assert on the
// translation from HTTP parameters to a store query — which is where the
// interesting bugs live, not in the JSON encoding.
type queryStore struct {
	fakeObsStore
	lastLog   obsstore.LogQuery
	lastTrace obsstore.TraceQuery
	logs      []obsstore.LogEntry
	traceErr  error
	ingestErr error
	ingested  [][]byte
}

func (q *queryStore) QueryLogs(_ context.Context, lq obsstore.LogQuery) ([]obsstore.LogEntry, error) {
	q.lastLog = lq
	return q.logs, nil
}

func (q *queryStore) SearchTraces(_ context.Context, tq obsstore.TraceQuery) ([]obsstore.TraceSummary, error) {
	q.lastTrace = tq
	return nil, q.traceErr
}

func (q *queryStore) IngestLogs(b []byte) error {
	if q.ingestErr != nil {
		return q.ingestErr
	}
	q.ingested = append(q.ingested, b)
	return nil
}

func obsTestServer(store obsstore.Store) *Server {
	s := &Server{obs: store, mux: http.NewServeMux()}
	s.routes()
	return s
}

func doObs(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestObsLogsTranslatesQueryParams is the real content of the read API: every
// user-facing filter has to survive the trip from a URL to a store query, and a
// silently dropped filter returns plausible but wrong rows.
func TestObsLogsTranslatesQueryParams(t *testing.T) {
	store := &queryStore{}
	s := obsTestServer(store)

	rec := doObs(t, s, "/.cornus/v1/obs/logs?service=web&match=timeout&severity=error&stream=stderr&limit=50&newest=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := store.lastLog
	if got.Service != "web" {
		t.Errorf("Service = %q", got.Service)
	}
	if got.Match != "timeout" {
		t.Errorf("Match = %q", got.Match)
	}
	if got.MinSeverity != obsstore.SeverityError {
		t.Errorf("MinSeverity = %d, want %d", got.MinSeverity, obsstore.SeverityError)
	}
	if got.Attrs["cornus.stream"] != "stderr" {
		t.Errorf("stream filter = %v", got.Attrs)
	}
	if got.Limit != 50 {
		t.Errorf("Limit = %d", got.Limit)
	}
	if !got.Newest {
		t.Error("Newest was not set")
	}
}

// TestObsLogsSinceUsesTheRuntimeVocabulary pins that a time expression means the
// same thing here as on `compose logs --since`. A user should never discover
// that the store spells time differently from the runtime.
func TestObsLogsSinceUsesTheRuntimeVocabulary(t *testing.T) {
	store := &queryStore{}
	s := obsTestServer(store)

	before := time.Now().Add(-90 * time.Minute)
	rec := doObs(t, s, "/.cornus/v1/obs/logs?since=1h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := store.lastLog.Start
	if got.IsZero() {
		t.Fatal("since=1h did not produce a Start bound")
	}
	if got.Before(before) || got.After(time.Now()) {
		t.Errorf("since=1h resolved to %v, which is not about an hour ago", got)
	}

	// An RFC3339 instant must work too, since that is what a script passes.
	stamp := "2026-03-04T05:06:07Z"
	if rec := doObs(t, s, "/.cornus/v1/obs/logs?since="+stamp); rec.Code != http.StatusOK {
		t.Fatalf("RFC3339 since: status = %d, body = %s", rec.Code, rec.Body)
	}
	want, _ := time.Parse(time.RFC3339, stamp)
	if !store.lastLog.Start.Equal(want) {
		t.Errorf("RFC3339 since resolved to %v, want %v", store.lastLog.Start, want)
	}
}

// TestObsLogsRejectsBadInput checks that malformed input is a 400 with a
// message, not a silently ignored filter that returns the wrong rows.
func TestObsLogsRejectsBadInput(t *testing.T) {
	s := obsTestServer(&queryStore{})
	for _, target := range []string{
		"/.cornus/v1/obs/logs?since=not-a-time",
		"/.cornus/v1/obs/logs?severity=verbose",
	} {
		rec := doObs(t, s, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 (body %s)", target, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// TestObsLogsCapsTheLimit stops one query from materializing a whole retention
// window on both ends.
func TestObsLogsCapsTheLimit(t *testing.T) {
	store := &queryStore{}
	s := obsTestServer(store)
	for _, target := range []string{
		"/.cornus/v1/obs/logs",                  // unset
		"/.cornus/v1/obs/logs?limit=0",          // explicit zero
		"/.cornus/v1/obs/logs?limit=999999",     // beyond the cap
		"/.cornus/v1/obs/logs?limit=-5",         // nonsense
		"/.cornus/v1/obs/logs?limit=notanumber", // unparseable
	} {
		if rec := doObs(t, s, target); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, rec.Code)
		}
		if store.lastLog.Limit != obsQueryLimit {
			t.Errorf("GET %s produced limit %d, want the %d cap", target, store.lastLog.Limit, obsQueryLimit)
		}
	}
}

// TestObsEmptyResultIsAnArray keeps clients from having to special-case JSON
// null versus an empty list.
func TestObsEmptyResultIsAnArray(t *testing.T) {
	s := obsTestServer(&queryStore{})
	rec := doObs(t, s, "/.cornus/v1/obs/logs")
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty result body = %q, want []", got)
	}
}

// TestObsTracesTranslatesDurations covers the filter that makes trace search
// useful: "show me the slow ones".
func TestObsTracesTranslatesDurations(t *testing.T) {
	store := &queryStore{}
	s := obsTestServer(store)

	rec := doObs(t, s, "/.cornus/v1/obs/traces?service=web&minDuration=500ms&status=error")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if store.lastTrace.MinDuration != 500*time.Millisecond {
		t.Errorf("MinDuration = %v", store.lastTrace.MinDuration)
	}
	if store.lastTrace.Service != "web" || store.lastTrace.Status != "error" {
		t.Errorf("trace query = %+v", store.lastTrace)
	}

	if rec := doObs(t, s, "/.cornus/v1/obs/traces?minDuration=soon"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad minDuration = %d, want 400", rec.Code)
	}
}

// TestObsMetricsRequiresAQuery — an empty PromQL expression is a client mistake
// worth naming rather than an empty result.
func TestObsMetricsRequiresAQuery(t *testing.T) {
	s := obsTestServer(&queryStore{})
	if rec := doObs(t, s, "/.cornus/v1/obs/metrics"); rec.Code != http.StatusBadRequest {
		t.Errorf("metrics with no query = %d, want 400", rec.Code)
	}
	if rec := doObs(t, s, "/.cornus/v1/obs/query"); rec.Code != http.StatusBadRequest {
		t.Errorf("sql with no query = %d, want 400", rec.Code)
	}
}

// TestObsTraceRequiresAnID guards the path-value route.
func TestObsTraceRequiresAnID(t *testing.T) {
	s := obsTestServer(&queryStore{})
	// The mux itself will not match an empty id, which is a 404 — still a
	// refusal, and the useful assertion is that a real id reaches the handler.
	if rec := doObs(t, s, "/.cornus/v1/obs/trace/abc123"); rec.Code != http.StatusOK {
		t.Errorf("GET trace/abc123 = %d, want 200", rec.Code)
	}
}

func TestObsSeverityNames(t *testing.T) {
	cases := map[string]int{
		"":        0,
		"info":    obsstore.SeverityInfo,
		"INFO":    obsstore.SeverityInfo,
		"warn":    obsstore.SeverityWarn,
		"warning": obsstore.SeverityWarn,
		"error":   obsstore.SeverityError,
		"fatal":   obsstore.SeverityFatal,
		"17":      17,
	}
	for in, want := range cases {
		got, err := obsSeverity(in)
		if err != nil {
			t.Errorf("obsSeverity(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("obsSeverity(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"verbose", "-1", "99"} {
		if _, err := obsSeverity(bad); err == nil {
			t.Errorf("obsSeverity(%q) accepted an invalid level", bad)
		}
	}
}

// --- OTLP receive -----------------------------------------------------------

func postOTLP(t *testing.T, s *Server, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func sampleOTLPLogs(t *testing.T) []byte {
	t.Helper()
	b, err := obsstore.EncodeLogs(map[string]string{"service.name": "web"},
		[]obsstore.Record{{Time: time.Now(), Body: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestOTLPAcceptsAnExport is the opt-in feed's happy path: the exact bytes an
// OTLP/HTTP exporter sends reach the store untouched, and the response is the
// protobuf a Collector expects rather than a bare 200.
func TestOTLPAcceptsAnExport(t *testing.T) {
	store := &queryStore{}
	s := obsTestServer(store)
	body := sampleOTLPLogs(t)

	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", body, protobufContentType)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != protobufContentType {
		t.Errorf("response content type = %q, want %q", ct, protobufContentType)
	}
	var resp collogs.ExportLogsServiceResponse
	if err := proto.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Errorf("response body is not an ExportLogsServiceResponse: %v", err)
	}
	if len(store.ingested) != 1 {
		t.Fatalf("store received %d exports, want 1", len(store.ingested))
	}
	if string(store.ingested[0]) != string(body) {
		t.Error("the receiver altered the payload; it must pass the bytes through untouched")
	}
}

// TestOTLPBackpressureAsksForRetry matters because, unlike the stdout recorder
// whose source keeps flowing regardless, an exporter holds the only copy of
// these spans. Shedding them silently loses data an honest 429 would preserve.
func TestOTLPBackpressureAsksForRetry(t *testing.T) {
	s := obsTestServer(&queryStore{ingestErr: obsstore.ErrBackpressure})
	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", sampleOTLPLogs(t), protobufContentType)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 carries no Retry-After, so a Collector has nothing to pace against")
	}
}

// TestOTLPRejectsUnparseablePayload: a payload that will never parse must be a
// 400 so the sender discards it instead of retrying forever.
func TestOTLPRejectsUnparseablePayload(t *testing.T) {
	s := obsTestServer(&queryStore{ingestErr: errNotOTLP})
	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", []byte("this is not protobuf"), protobufContentType)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

var errNotOTLP = &obsError{"not an OTLP payload"}

// TestOTLPRejectsWrongContentType keeps an untested encoding (OTLP JSON) from
// silently reaching a protobuf parser.
func TestOTLPRejectsWrongContentType(t *testing.T) {
	s := obsTestServer(&queryStore{})
	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", sampleOTLPLogs(t), "application/json")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestOTLPContentTypeVariants(t *testing.T) {
	cases := map[string]bool{
		"application/x-protobuf":                true,
		"application/x-protobuf; charset=utf-8": true,
		"application/protobuf":                  true,
		"application/json":                      false,
		"text/plain":                            false,
	}
	for ct, want := range cases {
		if got := isProtobufContentType(ct); got != want {
			t.Errorf("isProtobufContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}

// TestOTLPEmptyBodyIsANoop: an exporter with nothing to send is not an error.
func TestOTLPEmptyBodyIsANoop(t *testing.T) {
	store := &queryStore{}
	s := obsTestServer(store)
	rec := postOTLP(t, s, "/.cornus/v1/otlp/v1/logs", nil, protobufContentType)
	if rec.Code != http.StatusOK {
		t.Errorf("empty export = %d, want 200", rec.Code)
	}
	if len(store.ingested) != 0 {
		t.Errorf("empty export reached the store as %d ingests", len(store.ingested))
	}
}

func TestOTLPRejectsGET(t *testing.T) {
	s := obsTestServer(&queryStore{})
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.cornus/v1/otlp/v1/logs", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on an export endpoint = %d, want 405", rec.Code)
	}
}

// TestObsStatusReportsShedding: the dropped counter is the reason this endpoint
// exists, since without it an empty query result means two opposite things.
func TestObsStatusReportsShedding(t *testing.T) {
	s := obsTestServer(&queryStore{})
	rec := doObs(t, s, "/.cornus/v1/obs/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["dropped"]; !ok {
		t.Errorf("status payload has no dropped counter: %v", got)
	}
}
