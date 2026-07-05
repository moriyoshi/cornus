package webbff

// The observability-store slice of the operation core.
//
// These follow the same contract as the rest of core.go: value-returning,
// ctx-taking methods that hold the join, with the HTTP and MCP surfaces as thin
// adapters. What they add over calling pkg/client directly is the translation of
// one specific failure — a server with no store — into a message that names the
// remedy. An agent that gets "404" concludes it asked the wrong question; an
// agent that gets "this server has no observability store" knows the data does
// not exist and stops digging.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"cornus/pkg/client"
	"cornus/pkg/obsstore"
)

// observeToolLimit bounds what a single MCP query returns.
//
// It is far smaller than the HTTP surface's cap on purpose: these results land in
// a model's context window, where a thousand log lines is not thoroughness but a
// denial of service against the very reasoning that asked for them.
const observeToolLimit = 200

// obsErr maps a store failure into the core's status vocabulary.
func obsErr(err error) error {
	if errors.Is(err, client.ErrObsUnavailable) {
		// 501 rather than 404: the route the caller asked for is right, the
		// capability behind it is absent. A 404 would read as "wrong path".
		return statusErr(http.StatusNotImplemented, "%v", err)
	}
	return err
}

// ObserveLogs searches recorded workload logs.
func (s *Server) ObserveLogs(ctx context.Context, q client.ObsLogQuery) ([]obsstore.LogEntry, error) {
	if q.Limit <= 0 || q.Limit > observeToolLimit {
		q.Limit = observeToolLimit
	}
	out, err := s.client.ObsLogs(ctx, q)
	if err != nil {
		return nil, obsErr(err)
	}
	if out == nil {
		out = []obsstore.LogEntry{}
	}
	return out, nil
}

// ObserveTraces searches recorded traces.
func (s *Server) ObserveTraces(ctx context.Context, q client.ObsTraceQuery) ([]obsstore.TraceSummary, error) {
	if q.Limit <= 0 || q.Limit > observeToolLimit {
		q.Limit = observeToolLimit
	}
	out, err := s.client.ObsTraces(ctx, q)
	if err != nil {
		return nil, obsErr(err)
	}
	if out == nil {
		out = []obsstore.TraceSummary{}
	}
	return out, nil
}

// ObserveTrace returns one trace's spans, already assembled into a parent/child
// forest.
//
// Assembling here rather than returning the flat list is the one place this
// layer deliberately differs from the raw HTTP API: a model reading a flat span
// list has to reconstruct the causality itself, and reconstructing it from
// parentSpanId is exactly the kind of bookkeeping it does unreliably. The HTTP
// route still serves the flat form for callers that want to render it their own
// way.
func (s *Server) ObserveTrace(ctx context.Context, traceID string) ([]*obsstore.Node, error) {
	if traceID == "" {
		return nil, statusErr(http.StatusBadRequest, "trace id is required")
	}
	spans, err := s.client.ObsTrace(ctx, traceID)
	if err != nil {
		return nil, obsErr(err)
	}
	roots := obsstore.AssembleTrace(spans)
	if roots == nil {
		roots = []*obsstore.Node{}
	}
	return roots, nil
}

// ObserveMetrics evaluates a PromQL range query.
func (s *Server) ObserveMetrics(ctx context.Context, expr, since, until string, step time.Duration) ([]obsstore.Series, error) {
	if expr == "" {
		return nil, statusErr(http.StatusBadRequest, "a PromQL query is required")
	}
	out, err := s.client.ObsMetrics(ctx, expr, since, until, step)
	if err != nil {
		return nil, obsErr(err)
	}
	if out == nil {
		out = []obsstore.Series{}
	}
	return out, nil
}

// ObserveStatus reports what the store holds and whether it is shedding.
func (s *Server) ObserveStatus(ctx context.Context) (obsstore.Status, error) {
	st, err := s.client.ObsStatus(ctx)
	if err != nil {
		return obsstore.Status{}, obsErr(err)
	}
	return st, nil
}

// ---- HTTP adapters ----

func (s *Server) handleObserveLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.ObserveLogs(r.Context(), client.ObsLogQuery{
		Service:  q.Get("service"),
		Match:    q.Get("match"),
		Severity: q.Get("severity"),
		Stream:   q.Get("stream"),
		TraceID:  q.Get("trace"),
		Since:    q.Get("since"),
		Until:    q.Get("until"),
		Limit:    atoiDefault(q.Get("limit"), observeToolLimit),
		Newest:   q.Get("newest") != "",
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleObserveTraces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minDur, _ := time.ParseDuration(q.Get("minDuration"))
	out, err := s.ObserveTraces(r.Context(), client.ObsTraceQuery{
		Service:     q.Get("service"),
		Name:        q.Get("name"),
		Status:      q.Get("status"),
		MinDuration: minDur,
		Since:       q.Get("since"),
		Until:       q.Get("until"),
		Limit:       atoiDefault(q.Get("limit"), observeToolLimit),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleObserveTrace(w http.ResponseWriter, r *http.Request) {
	out, err := s.ObserveTrace(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleObserveMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, _ := time.ParseDuration(q.Get("step"))
	out, err := s.ObserveMetrics(r.Context(), q.Get("query"), q.Get("since"), q.Get("until"), step)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleObserveStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.ObserveStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

// atoiDefault parses a positive integer, falling back to def.
func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// obsErrorsURI is the resource form of "what is currently going wrong in the
// workloads".
//
// Like the unfinished-activity resource next to it, this is CONTEXT rather than
// an action: an agent asked why a service is misbehaving should start out already
// holding the recent error output, instead of having to guess that logs exist,
// guess which service, and query. The two are complementary — that resource says
// what CORNUS failed to finish, this one says what the USER'S CODE reported.
const obsErrorsURI = "cornus://observe/errors"

// observeErrorsSince bounds the resource's window. Recent enough to be about the
// present situation, long enough to survive a lunch break.
const observeErrorsSince = "1h"

func (s *Server) observeErrorsJSON(ctx context.Context) ([]byte, error) {
	out, err := s.ObserveLogs(ctx, client.ObsLogQuery{
		Severity: "error",
		Since:    observeErrorsSince,
		Newest:   true,
		Limit:    observeToolLimit,
	})
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(map[string]any{
		"since":   observeErrorsSince,
		"entries": out,
	}, "", "  ")
}
