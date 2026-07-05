//go:build imbh

package obsstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	imbhgo "github.com/moriyoshi/imbh-go"
)

// Compiled reports that the observability store is linked into this build.
func Compiled() bool { return true }

// store is the IMBH-backed Store. The engine is safe for concurrent use from
// many goroutines, so there is no lock around queries or ingest; the only shared
// mutable state is the closed flag, which exists so a late ingest from the log
// recorder cannot reach a closed handle during shutdown.
type store struct {
	db  *imbhgo.DB
	cfg Config

	mu     sync.RWMutex
	closed bool
}

// Open opens (or creates) the store at cfg.Dir.
func Open(cfg Config) (Store, error) {
	if strings.TrimSpace(cfg.Dir) == "" {
		return nil, errors.New("obsstore: Dir is required")
	}
	opts := imbhgo.DbOptions{
		Path:              cfg.Dir,
		RetentionDays:     retentionDays(cfg.Retention),
		MemoryBudgetBytes: uint64(max64(cfg.MemoryBudgetBytes, 0)),
		MaxDiskBytes:      uint64(max64(cfg.MaxBytes, 0)),
		// Seal the WAL on an interval rather than on every write. Workload
		// telemetry is not the flight recorder: losing the last second of an
		// app's logs to a power cut costs forensic detail, while an fsync per
		// batch would put the store on the hot path of every log line the
		// recorder tails. (pkg/activity makes the opposite trade for 9P mounts,
		// and for the same reason: there, an unfinished record means an effect
		// may still exist.)
		WalMode:       "interval",
		WalIntervalNs: int64(time.Second),
		// Promote the attributes cornus itself stamps on every record, so the
		// queries the CLI and web UI actually run hit dedicated columns rather
		// than JSON extraction.
		PromoteKeys: []string{"cornus.deployment", "cornus.replica", "cornus.stream"},
	}
	db, err := imbhgo.OpenWith(opts)
	if err != nil {
		return nil, fmt.Errorf("obsstore: open %s: %w", cfg.Dir, err)
	}
	if cfg.MaxInFlight > 0 {
		imbhgo.SetMaxInFlight(uint64(cfg.MaxInFlight))
	}
	return &store{db: db, cfg: cfg}, nil
}

// retentionDays converts a duration to IMBH's whole-day retention, rounding UP
// so a sub-day retention keeps one day rather than silently keeping nothing.
func retentionDays(d time.Duration) uint64 {
	if d <= 0 {
		return 0
	}
	const day = 24 * time.Hour
	return uint64((d + day - 1) / day)
}

func max64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}

// handle returns the live DB, or ErrNotCompiled's sibling once closed. Ingest
// and query paths take it so a racing shutdown turns into a clean error rather
// than a call into freed Rust state.
func (s *store) handle() (*imbhgo.DB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errors.New("obsstore: store is closed")
	}
	return s.db, nil
}

func (s *store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.db.Close()
	return nil
}

// --- ingest -----------------------------------------------------------------

// ingest admits one OTLP export-request through the backpressure-aware path.
// The Try* variants are used unconditionally: with no in-flight cap configured
// they never refuse, and with one they shed instead of piling on.
func (s *store) ingest(fn func(*imbhgo.DB, []byte) (imbhgo.Receipt, error), otlp []byte, what string) error {
	if len(otlp) == 0 {
		return nil
	}
	db, err := s.handle()
	if err != nil {
		return err
	}
	r, err := fn(db, otlp)
	if err != nil {
		if errors.Is(err, imbhgo.ErrBackpressure) {
			return ErrBackpressure
		}
		return fmt.Errorf("obsstore: ingest %s: %w", what, err)
	}
	// A partial rejection is a data-quality signal the Status counters carry;
	// only a total rejection is worth failing the call, because then the caller
	// wrote nothing and should know it.
	if r.Accepted == 0 && r.Rejected > 0 {
		return fmt.Errorf("obsstore: ingest %s: all %d records rejected", what, r.Rejected)
	}
	return nil
}

func (s *store) IngestLogs(otlp []byte) error {
	return s.ingest((*imbhgo.DB).TryIngestOTLPLogs, otlp, "logs")
}

func (s *store) IngestTraces(otlp []byte) error {
	return s.ingest((*imbhgo.DB).TryIngestOTLPTraces, otlp, "traces")
}

func (s *store) IngestMetrics(otlp []byte) error {
	return s.ingest((*imbhgo.DB).TryIngestOTLPMetrics, otlp, "metrics")
}

// --- queries ----------------------------------------------------------------

func (s *store) QueryLogs(ctx context.Context, q LogQuery) ([]LogEntry, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	qStart, qEnd := window(q.Start, q.End)
	entries, err := db.QueryLogsTyped(ctx, imbhgo.LogQuery{
		Service:         q.Service,
		Match:           q.Match,
		AttrEq:          q.Attrs,
		Start:           qStart,
		End:             qEnd,
		Limit:           q.Limit,
		Backward:        q.Newest,
		TraceID:         q.TraceID,
		SpanID:          q.SpanID,
		SeverityAtLeast: q.MinSeverity,
	})
	if err != nil {
		return nil, fmt.Errorf("obsstore: query logs: %w", err)
	}
	return convertLogEntries(entries), nil
}

func (s *store) SearchTraces(ctx context.Context, q TraceQuery) ([]TraceSummary, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	qStart, qEnd := window(q.Start, q.End)
	sums, err := db.SearchTraces(ctx, imbhgo.TraceQuery{
		Service:       q.Service,
		Name:          q.Name,
		Status:        q.Status,
		Kind:          q.Kind,
		MinDurationNs: int64(q.MinDuration),
		MaxDurationNs: int64(q.MaxDuration),
		AttrEq:        q.Attrs,
		Start:         qStart,
		End:           qEnd,
		Limit:         int64(q.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("obsstore: search traces: %w", err)
	}
	out := make([]TraceSummary, 0, len(sums))
	for _, t := range sums {
		out = append(out, TraceSummary{
			TraceID:     t.TraceID,
			RootService: t.RootService,
			RootName:    t.RootName,
			Start:       fromNanos(t.StartTime),
			Duration:    time.Duration(t.DurationNs),
			SpanCount:   t.SpanCount,
			Error:       t.Error,
		})
	}
	return out, nil
}

func (s *store) TraceSpans(ctx context.Context, traceID string) ([]Span, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	spans, err := db.GetTraceSpans(ctx, traceID)
	if err != nil {
		return nil, fmt.Errorf("obsstore: trace spans: %w", err)
	}
	out := make([]Span, 0, len(spans))
	for _, sp := range spans {
		out = append(out, Span{
			TraceID:       hex.EncodeToString(sp.TraceID),
			SpanID:        hex.EncodeToString(sp.SpanID),
			ParentSpanID:  hex.EncodeToString(sp.ParentSpanID),
			Name:          sp.Name,
			Kind:          sp.Kind,
			Start:         fromNanos(sp.StartTime),
			Duration:      time.Duration(sp.DurationNs),
			StatusCode:    sp.StatusCode,
			StatusMessage: sp.StatusMessage,
			Service:       sp.Service,
			Attributes:    sp.Attributes,
		})
	}
	return out, nil
}

func (s *store) QueryPromQL(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]Series, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	if step <= 0 {
		return nil, errors.New("obsstore: promql: step must be positive")
	}
	s0, e0 := window(start, end)
	series, err := db.QueryPromQLSeries(ctx, expr, s0, e0, int64(step))
	if err != nil {
		return nil, fmt.Errorf("obsstore: promql: %w", err)
	}
	return convertSeries(series), nil
}

func (s *store) QueryLogQL(ctx context.Context, expr string, start, end time.Time, limit int) ([]LogEntry, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	s0, e0 := window(start, end)
	entries, err := db.QueryLogQLLines(ctx, expr, s0, e0, limit)
	if err != nil {
		return nil, fmt.Errorf("obsstore: logql: %w", err)
	}
	return convertLogEntries(entries), nil
}

func (s *store) QueryTraceQL(ctx context.Context, expr string, start, end time.Time) ([]TraceMatch, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	s0, e0 := window(start, end)
	matches, err := db.QueryTraceQLMatches(ctx, expr, s0, e0)
	if err != nil {
		return nil, fmt.Errorf("obsstore: traceql: %w", err)
	}
	out := make([]TraceMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, TraceMatch{TraceID: m.TraceID, SpanIDs: m.SpanIDs})
	}
	return out, nil
}

// QuerySQL runs raw SQL and materializes every row.
//
// This is the one surface that touches Arrow directly, so it is also the one
// that carries the zero-copy lifetime rules: every batch is Released before the
// next is pulled, and every value read out of a batch is copied first (see
// valueAt). Materializing defeats the engine's lazy streaming, which is the
// deliberate trade for an escape hatch — callers wanting an unbounded scan
// should be reaching for a typed query instead.
func (s *store) QuerySQL(ctx context.Context, sql string) ([]Row, error) {
	db, err := s.handle()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("obsstore: sql: %w", err)
	}
	defer rows.Close()

	var out []Row
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, fmt.Errorf("obsstore: sql: %w", err)
		}
		if !ok {
			break
		}
		schema := rec.Schema()
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			row := make(Row, schema.NumFields())
			for c := 0; c < schema.NumFields(); c++ {
				row[schema.Field(c).Name] = valueAt(rec.Column(c), i)
			}
			out = append(out, row)
		}
		rec.Release()
	}
	return out, rows.Err()
}

func (s *store) Status(ctx context.Context) (Status, error) {
	db, err := s.handle()
	if err != nil {
		return Status{}, err
	}
	st, err := db.Stats()
	if err != nil {
		return Status{}, fmt.Errorf("obsstore: stats: %w", err)
	}
	out := Status{
		Dir:         s.cfg.Dir,
		BufferBytes: int64(st.BufferBytes),
		WALBytes:    int64(st.WalBytes),
		Dropped:     int64(st.IngestDropped),
		Errors:      int64(st.IngestErrors),
		Retention:   s.cfg.Retention,
		MaxBytes:    s.cfg.MaxBytes,
	}
	for _, t := range st.Tables {
		ts := TableStatus{
			Name:     t.Table,
			Rows:     int64(t.SegmentRows + t.BufferRows),
			Segments: int64(t.SegmentCount),
		}
		if t.MinTimeUnixNano != nil {
			ts.Oldest = fromNanos(*t.MinTimeUnixNano)
		}
		if t.MaxTimeUnixNano != nil {
			ts.Newest = fromNanos(*t.MaxTimeUnixNano)
		}
		out.Tables = append(out.Tables, ts)
	}
	return out, nil
}

func (s *store) Maintain(ctx context.Context) error {
	db, err := s.handle()
	if err != nil {
		return err
	}
	if _, err := db.Maintain(); err != nil {
		return fmt.Errorf("obsstore: maintain: %w", err)
	}
	return nil
}

// --- conversions ------------------------------------------------------------

// nanos converts a time to unix nanoseconds, mapping the zero time to 0 so an
// unset query bound stays open-ended rather than becoming year 1.
func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// window converts a query's time bounds, working around an engine behavior that
// would otherwise silently return nothing: a range with a lower bound and no
// upper bound matches no rows (the unset end is read as an end of zero rather
// than as open-ended). An upper bound alone behaves correctly.
//
// So whenever a caller sets only Start — which is exactly what `--since` does,
// the most common query this store will ever serve — the end is filled in with
// the largest representable nanosecond instant. That is genuinely open-ended
// for a nanosecond-resolution store: it is the year 2262, past which no record
// can be stored anyway.
func window(start, end time.Time) (int64, int64) {
	s, e := nanos(start), nanos(end)
	if s != 0 && e == 0 {
		e = math.MaxInt64
	}
	return s, e
}

// fromNanos is nanos' inverse; 0 maps back to the zero time.
func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func convertLogEntries(entries []imbhgo.LogEntry) []LogEntry {
	out := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, LogEntry{
			Time:         fromNanos(e.Time),
			Service:      e.Service,
			Severity:     e.Severity,
			SeverityText: e.SeverityText,
			Body:         e.Body,
			Attributes:   e.Attributes,
			TraceID:      hex.EncodeToString(e.TraceID),
			SpanID:       hex.EncodeToString(e.SpanID),
		})
	}
	return out
}

func convertSeries(series []imbhgo.Series) []Series {
	out := make([]Series, 0, len(series))
	for _, s := range series {
		pts := make([]Point, 0, len(s.Points))
		for _, p := range s.Points {
			pts = append(pts, Point{T: fromNanos(p.T), V: p.V})
		}
		out = append(out, Series{Labels: s.Labels, Points: pts})
	}
	return out
}

// valueAt reads column col at row i into a plain Go value safe to keep after the
// batch is Released.
//
// Every string path clones: arrow-go's string accessors return values that alias
// the engine-owned Arrow buffer without copying, so keeping one past Release is
// a use-after-free. Binary columns render as hex because the only binary columns
// in this schema are trace and span ids, and hex is what every reader wants.
func valueAt(col arrow.Array, i int) any {
	if col == nil || col.IsNull(i) {
		return nil
	}
	switch a := col.(type) {
	case *array.Boolean:
		return a.Value(i)
	case *array.Int8:
		return int64(a.Value(i))
	case *array.Int16:
		return int64(a.Value(i))
	case *array.Int32:
		return int64(a.Value(i))
	case *array.Int64:
		return a.Value(i)
	case *array.Uint8:
		return uint64(a.Value(i))
	case *array.Uint16:
		return uint64(a.Value(i))
	case *array.Uint32:
		return uint64(a.Value(i))
	case *array.Uint64:
		return a.Value(i)
	case *array.Float32:
		return float64(a.Value(i))
	case *array.Float64:
		return a.Value(i)
	case *array.String:
		return strings.Clone(a.Value(i))
	case *array.LargeString:
		return strings.Clone(a.Value(i))
	case *array.StringView:
		return strings.Clone(a.Value(i))
	case *array.Binary:
		return hex.EncodeToString(a.Value(i))
	case *array.LargeBinary:
		return hex.EncodeToString(a.Value(i))
	case *array.BinaryView:
		return hex.EncodeToString(a.Value(i))
	case *array.Timestamp:
		unit := arrow.Nanosecond
		if dt, ok := a.DataType().(*arrow.TimestampType); ok {
			unit = dt.Unit
		}
		return a.Value(i).ToTime(unit).UTC()
	case *array.Dictionary:
		if vals, ok := a.Dictionary().(*array.String); ok {
			return strings.Clone(vals.Value(a.GetValueIndex(i)))
		}
	}
	// Anything else renders through Arrow's own stringifier. Cloning matters
	// here too: ValueStr can return a buffer-aliasing string for the encodings
	// handled above that fall through on an unexpected dictionary value type.
	return strings.Clone(col.ValueStr(i))
}
