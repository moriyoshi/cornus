package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cornus/pkg/obsstore"
)

// Grafana-datasource-shaped views of the same store.
//
// These exist because the query languages are already implemented. IMBH answers
// PromQL, LogQL and TraceQL natively, so the only thing standing between a user
// and a real Grafana dashboard over their workloads is the JSON envelope each
// datasource expects. That is a few hundred lines of shaping, against which the
// alternative — cornus growing its own dashboard builder — is not close.
//
// The contract is narrow on purpose: enough of each API for Grafana's datasource
// to issue a range query and render it. Anything outside that returns a clean
// error rather than a plausible-looking approximation, matching how the engine
// itself treats out-of-profile query constructs. A dashboard that silently
// renders subtly wrong data is worse than one that refuses to load.
//
// Point a Prometheus datasource at <server>/.cornus/v1/obs/prom, a Loki
// datasource at .../obs/loki, and a Tempo datasource at .../obs/tempo.

// grafanaSuccess is the envelope Prometheus and Loki share: a status field and a
// data object whose resultType names the shape.
type grafanaSuccess struct {
	Status string      `json:"status"`
	Data   grafanaData `json:"data"`
}

type grafanaData struct {
	ResultType string `json:"resultType"`
	Result     any    `json:"result"`
}

// promSeries is one matrix series in Prometheus's wire form: a label set and
// [unixSeconds, "value"] pairs. The value is a STRING because that is what the
// Prometheus API emits and what Grafana's parser expects; sending a JSON number
// works for small values and silently loses precision for large ones.
type promSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

// writeGrafanaError answers in the error shape both datasources understand, so a
// failure surfaces in Grafana's UI as a message rather than as an empty panel.
func writeGrafanaError(w http.ResponseWriter, code int, errType, msg string) {
	writeJSON(w, code, map[string]string{
		"status":    "error",
		"errorType": errType,
		"error":     msg,
	})
}

// handleObsPromQuery serves the Prometheus range-query API:
// GET /.cornus/v1/obs/prom/api/v1/query_range?query=&start=&end=&step=
func (s *Server) handleObsPromQuery(w http.ResponseWriter, r *http.Request) {
	if !s.allowObsGrafana(w, r) {
		return
	}
	q := r.URL.Query()
	expr := strings.TrimSpace(q.Get("query"))
	if expr == "" {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", "query is required")
		return
	}
	start, end, err := promWindow(q.Get("start"), q.Get("end"))
	if err != nil {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	step, err := promStep(q.Get("step"))
	if err != nil {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}

	series, err := s.obs.QueryPromQL(r.Context(), expr, start, end, step)
	if err != nil {
		// The engine rejects constructs outside its supported PromQL profile with
		// a diagnostic. Passing that through as bad_data puts the real reason in
		// front of whoever wrote the panel.
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}

	out := make([]promSeries, 0, len(series))
	for _, s := range series {
		values := make([][2]any, 0, len(s.Points))
		for _, p := range s.Points {
			values = append(values, [2]any{
				float64(p.T.UnixNano()) / float64(time.Second),
				strconv.FormatFloat(p.V, 'f', -1, 64),
			})
		}
		out = append(out, promSeries{Metric: s.Labels, Values: values})
	}
	writeJSON(w, http.StatusOK, grafanaSuccess{
		Status: "success",
		Data:   grafanaData{ResultType: "matrix", Result: out},
	})
}

// handleObsLokiQuery serves Loki's range-query API:
// GET /.cornus/v1/obs/loki/api/v1/query_range?query=&start=&end=&limit=
//
// Loki has two result shapes and picks between them by what the query is: a bare
// stream selector returns log lines, an aggregation returns a matrix. Cornus
// only serves the lines form here, because that is what the query the engine can
// answer through QueryLogQL returns, and answering a matrix request with lines
// would render as nonsense rather than as an error.
func (s *Server) handleObsLokiQuery(w http.ResponseWriter, r *http.Request) {
	if !s.allowObsGrafana(w, r) {
		return
	}
	q := r.URL.Query()
	expr := strings.TrimSpace(q.Get("query"))
	if expr == "" {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", "query is required")
		return
	}
	// Loki's timestamps are nanoseconds since the epoch, not seconds.
	start, end, err := lokiWindow(q.Get("start"), q.Get("end"))
	if err != nil {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	limit := obsLimit(q.Get("limit"))

	entries, err := s.obs.QueryLogQL(r.Context(), expr, start, end, limit)
	if err != nil {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}

	// Loki groups lines into streams keyed by label set. Group by service, which
	// is the label every record carries, so a multi-service query renders as
	// separate streams rather than one interleaved blob.
	type stream struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	byService := map[string]*stream{}
	var order []string
	for _, e := range entries {
		svc := e.Service
		if svc == "" {
			svc = "unknown"
		}
		st, ok := byService[svc]
		if !ok {
			st = &stream{Stream: map[string]string{"service": svc}}
			byService[svc] = st
			order = append(order, svc)
		}
		st.Values = append(st.Values, [2]string{
			strconv.FormatInt(e.Time.UnixNano(), 10),
			e.Body,
		})
	}
	sort.Strings(order)
	out := make([]*stream, 0, len(order))
	for _, svc := range order {
		out = append(out, byService[svc])
	}
	writeJSON(w, http.StatusOK, grafanaSuccess{
		Status: "success",
		Data:   grafanaData{ResultType: "streams", Result: out},
	})
}

// handleObsTempoTrace serves Tempo's trace-by-id API:
// GET /.cornus/v1/obs/tempo/api/traces/{id}
//
// The response is OTLP-shaped JSON (batches of resourceSpans), which is what
// Tempo returns and what Grafana's trace view parses. Building it from the stored
// spans rather than storing OTLP verbatim keeps one source of truth: the same
// spans the CLI and MCP surfaces read.
func (s *Server) handleObsTempoTrace(w http.ResponseWriter, r *http.Request) {
	if !s.allowObsGrafana(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeGrafanaError(w, http.StatusBadRequest, "bad_data", "trace id is required")
		return
	}
	spans, err := s.obs.TraceSpans(r.Context(), id)
	if err != nil {
		writeGrafanaError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if len(spans) == 0 {
		// Tempo answers 404 for an unknown trace, and Grafana renders that as
		// "trace not found" rather than as a broken datasource.
		writeGrafanaError(w, http.StatusNotFound, "not_found", "trace not found")
		return
	}
	writeJSON(w, http.StatusOK, tempoTrace(spans))
}

// tempoTrace renders stored spans as OTLP/JSON resource batches, one per service.
func tempoTrace(spans []obsstore.Span) map[string]any {
	type kv struct {
		Key   string         `json:"key"`
		Value map[string]any `json:"value"`
	}
	strAttr := func(k, v string) kv {
		return kv{Key: k, Value: map[string]any{"stringValue": v}}
	}

	byService := map[string][]obsstore.Span{}
	var order []string
	for _, sp := range spans {
		svc := sp.Service
		if svc == "" {
			svc = "unknown"
		}
		if _, ok := byService[svc]; !ok {
			order = append(order, svc)
		}
		byService[svc] = append(byService[svc], sp)
	}
	sort.Strings(order)

	batches := make([]map[string]any, 0, len(order))
	for _, svc := range order {
		out := make([]map[string]any, 0, len(byService[svc]))
		for _, sp := range byService[svc] {
			start := sp.Start.UnixNano()
			span := map[string]any{
				"traceId": sp.TraceID,
				"spanId":  sp.SpanID,
				"name":    sp.Name,
				// OTLP/JSON carries nanosecond timestamps as decimal STRINGS,
				// because they exceed the range JSON numbers represent exactly.
				"startTimeUnixNano": strconv.FormatInt(start, 10),
				"endTimeUnixNano":   strconv.FormatInt(start+int64(sp.Duration), 10),
				"kind":              tempoSpanKind(sp.Kind),
			}
			if sp.ParentSpanID != "" {
				span["parentSpanId"] = sp.ParentSpanID
			}
			if sp.StatusCode != "" {
				status := map[string]any{"code": tempoStatusCode(sp.StatusCode)}
				if sp.StatusMessage != "" {
					status["message"] = sp.StatusMessage
				}
				span["status"] = status
			}
			out = append(out, span)
		}
		batches = append(batches, map[string]any{
			"resource": map[string]any{
				"attributes": []kv{strAttr("service.name", svc)},
			},
			"scopeSpans": []map[string]any{{
				"scope": map[string]any{"name": "cornus"},
				"spans": out,
			}},
		})
	}
	return map[string]any{"batches": batches}
}

// tempoSpanKind maps a stored kind name onto OTLP's numeric SpanKind. An
// unrecognized kind becomes UNSPECIFIED rather than guessing.
func tempoSpanKind(kind string) int {
	switch strings.ToLower(strings.TrimPrefix(strings.ToUpper(kind), "SPAN_KIND_")) {
	case "internal":
		return 1
	case "server":
		return 2
	case "client":
		return 3
	case "producer":
		return 4
	case "consumer":
		return 5
	}
	return 0
}

// tempoStatusCode maps a stored status onto OTLP's numeric StatusCode.
func tempoStatusCode(code string) int {
	switch strings.ToLower(code) {
	case "ok":
		return 1
	case "error":
		return 2
	}
	return 0
}

// allowObsGrafana is the method and policy gate for the datasource routes. It
// answers in the Grafana error shape rather than the plain one the native routes
// use, so a permission problem shows up as a message in the panel.
func (s *Server) allowObsGrafana(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeGrafanaError(w, http.StatusMethodNotAllowed, "bad_data", "method not allowed")
		return false
	}
	if !s.apiPolicy.Allow(Identity(r), "observe") {
		writeGrafanaError(w, http.StatusForbidden, "forbidden", "identity not permitted to read workload telemetry")
		return false
	}
	return true
}

// promWindow parses Prometheus's start/end, which are unix SECONDS (possibly
// fractional) or RFC3339. Both datasources send the numeric form; the RFC3339
// form is accepted because the API defines it and hand-written curl uses it.
func promWindow(startS, endS string) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	if endS != "" {
		t, err := parsePromTime(endS)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("end: %w", err)
		}
		end = t
	}
	if startS != "" {
		t, err := parsePromTime(startS)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("start: %w", err)
		}
		start = t
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start must be before end")
	}
	return start, end, nil
}

func parsePromTime(v string) (time.Time, error) {
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Unix(0, int64(secs*float64(time.Second))).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("want unix seconds or RFC3339, got %q", v)
	}
	return t.UTC(), nil
}

// promStep parses Prometheus's step, which is either a duration ("30s") or a
// bare number of seconds.
func promStep(v string) (time.Duration, error) {
	if v == "" {
		return time.Minute, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("step must be positive")
		}
		return d, nil
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || secs <= 0 {
		return 0, fmt.Errorf("step: want a duration like 30s or a positive number of seconds, got %q", v)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// lokiWindow parses Loki's start/end, which are unix NANOSECONDS (or RFC3339).
// The unit difference from Prometheus is the entire reason this is a separate
// function: reusing the Prometheus parser here silently shifts every query by a
// factor of a billion, and the result looks like an empty dashboard rather than
// an error.
func lokiWindow(startS, endS string) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	if endS != "" {
		t, err := parseLokiTime(endS)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("end: %w", err)
		}
		end = t
	}
	if startS != "" {
		t, err := parseLokiTime(startS)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("start: %w", err)
		}
		start = t
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start must be before end")
	}
	return start, end, nil
}

func parseLokiTime(v string) (time.Time, error) {
	if ns, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(0, ns).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("want unix nanoseconds or RFC3339, got %q", v)
	}
	return t.UTC(), nil
}
