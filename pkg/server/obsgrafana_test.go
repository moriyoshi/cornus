package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/obsstore"
)

// grafanaStore records the window each datasource query resolved to, which is
// where the unit bugs live: Prometheus sends seconds and Loki sends nanoseconds,
// and confusing them yields an empty dashboard rather than an error.
type grafanaStore struct {
	fakeObsStore
	promStart, promEnd time.Time
	promStep           time.Duration
	promExpr           string
	lokiStart, lokiEnd time.Time
	series             []obsstore.Series
	logs               []obsstore.LogEntry
	spans              []obsstore.Span
	promErr            error
}

func (g *grafanaStore) QueryPromQL(_ context.Context, expr string, start, end time.Time, step time.Duration) ([]obsstore.Series, error) {
	g.promExpr, g.promStart, g.promEnd, g.promStep = expr, start, end, step
	return g.series, g.promErr
}

func (g *grafanaStore) QueryLogQL(_ context.Context, _ string, start, end time.Time, _ int) ([]obsstore.LogEntry, error) {
	g.lokiStart, g.lokiEnd = start, end
	return g.logs, nil
}

func (g *grafanaStore) TraceSpans(context.Context, string) ([]obsstore.Span, error) {
	return g.spans, nil
}

func grafanaGet(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestPromQueryRangeEnvelope pins the exact shape Grafana's Prometheus parser
// expects. Getting resultType or the value encoding wrong renders as an empty
// panel with no error, which is the worst possible failure mode.
func TestPromQueryRangeEnvelope(t *testing.T) {
	store := &grafanaStore{series: []obsstore.Series{{
		Labels: map[string]string{"service": "web"},
		Points: []obsstore.Point{
			{T: time.Unix(1700000000, 0).UTC(), V: 1.5},
			{T: time.Unix(1700000060, 0).UTC(), V: 2},
		},
	}}}
	s := obsTestServer(store)

	rec := grafanaGet(t, s, "/.cornus/v1/obs/prom/api/v1/query_range?query=up&start=1700000000&end=1700000120&step=60")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	if got.Status != "success" || got.Data.ResultType != "matrix" {
		t.Fatalf("envelope = %s/%s, want success/matrix", got.Status, got.Data.ResultType)
	}
	if len(got.Data.Result) != 1 || len(got.Data.Result[0].Values) != 2 {
		t.Fatalf("unexpected result shape: %+v", got.Data.Result)
	}
	if got.Data.Result[0].Metric["service"] != "web" {
		t.Errorf("labels lost: %v", got.Data.Result[0].Metric)
	}
	// Timestamps are unix SECONDS, values are STRINGS. Emitting the value as a
	// JSON number works for small values and silently loses precision on large
	// ones, which is exactly the bug nobody notices.
	first := got.Data.Result[0].Values[0]
	if ts, ok := first[0].(float64); !ok || ts != 1700000000 {
		t.Errorf("timestamp = %v (%T), want 1700000000 as seconds", first[0], first[0])
	}
	if v, ok := first[1].(string); !ok || v != "1.5" {
		t.Errorf("value = %v (%T), want the string \"1.5\"", first[1], first[1])
	}
}

// TestPromWindowUnits: Prometheus sends unix seconds. Reading them as anything
// else shifts every query far outside the retained window.
func TestPromWindowUnits(t *testing.T) {
	store := &grafanaStore{}
	s := obsTestServer(store)

	grafanaGet(t, s, "/.cornus/v1/obs/prom/api/v1/query_range?query=up&start=1700000000&end=1700003600&step=30s")
	if got := store.promStart.Unix(); got != 1700000000 {
		t.Errorf("start = %d, want 1700000000 (seconds)", got)
	}
	if got := store.promEnd.Unix(); got != 1700003600 {
		t.Errorf("end = %d", got)
	}
	if store.promStep != 30*time.Second {
		t.Errorf("step = %v, want 30s", store.promStep)
	}

	// A bare number of seconds is also a legal step.
	grafanaGet(t, s, "/.cornus/v1/obs/prom/api/v1/query_range?query=up&start=1700000000&end=1700003600&step=15")
	if store.promStep != 15*time.Second {
		t.Errorf("numeric step = %v, want 15s", store.promStep)
	}
}

// TestLokiWindowUnitsDifferFromProm is the bug this test exists to prevent:
// Loki's timestamps are NANOseconds. Reusing the Prometheus parser here shifts
// every query by a factor of a billion and renders as an empty panel.
func TestLokiWindowUnitsDifferFromProm(t *testing.T) {
	store := &grafanaStore{}
	s := obsTestServer(store)

	grafanaGet(t, s, `/.cornus/v1/obs/loki/api/v1/query_range?query={service="web"}&start=1700000000000000000&end=1700003600000000000`)
	if got := store.lokiStart.Unix(); got != 1700000000 {
		t.Errorf("start = %d (from nanoseconds), want 1700000000", got)
	}
	if got := store.lokiEnd.Unix(); got != 1700003600 {
		t.Errorf("end = %d", got)
	}
}

// TestLokiStreamsEnvelope pins Loki's result shape: streams keyed by label set,
// with [nanosecondString, line] pairs.
func TestLokiStreamsEnvelope(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	store := &grafanaStore{logs: []obsstore.LogEntry{
		{Time: base, Service: "web", Body: "one"},
		{Time: base.Add(time.Second), Service: "api", Body: "two"},
		{Time: base.Add(2 * time.Second), Service: "web", Body: "three"},
	}}
	s := obsTestServer(store)

	rec := grafanaGet(t, s, `/.cornus/v1/obs/loki/api/v1/query_range?query={service="web"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data.ResultType != "streams" {
		t.Fatalf("resultType = %q, want streams", got.Data.ResultType)
	}
	// Two services means two streams, sorted, so a multi-service query renders
	// as separate streams rather than one interleaved blob.
	if len(got.Data.Result) != 2 {
		t.Fatalf("got %d streams, want 2: %+v", len(got.Data.Result), got.Data.Result)
	}
	if got.Data.Result[0].Stream["service"] != "api" {
		t.Errorf("streams are not sorted by service: %+v", got.Data.Result[0].Stream)
	}
	web := got.Data.Result[1]
	if len(web.Values) != 2 {
		t.Fatalf("web stream has %d lines, want 2", len(web.Values))
	}
	if web.Values[0][0] != "1700000000000000000" {
		t.Errorf("timestamp = %q, want nanoseconds as a decimal string", web.Values[0][0])
	}
	if web.Values[0][1] != "one" {
		t.Errorf("line = %q", web.Values[0][1])
	}
}

// TestTempoTraceEnvelope pins the OTLP-shaped JSON Grafana's trace view parses.
func TestTempoTraceEnvelope(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	store := &grafanaStore{spans: []obsstore.Span{
		{TraceID: "abc", SpanID: "root", Name: "GET /checkout", Service: "web", Kind: "server", Start: base, Duration: 2 * time.Second, StatusCode: "ok"},
		{TraceID: "abc", SpanID: "kid", ParentSpanID: "root", Name: "db.query", Service: "db", Kind: "client", Start: base.Add(time.Second), Duration: time.Second, StatusCode: "error", StatusMessage: "deadlock"},
	}}
	s := obsTestServer(store)

	rec := grafanaGet(t, s, "/.cornus/v1/obs/tempo/api/traces/abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Batches []struct {
			Resource struct {
				Attributes []struct {
					Key   string         `json:"key"`
					Value map[string]any `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	// One batch per service, sorted.
	if len(got.Batches) != 2 {
		t.Fatalf("got %d batches, want one per service", len(got.Batches))
	}
	if svc := got.Batches[0].Resource.Attributes[0].Value["stringValue"]; svc != "db" {
		t.Errorf("first batch service = %v, want db (sorted)", svc)
	}

	dbSpan := got.Batches[0].ScopeSpans[0].Spans[0]
	// Nanosecond timestamps must be decimal STRINGS: they exceed the range JSON
	// numbers represent exactly, so emitting them as numbers corrupts them.
	if ts, ok := dbSpan["startTimeUnixNano"].(string); !ok || ts != "1700000001000000000" {
		t.Errorf("startTimeUnixNano = %v (%T), want a decimal string", dbSpan["startTimeUnixNano"], dbSpan["startTimeUnixNano"])
	}
	if end, ok := dbSpan["endTimeUnixNano"].(string); !ok || end != "1700000002000000000" {
		t.Errorf("endTimeUnixNano = %v, want start + duration", dbSpan["endTimeUnixNano"])
	}
	if dbSpan["parentSpanId"] != "root" {
		t.Errorf("parentSpanId = %v", dbSpan["parentSpanId"])
	}
	if kind, ok := dbSpan["kind"].(float64); !ok || int(kind) != 3 {
		t.Errorf("kind = %v, want 3 (CLIENT)", dbSpan["kind"])
	}
	status, _ := dbSpan["status"].(map[string]any)
	if code, _ := status["code"].(float64); int(code) != 2 {
		t.Errorf("status code = %v, want 2 (ERROR)", status["code"])
	}
	if status["message"] != "deadlock" {
		t.Errorf("status message = %v", status["message"])
	}

	// A root span carries no parentSpanId at all, rather than an empty one.
	rootSpan := got.Batches[1].ScopeSpans[0].Spans[0]
	if _, present := rootSpan["parentSpanId"]; present {
		t.Errorf("root span carries a parentSpanId: %v", rootSpan["parentSpanId"])
	}
}

// TestTempoUnknownTraceIs404 so Grafana renders "trace not found" rather than a
// broken datasource.
func TestTempoUnknownTraceIs404(t *testing.T) {
	s := obsTestServer(&grafanaStore{})
	rec := grafanaGet(t, s, "/.cornus/v1/obs/tempo/api/traces/missing")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestGrafanaErrorsUseTheDatasourceShape: a failure has to reach the panel as a
// message, which means the error envelope, not the plain one the native routes
// use.
func TestGrafanaErrorsUseTheDatasourceShape(t *testing.T) {
	s := obsTestServer(&grafanaStore{})
	for _, target := range []string{
		"/.cornus/v1/obs/prom/api/v1/query_range",                                        // no query
		"/.cornus/v1/obs/prom/api/v1/query_range?query=up&start=nope",                    // bad time
		"/.cornus/v1/obs/prom/api/v1/query_range?query=up&start=1700000000&end=1&step=1", // end before start
		"/.cornus/v1/obs/loki/api/v1/query_range",                                        // no query
	} {
		rec := grafanaGet(t, s, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, rec.Code)
			continue
		}
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Errorf("GET %s: body is not JSON: %v", target, err)
			continue
		}
		if got["status"] != "error" || got["error"] == "" {
			t.Errorf("GET %s: error envelope = %v", target, got)
		}
	}
}

// TestPromRejectionCarriesTheDiagnostic: the engine rejects out-of-profile PromQL
// with a reason, and that reason is the useful answer for whoever wrote the panel.
func TestPromRejectionCarriesTheDiagnostic(t *testing.T) {
	s := obsTestServer(&grafanaStore{promErr: &obsError{"unsupported function holt_winters"}})
	rec := grafanaGet(t, s, "/.cornus/v1/obs/prom/api/v1/query_range?query=holt_winters(x)")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !strings.Contains(got["error"], "holt_winters") {
		t.Errorf("error %q does not carry the engine's diagnostic", got["error"])
	}
}

func TestTempoSpanKindMapping(t *testing.T) {
	cases := map[string]int{
		"internal": 1, "server": 2, "client": 3, "producer": 4, "consumer": 5,
		"SPAN_KIND_SERVER": 2, "": 0, "nonsense": 0,
	}
	for in, want := range cases {
		if got := tempoSpanKind(in); got != want {
			t.Errorf("tempoSpanKind(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTempoStatusCodeMapping(t *testing.T) {
	cases := map[string]int{"ok": 1, "OK": 1, "error": 2, "": 0, "unset": 0}
	for in, want := range cases {
		if got := tempoStatusCode(in); got != want {
			t.Errorf("tempoStatusCode(%q) = %d, want %d", in, got, want)
		}
	}
}
