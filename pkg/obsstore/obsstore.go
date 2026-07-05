// Package obsstore is cornus's built-in observability store: a durable,
// queryable home for a *workload's* telemetry, kept on the server so a user gets
// logs, traces, and metrics without standing up a backend first.
//
// It exists because the collection half was already built and had nowhere to go.
// `x-cornus-telemetry:` runs an embedded OpenTelemetry Collector in the caretaker
// and auto-wires the app to it (see pkg/deploy.BuildTelemetryWiring), but the
// exporter endpoint was mandatory — so the feature only paid off for users who
// already ran Grafana or Datadog. Meanwhile the only log surface a user actually
// got was a live tail that is ephemeral, first-instance-only, and answers nothing
// after the container that produced it is gone.
//
// # The two feeds
//
// Data arrives two ways, and the split is the whole design:
//
//   - Zero-touch: the server tees every managed workload's stdout/stderr from
//     Backend.Logs into the store as OTLP log records. No app instrumentation, no
//     sidecar, no opt-in. This is what earns the store the right to back
//     `compose logs`: it holds everything that command already shows, and keeps
//     holding it after the container is gone.
//
//     Every replica is recorded, one tail per instance, each record carrying its
//     ordinal as cornus.replica. That needed an instance selector on
//     api.LogOptions, which the interface originally lacked — it inherited
//     `docker logs <name>` semantics, where one name means one container.
//
//   - Opt-in: the caretaker's Collector exports the app's own OTLP here when the
//     telemetry spec names no external endpoint. This is the only way to get
//     traces and metrics, which stdout fundamentally cannot carry.
//
// Both feeds land in the same tables with the same resource attributes, so a log
// line recorded from stdout and a span exported by the app's SDK join on
// service.name and cornus.deployment without the reader knowing which feed
// produced which.
//
// # Compiled in, or not
//
// The implementation is IMBH (github.com/moriyoshi/imbh-go), an embeddable
// observability database that ingests OTLP protobuf directly and answers SQL,
// typed queries, and PromQL/LogQL/TraceQL. It is a Rust static library reached
// over cgo, so it cannot be part of the default pure-Go, CGO_ENABLED=0 build.
// It therefore sits behind the `imbh` build tag exactly as the embedded
// Collector sits behind `otelcol`: this file holds the vocabulary and compiles
// everywhere, store_imbh.go holds the implementation, and store_stub.go answers
// ErrNotCompiled so every caller compiles unconditionally and the absence is
// reported rather than silently doing nothing.
//
// # Types are mirrors, not aliases
//
// The types below deliberately restate imbh-go's rather than aliasing them: a
// tagless build must compile with the dependency absent entirely. The boundary
// is also where imbh-go's zero-copy Arrow lifetime rules stop — values crossing
// out of a Store method are owned by the caller and outlive any Arrow batch, so
// store_imbh.go carries the whole Close/Release/clone discipline and nothing
// above this package has to know Arrow exists.
package obsstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotCompiled is returned by every entry point when the store was not linked
// into this build (built without the `imbh` tag). Callers treat it as "the
// feature is absent", not as a failure: the log recorder stays asleep, the
// server registers no /.cornus/v1/obs routes, and a CLI query fails with a
// message naming the remedy.
var ErrNotCompiled = errors.New("obsstore: not compiled in (build with -tags imbh)")

// ErrBackpressure is returned by an ingest call that was refused because the
// store is already at its in-flight cap. It is a shed-load signal, not an error
// to retry tightly: the recorder counts it and drops the batch.
var ErrBackpressure = errors.New("obsstore: backpressure")

// Config configures a store open.
type Config struct {
	// Dir is the on-disk directory holding the database. It lives under the
	// server's DataDir and is never handed to a container runtime, so it needs
	// no host-path translation in the in-container server topology.
	Dir string
	// Retention drops data older than this. Zero keeps everything, bounded only
	// by MaxBytes. IMBH's retention is expressed in whole days, so a shorter
	// duration rounds up to one day rather than silently keeping nothing.
	Retention time.Duration
	// MaxBytes bounds on-disk segment bytes. Zero is unbounded.
	MaxBytes int64
	// MemoryBudgetBytes caps the in-memory ingest buffer. Zero takes the
	// engine default.
	MemoryBudgetBytes int64
	// MaxInFlight caps concurrent admitted operations, which is what makes
	// TryIngest* able to shed rather than pile on. Zero is unbounded.
	//
	// The cap is process-global in the engine, so the last store opened in a
	// process wins. That is fine because a process opens exactly one.
	MaxInFlight int
}

// Severity numbers as OTLP defines them, for callers that want to filter by
// level without importing an OTel package. Only the thresholds a user actually
// asks for on a command line are named.
const (
	SeverityDebug = 5
	SeverityInfo  = 9
	SeverityWarn  = 13
	SeverityError = 17
	SeverityFatal = 21
)

// LogQuery selects log records. Every non-zero field is AND-combined with the
// others.
type LogQuery struct {
	Service string // exact service.name match
	Match   string // full-text term match on the body
	// MinSeverity keeps records at or above this OTLP severity number; zero
	// means unfiltered.
	MinSeverity int
	// TraceID and SpanID correlate logs to one trace or span (hex).
	TraceID string
	SpanID  string
	// Attrs are attribute equality filters.
	Attrs map[string]string
	// Start and End bound event time. A zero Start or End is open-ended.
	Start time.Time
	End   time.Time
	// Limit caps returned rows; zero takes the engine default.
	Limit int
	// Newest returns newest-first. The default is oldest-first, which is what a
	// log reader wants, so a caller only sets this to answer "the last N".
	Newest bool
}

// LogEntry is one recorded log record.
type LogEntry struct {
	Time         time.Time `json:"time"`
	Service      string    `json:"service,omitempty"`
	Severity     uint8     `json:"severity,omitempty"`
	SeverityText string    `json:"severityText,omitempty"`
	Body         string    `json:"body"`
	// Attributes is the record's attribute map as a JSON object string, kept
	// opaque because callers overwhelmingly pass it straight through to a
	// renderer or a JSON response.
	Attributes string `json:"attributes,omitempty"`
	// TraceID and SpanID are hex, or empty when the record carried none.
	TraceID string `json:"traceId,omitempty"`
	SpanID  string `json:"spanId,omitempty"`
}

// TraceQuery selects traces by their spans' properties.
type TraceQuery struct {
	Service     string
	Name        string
	Status      string
	Kind        string
	MinDuration time.Duration
	MaxDuration time.Duration
	Attrs       map[string]string
	Start       time.Time
	End         time.Time
	Limit       int
}

// TraceSummary is one matched trace. RootService and RootName are empty when the
// root span is not itself present in the store.
type TraceSummary struct {
	TraceID     string        `json:"traceId"`
	RootService string        `json:"rootService,omitempty"`
	RootName    string        `json:"rootName,omitempty"`
	Start       time.Time     `json:"start"`
	Duration    time.Duration `json:"duration"`
	SpanCount   int64         `json:"spanCount"`
	Error       bool          `json:"error,omitempty"`
}

// Span is one span of a trace. Ids are hex; ParentSpanID is empty for a root.
type Span struct {
	TraceID       string        `json:"traceId"`
	SpanID        string        `json:"spanId"`
	ParentSpanID  string        `json:"parentSpanId,omitempty"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind,omitempty"`
	Start         time.Time     `json:"start"`
	Duration      time.Duration `json:"duration"`
	StatusCode    string        `json:"statusCode,omitempty"`
	StatusMessage string        `json:"statusMessage,omitempty"`
	Service       string        `json:"service,omitempty"`
	Attributes    string        `json:"attributes,omitempty"` // JSON object string
}

// TraceMatch is a TraceQL result: the trace, and the spans its spanset selected.
type TraceMatch struct {
	TraceID string   `json:"traceId"`
	SpanIDs []string `json:"spanIds,omitempty"`
}

// Point is one metric sample.
type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// Series is one metric time series: a label set and its samples.
type Series struct {
	Labels map[string]string `json:"labels,omitempty"`
	Points []Point           `json:"points,omitempty"`
}

// Row is one row of a raw SQL result, keyed by column name. The SQL surface is
// an escape hatch for questions the typed queries do not cover, so it trades the
// typed shape for reaching anything in the schema.
type Row map[string]any

// TableStatus summarizes one stored table.
type TableStatus struct {
	Name     string    `json:"name"`
	Rows     int64     `json:"rows"`
	Segments int64     `json:"segments"`
	Oldest   time.Time `json:"oldest,omitzero"`
	Newest   time.Time `json:"newest,omitzero"`
}

// Status is an operator-facing snapshot: what the store is holding, how far back
// it reaches, and whether it is dropping anything.
type Status struct {
	Dir         string        `json:"dir"`
	Tables      []TableStatus `json:"tables,omitempty"`
	BufferBytes int64         `json:"bufferBytes"`
	WALBytes    int64         `json:"walBytes"`
	// Dropped and Errors are cumulative ingest counters. A non-zero Dropped is
	// the signal that the store is shedding load, which is the one number an
	// operator needs before trusting an absence of logs.
	Dropped   int64         `json:"dropped"`
	Errors    int64         `json:"errors"`
	Retention time.Duration `json:"retention"`
	MaxBytes  int64         `json:"maxBytes"`
	// Export reports the re-export forwarder's state when one is configured, and
	// is nil otherwise. It lives here rather than in a separate endpoint because
	// the question it answers — "is my telemetry actually getting where I think
	// it is?" — is the same question the rest of Status answers.
	Export *ExportStatus `json:"export,omitempty"`
	// Metrics reports the workload metrics sampler's state when it is running,
	// and is nil otherwise. Same reasoning as Export: "why is this series empty?"
	// is the question Status exists to answer, and for metrics the answer is
	// usually one of these counters rather than anything about the store.
	Metrics *MetricsStatus `json:"metrics,omitempty"`
}

// MetricsStatus describes the zero-touch workload metrics sampler.
//
// The three counters answer three different failure modes that all look
// identical from a query returning nothing:
//
//   - Replicas is how many the last cycle found. Zero means nothing is deployed,
//     and no amount of looking at the store will reveal otherwise.
//   - Failed rising while Sampled stays flat means the backend is refusing —
//     missing RBAC on a kubernetes cluster, an unreachable daemon.
//   - Dropped rising means the readings were taken and then shed, which is a
//     capacity problem rather than a collection one.
type MetricsStatus struct {
	// Interval is the sampling period.
	Interval time.Duration `json:"interval"`
	// Replicas is how many workload instances the most recent cycle covered.
	Replicas int64 `json:"replicas"`
	Sampled  int64 `json:"sampled"`
	Failed   int64 `json:"failed"`
	Dropped  int64 `json:"dropped"`
}

// ExportStatus describes the upstream OTLP backend the server forwards to.
//
// Dropped and Failed mean different things and both matter: Dropped is cornus
// shedding because the forwarder fell behind (the upstream is slow), Failed is
// the upstream refusing or being unreachable (the upstream is broken). Telling
// them apart is the difference between tuning and paging someone.
type ExportStatus struct {
	Endpoint string `json:"endpoint"`
	Sent     int64  `json:"sent"`
	Dropped  int64  `json:"dropped"`
	Failed   int64  `json:"failed"`
}

// Store is the query and ingest surface the rest of cornus codes against.
//
// Ingest takes the raw protobuf bytes an OTLP/HTTP exporter sends (an
// ExportLogsServiceRequest and friends) and hands them straight to the engine —
// no decode, no re-encode. That is why the server's OTLP receiver is a handful
// of lines: the wire format the Collector already speaks is the format the
// engine already reads.
//
// Every query takes a context and honors its cancellation.
type Store interface {
	// IngestLogs, IngestTraces and IngestMetrics admit one OTLP/HTTP
	// export-request. They shed with ErrBackpressure at the in-flight cap
	// rather than queueing without bound.
	IngestLogs(otlp []byte) error
	IngestTraces(otlp []byte) error
	IngestMetrics(otlp []byte) error

	QueryLogs(ctx context.Context, q LogQuery) ([]LogEntry, error)
	SearchTraces(ctx context.Context, q TraceQuery) ([]TraceSummary, error)
	// TraceSpans returns one trace's spans ordered by start time. Assembling
	// them into a tree is the caller's business (see AssembleTrace).
	TraceSpans(ctx context.Context, traceID string) ([]Span, error)

	// The LGTM query languages, which the engine implements as versioned
	// compatibility profiles: a construct outside the profile is rejected with
	// a diagnostic rather than silently approximated.
	QueryPromQL(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]Series, error)
	QueryLogQL(ctx context.Context, expr string, start, end time.Time, limit int) ([]LogEntry, error)
	QueryTraceQL(ctx context.Context, expr string, start, end time.Time) ([]TraceMatch, error)

	QuerySQL(ctx context.Context, sql string) ([]Row, error)

	Status(ctx context.Context) (Status, error)
	// Maintain runs retention and compaction. The server calls it on a ticker;
	// it is safe to call concurrently with ingest and queries.
	Maintain(ctx context.Context) error
	Close() error
}

// Node is a span with its children attached, as returned by AssembleTrace.
type Node struct {
	Span
	Children []*Node
}

// AssembleTrace rebuilds the parent -> child forest from a flat span slice and
// returns its roots, so a renderer can draw a waterfall.
//
// A span becomes a root when it has no parent OR when its parent is not present
// in the input: an orphan surfaces as a root rather than being dropped, because
// a partially-collected trace is exactly when someone is looking. Roots and each
// node's children are sorted by start time then span id, so the output is stable
// regardless of input order. The input is not mutated.
func AssembleTrace(spans []Span) []*Node {
	if len(spans) == 0 {
		return nil
	}
	byID := make(map[string]*Node, len(spans))
	order := make([]*Node, 0, len(spans))
	for _, s := range spans {
		if s.SpanID == "" {
			continue
		}
		if _, dup := byID[s.SpanID]; dup {
			continue // first occurrence wins
		}
		n := &Node{Span: s}
		byID[s.SpanID] = n
		order = append(order, n)
	}
	var roots []*Node
	for _, n := range order {
		parent, ok := byID[n.ParentSpanID]
		if n.ParentSpanID == "" || !ok || parent == n {
			roots = append(roots, n)
			continue
		}
		parent.Children = append(parent.Children, n)
	}
	sortNodes(roots)
	for _, n := range order {
		sortNodes(n.Children)
	}
	return roots
}

// sortNodes orders siblings by start time, breaking ties on span id so the
// result does not depend on map iteration order.
func sortNodes(ns []*Node) {
	if len(ns) < 2 {
		return
	}
	// Insertion sort: sibling counts are small, and this keeps the package
	// dependency-free apart from the standard library it already uses.
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && less(ns[j], ns[j-1]); j-- {
			ns[j], ns[j-1] = ns[j-1], ns[j]
		}
	}
}

func less(a, b *Node) bool {
	if !a.Start.Equal(b.Start) {
		return a.Start.Before(b.Start)
	}
	return a.SpanID < b.SpanID
}
