package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cornus/pkg/obsstore"
)

// The client side of the built-in observability store.
//
// ErrObsUnavailable is the piece that matters here. The store's routes exist
// only on a server that actually has it, so a query against a server without it
// comes back as a bare 404 — indistinguishable, to a user, from a typo or a
// broken URL. Translating that one status into a named error lets every caller
// print the same actionable sentence instead of "404 page not found".

// ErrObsUnavailable reports that the server has no observability store: either
// it was not enabled (--obs / CORNUS_OBS) or its binary was built without the
// `imbh` tag.
var ErrObsUnavailable = errors.New("this cornus server has no observability store (start it with --obs, and use a build that includes -tags imbh)")

// ObsLogQuery selects recorded log records. Zero fields are omitted, so the
// zero value means "everything the server will give me".
type ObsLogQuery struct {
	Service string
	Match   string
	// Severity is a level name (info, warn, error, ...) or an OTLP severity
	// number; records below it are excluded.
	Severity string
	// Stream filters to "stdout" or "stderr".
	Stream string
	// Replica filters to one instance ordinal of a scaled workload. Empty means
	// every replica, which is what makes the store's answer complete by default.
	Replica string
	TraceID string
	SpanID  string
	// Since and Until accept the same spellings as `compose logs --since`: a Go
	// duration, an RFC3339 instant, or Unix seconds.
	Since string
	Until string
	Limit int
	// Newest returns the most recent records first, which is how "the last N"
	// is asked for.
	Newest bool
}

func (q ObsLogQuery) values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("service", q.Service)
	set("match", q.Match)
	set("severity", q.Severity)
	set("stream", q.Stream)
	set("replica", q.Replica)
	set("trace", q.TraceID)
	set("span", q.SpanID)
	set("since", q.Since)
	set("until", q.Until)
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Newest {
		v.Set("newest", "1")
	}
	return v
}

// ObsLogs returns recorded log records matching q.
func (c *Client) ObsLogs(ctx context.Context, q ObsLogQuery) ([]obsstore.LogEntry, error) {
	var out []obsstore.LogEntry
	err := c.obsGet(ctx, "/.cornus/v1/obs/logs", q.values(), &out)
	return out, err
}

// ObsTraceQuery selects recorded traces.
type ObsTraceQuery struct {
	Service     string
	Name        string
	Status      string
	Kind        string
	MinDuration time.Duration
	MaxDuration time.Duration
	Since       string
	Until       string
	Limit       int
}

func (q ObsTraceQuery) values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("service", q.Service)
	set("name", q.Name)
	set("status", q.Status)
	set("kind", q.Kind)
	set("since", q.Since)
	set("until", q.Until)
	if q.MinDuration > 0 {
		v.Set("minDuration", q.MinDuration.String())
	}
	if q.MaxDuration > 0 {
		v.Set("maxDuration", q.MaxDuration.String())
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return v
}

// ObsTraces returns trace summaries matching q.
func (c *Client) ObsTraces(ctx context.Context, q ObsTraceQuery) ([]obsstore.TraceSummary, error) {
	var out []obsstore.TraceSummary
	err := c.obsGet(ctx, "/.cornus/v1/obs/traces", q.values(), &out)
	return out, err
}

// ObsTrace returns one trace's spans, ordered by start time. Assemble them with
// obsstore.AssembleTrace to render a waterfall.
func (c *Client) ObsTrace(ctx context.Context, traceID string) ([]obsstore.Span, error) {
	var out []obsstore.Span
	err := c.obsGet(ctx, "/.cornus/v1/obs/trace/"+url.PathEscape(traceID), nil, &out)
	return out, err
}

// ObsMetrics evaluates a PromQL range query against the store.
func (c *Client) ObsMetrics(ctx context.Context, expr, since, until string, step time.Duration) ([]obsstore.Series, error) {
	v := url.Values{}
	v.Set("query", expr)
	if since != "" {
		v.Set("since", since)
	}
	if until != "" {
		v.Set("until", until)
	}
	if step > 0 {
		v.Set("step", step.String())
	}
	var out []obsstore.Series
	err := c.obsGet(ctx, "/.cornus/v1/obs/metrics", v, &out)
	return out, err
}

// ObsQuery runs raw SQL against the store.
func (c *Client) ObsQuery(ctx context.Context, sql string) ([]obsstore.Row, error) {
	v := url.Values{}
	v.Set("sql", sql)
	var out []obsstore.Row
	err := c.obsGet(ctx, "/.cornus/v1/obs/query", v, &out)
	return out, err
}

// ObsStatus reports what the store is holding and whether it is shedding.
func (c *Client) ObsStatus(ctx context.Context) (obsstore.Status, error) {
	var out obsstore.Status
	err := c.obsGet(ctx, "/.cornus/v1/obs/status", nil, &out)
	return out, err
}

// ObsAvailable reports whether the server has a live observability store. It is
// a cheap probe callers use to choose a source before committing to it — notably
// `compose logs --from=auto`, which must decide between the store and the
// runtime without turning a missing feature into a visible failure.
func (c *Client) ObsAvailable(ctx context.Context) bool {
	_, err := c.ObsStatus(ctx)
	return err == nil
}

// obsGet performs one store query and decodes the JSON result.
func (c *Client) obsGet(ctx context.Context, path string, q url.Values, out any) error {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// A 404 here is structural, not a missing row: these routes are registered
	// only when the store is live, so their absence IS the answer.
	if resp.StatusCode == http.StatusNotFound {
		return ErrObsUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("observability: decode: %w", err)
	}
	return nil
}
