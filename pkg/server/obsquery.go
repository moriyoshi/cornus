package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"cornus/pkg/deploy"
	"cornus/pkg/obsstore"
)

// The read side of the built-in observability store.
//
// These routes exist only when the store is live (see Server.routes), so a build
// without the `imbh` tag answers 404 rather than pretending. They go through the
// same API policy gate as the rest of the API under the "observe" action:
// recorded telemetry carries workload names, log bodies, and lineage, which is
// at least as sensitive as the deploy status right next to it.
//
// Every response is the plain obsstore type as JSON. Introducing a parallel DTO
// layer here would buy nothing — the store's types are already cornus-owned
// mirrors chosen for exactly this purpose — and would give the wire format a
// second place to drift.

// obsQueryLimit caps how many rows any one query returns when the caller does not
// say. It is a guard against a bare `cornus observe logs` on a busy server
// materializing the whole retention window into memory on both ends.
const obsQueryLimit = 1000

// handleObsLogs serves GET /.cornus/v1/obs/logs.
func (s *Server) handleObsLogs(w http.ResponseWriter, r *http.Request) {
	if !s.allowObs(w, r) {
		return
	}
	q := r.URL.Query()
	start, end, err := obsWindow(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	sev, err := obsSeverity(q.Get("severity"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	query := obsstore.LogQuery{
		Service:     q.Get("service"),
		Match:       q.Get("match"),
		MinSeverity: sev,
		TraceID:     q.Get("trace"),
		SpanID:      q.Get("span"),
		Start:       start,
		End:         end,
		Limit:       obsLimit(q.Get("limit")),
		Newest:      q.Get("newest") != "",
	}
	// Both filters land on attribute columns the store promotes, so they are
	// cheap; an absent filter must leave Attrs nil rather than an empty map, which
	// the engine would read as a predicate that matches nothing.
	attrs := map[string]string{}
	if stream := q.Get("stream"); stream != "" {
		attrs["cornus.stream"] = stream
	}
	if replica := q.Get("replica"); replica != "" {
		attrs["cornus.replica"] = replica
	}
	if len(attrs) > 0 {
		query.Attrs = attrs
	}
	entries, err := s.obs.QueryLogs(r.Context(), query)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// An empty result is `[]`, never `null`: a client that renders "no logs" and
	// one that iterates should not have to special-case the JSON spelling.
	if entries == nil {
		entries = []obsstore.LogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleObsTraces serves GET /.cornus/v1/obs/traces.
func (s *Server) handleObsTraces(w http.ResponseWriter, r *http.Request) {
	if !s.allowObs(w, r) {
		return
	}
	q := r.URL.Query()
	start, end, err := obsWindow(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	minDur, err := obsDuration(q.Get("minDuration"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid minDuration: " + err.Error()})
		return
	}
	maxDur, err := obsDuration(q.Get("maxDuration"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid maxDuration: " + err.Error()})
		return
	}
	out, err := s.obs.SearchTraces(r.Context(), obsstore.TraceQuery{
		Service:     q.Get("service"),
		Name:        q.Get("name"),
		Status:      q.Get("status"),
		Kind:        q.Get("kind"),
		MinDuration: minDur,
		MaxDuration: maxDur,
		Start:       start,
		End:         end,
		Limit:       obsLimit(q.Get("limit")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if out == nil {
		out = []obsstore.TraceSummary{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleObsTrace serves GET /.cornus/v1/obs/trace/{id}: one trace's spans.
//
// It returns the flat span list rather than an assembled tree. The tree is a
// rendering concern with its own ordering and orphan rules (obsstore.AssembleTrace),
// and a caller that wants a flame graph, a table, or a span count should not have
// to undo someone else's nesting.
func (s *Server) handleObsTrace(w http.ResponseWriter, r *http.Request) {
	if !s.allowObs(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace id is required"})
		return
	}
	spans, err := s.obs.TraceSpans(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if spans == nil {
		spans = []obsstore.Span{}
	}
	writeJSON(w, http.StatusOK, spans)
}

// handleObsMetrics serves GET /.cornus/v1/obs/metrics: a PromQL range query.
func (s *Server) handleObsMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.allowObs(w, r) {
		return
	}
	q := r.URL.Query()
	expr := strings.TrimSpace(q.Get("query"))
	if expr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query is required"})
		return
	}
	start, end, err := obsWindow(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	step, err := obsDuration(q.Get("step"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid step: " + err.Error()})
		return
	}
	// A range query needs all three bounds, and defaulting them quietly would
	// produce a plausible-looking series over a window the caller never asked
	// for. Fill only what has an obvious answer.
	if step <= 0 {
		step = time.Minute
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		start = end.Add(-time.Hour)
	}
	series, err := s.obs.QueryPromQL(r.Context(), expr, start, end, step)
	if err != nil {
		// A rejected PromQL expression is the caller's error, not the server's:
		// the engine rejects out-of-profile constructs with a diagnostic rather
		// than approximating them, and that diagnostic is the useful answer.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if series == nil {
		series = []obsstore.Series{}
	}
	writeJSON(w, http.StatusOK, series)
}

// handleObsQuery serves GET /.cornus/v1/obs/query?sql=: the raw SQL escape hatch.
func (s *Server) handleObsQuery(w http.ResponseWriter, r *http.Request) {
	if !s.allowObs(w, r) {
		return
	}
	sql := strings.TrimSpace(r.URL.Query().Get("sql"))
	if sql == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sql is required"})
		return
	}
	rows, err := s.obs.QuerySQL(r.Context(), sql)
	if err != nil {
		// Same reasoning as PromQL: a SQL error describes the caller's query.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []obsstore.Row{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// allowObs applies the method and policy gate shared by every read route,
// answering the client directly and reporting whether the handler should run.
func (s *Server) allowObs(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return false
	}
	if !s.apiPolicy.Allow(Identity(r), "observe") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to read workload telemetry"})
		return false
	}
	return true
}

// obsWindow parses the shared since/until bounds.
//
// It reuses deploy.ParseSince so a time expression means the same thing here as
// it does on `compose logs --since`: a Go duration, an RFC3339 instant, or Unix
// seconds. A user should never have to learn that the store spells time
// differently from the runtime.
func obsWindow(q map[string][]string) (start, end time.Time, err error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	now := time.Now()
	if v := get("since"); v != "" {
		start, err = deploy.ParseSince(v, now)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if v := get("until"); v != "" {
		end, err = deploy.ParseSince(v, now)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	return start, end, nil
}

func obsLimit(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > obsQueryLimit {
		return obsQueryLimit
	}
	return n
}

func obsDuration(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	return time.ParseDuration(v)
}

// obsSeverity maps a severity name (or number) to its OTLP severity number, so a
// caller writes `--severity error` rather than `--severity 17`.
func obsSeverity(v string) (int, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return 0, nil
	}
	switch v {
	case "trace":
		return 1, nil
	case "debug":
		return obsstore.SeverityDebug, nil
	case "info":
		return obsstore.SeverityInfo, nil
	case "warn", "warning":
		return obsstore.SeverityWarn, nil
	case "error":
		return obsstore.SeverityError, nil
	case "fatal":
		return obsstore.SeverityFatal, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 24 {
		return 0, errInvalidSeverity
	}
	return n, nil
}

var errInvalidSeverity = &obsError{"invalid severity: want one of trace, debug, info, warn, error, fatal, or an OTLP severity number 1-24"}

type obsError struct{ msg string }

func (e *obsError) Error() string { return e.msg }
