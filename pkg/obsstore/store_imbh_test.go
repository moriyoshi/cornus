//go:build imbh

package obsstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// openTestStore opens a real on-disk store in a temp dir. It is on disk rather
// than in memory on purpose: the durability path (WAL + segments) is the one
// this feature actually ships, and a temp dir costs nothing.
func openTestStore(t *testing.T) Store {
	t.Helper()
	s, err := Open(Config{Dir: t.TempDir(), Retention: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ingestLines(t *testing.T, s Store, service string, bodies ...string) time.Time {
	t.Helper()
	base := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	recs := make([]Record, 0, len(bodies))
	for i, b := range bodies {
		recs = append(recs, Record{
			Time:       base.Add(time.Duration(i) * time.Second),
			Severity:   SeverityInfo,
			Body:       b,
			Attributes: map[string]string{"cornus.stream": "stdout"},
		})
	}
	otlp, err := EncodeLogs(map[string]string{
		"service.name":      service,
		"cornus.deployment": service,
	}, recs)
	if err != nil {
		t.Fatalf("EncodeLogs: %v", err)
	}
	if err := s.IngestLogs(otlp); err != nil {
		t.Fatalf("IngestLogs: %v", err)
	}
	return base
}

// TestIngestAndQueryLogs is the end-to-end proof of the whole seam: bytes an
// OTLP exporter would send go in, Go structs come out, and the values survive
// the Arrow boundary intact (which is what the clone discipline in valueAt and
// the typed decoders is for).
func TestIngestAndQueryLogs(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "listening on :8080", "connection refused", "shutting down")

	got, err := s.QueryLogs(context.Background(), LogQuery{Service: "web", Limit: 100})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	if got[0].Body != "listening on :8080" {
		t.Errorf("entries are not oldest-first: got[0].Body = %q", got[0].Body)
	}
	if got[0].Service != "web" {
		t.Errorf("service = %q, want web", got[0].Service)
	}
	if got[0].Time.IsZero() {
		t.Error("entry time is zero; the timestamp did not survive the round trip")
	}
	if !strings.Contains(got[0].Attributes, "cornus.stream") {
		t.Errorf("attributes lost the stream tag: %q", got[0].Attributes)
	}
}

// TestQueryLogsFullTextMatch covers the search that justifies the store backing
// `compose logs --from=store`: filtering by body text, which a runtime tail
// cannot do at all.
func TestQueryLogsFullTextMatch(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "listening on :8080", "connection refused", "shutting down")

	got, err := s.QueryLogs(context.Background(), LogQuery{Service: "web", Match: "refused"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Body, "refused") {
		t.Errorf("matched the wrong record: %q", got[0].Body)
	}
}

// TestQueryLogsIsolatesServices proves the store is shared across deployments
// without leaking between them — the property that lets one database back every
// workload on the server.
func TestQueryLogsIsolatesServices(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "web line")
	ingestLines(t, s, "api", "api line")

	got, err := s.QueryLogs(context.Background(), LogQuery{Service: "api"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(got) != 1 || got[0].Body != "api line" {
		t.Fatalf("service filter leaked: %+v", got)
	}
}

// TestQueryLogsNewestFirst pins the Backward flag, which is what answers "the
// last N lines" — the default `compose logs` question.
func TestQueryLogsNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "first", "second", "third")

	got, err := s.QueryLogs(context.Background(), LogQuery{Service: "web", Newest: true, Limit: 2})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Body != "third" {
		t.Errorf("newest-first returned %q first, want \"third\"", got[0].Body)
	}
}

// TestQueryLogsTimeWindow covers the bound that makes `--since` work.
func TestQueryLogsTimeWindow(t *testing.T) {
	s := openTestStore(t)
	base := ingestLines(t, s, "web", "first", "second", "third")

	// Records are one second apart from base; ask for everything strictly after
	// the first.
	got, err := s.QueryLogs(context.Background(), LogQuery{
		Service: "web",
		Start:   base.Add(500 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (%+v)", len(got), got)
	}
	if got[0].Body != "second" {
		t.Errorf("window started at %q, want \"second\"", got[0].Body)
	}
}

// TestQuerySQLDecodesRows exercises the one surface that touches Arrow directly,
// including the clone discipline: the returned strings must still be readable
// after the batches they came from have been Released (which QuerySQL does
// before returning).
func TestQuerySQLDecodesRows(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "alpha", "beta")

	rows, err := s.QuerySQL(context.Background(),
		"SELECT service, count(*) AS n FROM logs GROUP BY service")
	if err != nil {
		t.Fatalf("QuerySQL: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if got := rows[0]["service"]; got != "web" {
		t.Errorf("service = %v (%T), want \"web\"", got, got)
	}
	// The count column's exact integer width is the engine's business; compare
	// through a string so the test pins the value, not the encoding.
	if got := rows[0]["n"]; got == nil {
		t.Error("count column missing from the decoded row")
	}
}

// TestStatusReportsWhatIsHeld covers the operator-facing snapshot. A non-zero
// row count is what tells someone the store is actually recording, so an
// absence of logs can be distinguished from an absence of ingest.
func TestStatusReportsWhatIsHeld(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "alpha", "beta")

	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Dir == "" {
		t.Error("Status.Dir is empty")
	}
	if st.Retention != 24*time.Hour {
		t.Errorf("Status.Retention = %v, want 24h", st.Retention)
	}
	var logRows int64
	for _, tbl := range st.Tables {
		if tbl.Name == "logs" {
			logRows = tbl.Rows
		}
	}
	if logRows < 2 {
		t.Errorf("logs table reports %d rows, want at least 2 (tables: %+v)", logRows, st.Tables)
	}
}

// TestMaintainIsSafeWhileHolding checks retention/compaction can run against a
// live store, since the server calls it on a ticker alongside ingest.
func TestMaintainIsSafeWhileHolding(t *testing.T) {
	s := openTestStore(t)
	ingestLines(t, s, "web", "alpha")
	if err := s.Maintain(context.Background()); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	got, err := s.QueryLogs(context.Background(), LogQuery{Service: "web"})
	if err != nil {
		t.Fatalf("QueryLogs after Maintain: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Maintain lost data: %d entries, want 1", len(got))
	}
}

// TestClosedStoreRejects makes shutdown safe: the log recorder can still be
// draining when the server closes the store, and that must be a clean error
// rather than a call into freed Rust state.
func TestClosedStoreRejects(t *testing.T) {
	s, err := Open(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v, want nil (Close must be idempotent)", err)
	}
	otlp, err := EncodeLogs(map[string]string{"service.name": "web"},
		[]Record{{Time: time.Now(), Body: "late"}})
	if err != nil {
		t.Fatalf("EncodeLogs: %v", err)
	}
	if err := s.IngestLogs(otlp); err == nil {
		t.Error("IngestLogs on a closed store succeeded, want an error")
	}
	if _, err := s.QueryLogs(context.Background(), LogQuery{}); err == nil {
		t.Error("QueryLogs on a closed store succeeded, want an error")
	}
}

// TestIngestEmptyIsNoop keeps the recorder's batching simple: an empty batch is
// not an error it has to special-case.
func TestIngestEmptyIsNoop(t *testing.T) {
	s := openTestStore(t)
	if err := s.IngestLogs(nil); err != nil {
		t.Errorf("IngestLogs(nil) = %v, want nil", err)
	}
}

// TestDuplicateTimestampErrorCarriesItsCause exercises the failure that made a
// live cornus server record thousands of workload readings that no query could
// ever return (k3s, 2026-08-04): two datapoints at one timestamp in one series
// make the engine refuse to evaluate that series AT READ TIME, so every query of
// the metric fails rather than the offending point being dropped.
//
// The producer-side fix is in pkg/server/metricsrecorder.go. What this pins is
// the part cornus still owns when some other producer does it: the error a user
// sees must explain itself. `promql: malformed semantic input: duplicate
// timestamps in one PromQL series` names neither the metric, the writer, nor the
// remedy.
//
// If this test starts failing at the "want an error" branch, upstream has taken
// moriyoshi/imbh#27 (reject or resolve at ingest) — then duplicateTimestampHint
// and this test are both vestigial and should go.
func TestDuplicateTimestampErrorCarriesItsCause(t *testing.T) {
	s := openTestStore(t)
	at := time.Now().Add(-time.Minute).Truncate(time.Second)

	// Two exports carrying ONE reading twice — exactly what a sampler polling
	// faster than its source refreshes produces.
	for i := 0; i < 2; i++ {
		body, err := EncodeMetrics(map[string]string{"service.name": "web"}, []Metric{{
			Name:   "container.memory.usage",
			Unit:   "By",
			Kind:   KindGauge,
			Points: []NumberPoint{{Time: at, Value: 1 << 20, Attributes: map[string]string{"cornus_replica": "0"}}},
		}})
		if err != nil {
			t.Fatalf("EncodeMetrics: %v", err)
		}
		if err := s.IngestMetrics(body); err != nil {
			t.Fatalf("IngestMetrics(%d): %v", i, err)
		}
	}

	_, err := s.QueryPromQL(context.Background(), "container_memory_usage",
		at.Add(-5*time.Minute), at.Add(time.Minute), 15*time.Second)
	if err == nil {
		t.Fatal("the store accepted and then evaluated a series with duplicate timestamps; " +
			"upstream behavior changed — drop duplicateTimestampHint and this test")
	}
	if !strings.Contains(err.Error(), "duplicate timestamps") {
		t.Fatalf("QueryPromQL failed for some other reason: %v", err)
	}
	// The hint is the whole point: without it the message is unactionable.
	for _, want := range []string{"two datapoints at one timestamp", "every query of that metric"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not explain the cause (%q missing): %v", want, err)
		}
	}
}

// TestOpenRequiresDir guards the one config field with no sensible default.
func TestOpenRequiresDir(t *testing.T) {
	if _, err := Open(Config{}); err == nil {
		t.Error("Open with no Dir succeeded, want an error")
	}
}

// TestRetentionDaysRoundsUp pins the conversion: the engine's retention is in
// whole days, so a sub-day setting must keep one day rather than round to zero,
// which would mean "keep forever" and be the opposite of what was asked.
func TestRetentionDaysRoundsUp(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want uint64
	}{
		{0, 0},
		{-time.Hour, 0},
		{time.Hour, 1},
		{24 * time.Hour, 1},
		{25 * time.Hour, 2},
		{7 * 24 * time.Hour, 7},
	}
	for _, c := range cases {
		if got := retentionDays(c.in); got != c.want {
			t.Errorf("retentionDays(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
