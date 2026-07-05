package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"google.golang.org/protobuf/proto"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"cornus/pkg/api"
	"cornus/pkg/obsstore"
)

// --- fakes ------------------------------------------------------------------

// fakeObsStore is an in-memory obsstore.Store that decodes what was ingested, so
// the recorder's tests assert on real OTLP bytes rather than on an internal
// shortcut. It runs in the default build, which is the point: the recorder is
// pure Go and stays covered even where the engine is not compiled in.
// recordedEntry is a decoded entry plus the record attributes the tests assert
// on directly (obsstore.LogEntry carries them as an opaque JSON string).
type recordedEntry struct {
	obsstore.LogEntry
	Replica string
}

type fakeObsStore struct {
	mu      sync.Mutex
	entries []recordedEntry
	// newest, when set, is what QueryLogs returns for a newest-first limit-1
	// query, which is how the recorder computes its resume point.
	newest map[string]time.Time
	// failWith, when set, is returned by every IngestLogs call.
	failWith error
	// queries records the LogQuery values the recorder issued.
	queries []obsstore.LogQuery
}

func (f *fakeObsStore) IngestLogs(otlp []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	var req collogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(otlp, &req); err != nil {
		return fmt.Errorf("fake store got bytes that are not OTLP: %w", err)
	}
	for _, rl := range req.ResourceLogs {
		res := map[string]string{}
		for _, kv := range rl.Resource.GetAttributes() {
			res[kv.Key] = kv.Value.GetStringValue()
		}
		for _, sl := range rl.ScopeLogs {
			for _, rec := range sl.LogRecords {
				attrs := map[string]string{}
				for _, kv := range rec.Attributes {
					attrs[kv.Key] = kv.Value.GetStringValue()
				}
				f.entries = append(f.entries, recordedEntry{
					LogEntry: obsstore.LogEntry{
						Time:       time.Unix(0, int64(rec.TimeUnixNano)).UTC(),
						Service:    res["service.name"],
						Body:       rec.Body.GetStringValue(),
						Attributes: fmt.Sprintf("%v|%v", res["cornus.deployment"], attrs["cornus.stream"]),
					},
					Replica: attrs[attrReplica],
				})
			}
		}
	}
	return nil
}

func (f *fakeObsStore) snapshot() []recordedEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedEntry(nil), f.entries...)
}

func (f *fakeObsStore) IngestTraces([]byte) error  { return nil }
func (f *fakeObsStore) IngestMetrics([]byte) error { return nil }

func (f *fakeObsStore) QueryLogs(_ context.Context, q obsstore.LogQuery) ([]obsstore.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if t, ok := f.newest[q.Service]; ok {
		return []obsstore.LogEntry{{Time: t, Service: q.Service}}, nil
	}
	return nil, nil
}

func (f *fakeObsStore) SearchTraces(context.Context, obsstore.TraceQuery) ([]obsstore.TraceSummary, error) {
	return nil, nil
}
func (f *fakeObsStore) TraceSpans(context.Context, string) ([]obsstore.Span, error) { return nil, nil }
func (f *fakeObsStore) QueryPromQL(context.Context, string, time.Time, time.Time, time.Duration) ([]obsstore.Series, error) {
	return nil, nil
}
func (f *fakeObsStore) QueryLogQL(context.Context, string, time.Time, time.Time, int) ([]obsstore.LogEntry, error) {
	return nil, nil
}
func (f *fakeObsStore) QueryTraceQL(context.Context, string, time.Time, time.Time) ([]obsstore.TraceMatch, error) {
	return nil, nil
}
func (f *fakeObsStore) QuerySQL(context.Context, string) ([]obsstore.Row, error) { return nil, nil }
func (f *fakeObsStore) Status(context.Context) (obsstore.Status, error) {
	return obsstore.Status{}, nil
}
func (f *fakeObsStore) Maintain(context.Context) error { return nil }
func (f *fakeObsStore) Close() error                   { return nil }

// fakeLogSource writes canned stdcopy frames, standing in for a deploy backend.
type fakeLogSource struct {
	mu   sync.Mutex
	list []api.DeployStatus
	// write emits one workload's frames. Returning ends the stream, which is
	// what a container exit looks like.
	write func(name string, w io.Writer)
	// seen records the LogOptions each Logs call received.
	seen []api.LogOptions
	// attached counts concurrent Logs calls, so a test can watch tails start
	// and stop.
	attached sync.WaitGroup
	blockCh  chan struct{}
}

func (f *fakeLogSource) List(context.Context) ([]api.DeployStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.DeployStatus(nil), f.list...), nil
}

func (f *fakeLogSource) setList(l []api.DeployStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = l
}

func (f *fakeLogSource) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	f.mu.Lock()
	f.seen = append(f.seen, opts)
	f.mu.Unlock()
	if f.write != nil {
		f.write(name, w)
	}
	if f.blockCh != nil {
		// Hold the stream open so a test can observe an attached tail.
		select {
		case <-ctx.Done():
		case <-f.blockCh:
		}
	}
	return io.EOF
}

func (f *fakeLogSource) options() []api.LogOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.LogOptions(nil), f.seen...)
}

// writeFrames emits lines on the requested stream with docker's timestamp prefix.
func writeFrames(w io.Writer, stream stdcopy.StdType, at time.Time, lines ...string) {
	sw := stdcopy.NewStdWriter(w, stream)
	for i, l := range lines {
		fmt.Fprintf(sw, "%s %s\n", at.Add(time.Duration(i)*time.Second).UTC().Format(time.RFC3339Nano), l)
	}
}

func newTestRecorder(store obsstore.Store, src logSource) *logRecorder {
	// The production accept path stores AND re-exports; here the store is the
	// only sink, which is what the assertions read back.
	accept := func(_ string, body []byte) error { return store.IngestLogs(body) }
	r := newLogRecorder(store, accept, func() (logSource, error) { return src, nil })
	// Keep the timers far shorter than the defaults so tests do not sleep.
	r.reconcileInterval = 10 * time.Millisecond
	r.flushInterval = 5 * time.Millisecond
	r.backoff = 5 * time.Millisecond
	return r
}

// --- tests ------------------------------------------------------------------

// TestRecorderRecordsBothStreams is the core of Tier 1: bytes a container wrote
// become queryable records, tagged with which stream carried them and stamped
// with the runtime's timestamp rather than the moment cornus read them.
func TestRecorderRecordsBothStreams(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &fakeObsStore{}
	src := &fakeLogSource{write: func(_ string, w io.Writer) {
		writeFrames(w, stdcopy.Stdout, base, "listening on :8080", "ready")
		writeFrames(w, stdcopy.Stderr, base.Add(time.Minute), "connection refused")
	}}
	r := newTestRecorder(store, src)

	// A stream that ends cleanly is not an error: the container exiting is the
	// normal way a tail finishes, and the caller re-attaches.
	if err := r.stream(context.Background(), src, "web", 0, time.Time{},
		resourceAttrsFor(api.DeployStatus{Name: "web", Backend: "dockerhost"})); err != nil {
		t.Fatalf("stream error = %v, want nil for a clean end", err)
	}

	got := store.snapshot()
	if len(got) != 3 {
		t.Fatalf("recorded %d entries, want 3: %+v", len(got), got)
	}
	bodies := map[string]string{} // body -> "deployment|stream"
	for _, e := range got {
		bodies[e.Body] = e.Attributes
		if e.Service != "web" {
			t.Errorf("entry %q has service %q, want web", e.Body, e.Service)
		}
	}
	if bodies["listening on :8080"] != "web|stdout" {
		t.Errorf("stdout line tagged %q", bodies["listening on :8080"])
	}
	if bodies["connection refused"] != "web|stderr" {
		t.Errorf("stderr line tagged %q", bodies["connection refused"])
	}

	// The runtime's timestamp must survive, not be replaced by ingest time.
	for _, e := range got {
		if e.Body == "listening on :8080" && !e.Time.Equal(base) {
			t.Errorf("timestamp = %v, want the runtime's %v", e.Time, base)
		}
	}
}

// TestRecorderAsksForTimestamps pins the option that makes this a recorder
// rather than a transcript. Without Timestamps every line would be stamped with
// when cornus happened to read it, and the resume point would drift forward on
// every reattach.
func TestRecorderAsksForTimestamps(t *testing.T) {
	store := &fakeObsStore{}
	src := &fakeLogSource{write: func(string, io.Writer) {}}
	r := newTestRecorder(store, src)

	_ = r.stream(context.Background(), src, "web", 0, time.Time{}, map[string]string{"service.name": "web"})

	opts := src.options()
	if len(opts) != 1 {
		t.Fatalf("Logs called %d times, want 1", len(opts))
	}
	if !opts[0].Timestamps {
		t.Error("Logs was called without Timestamps")
	}
	if !opts[0].Follow {
		t.Error("Logs was called without Follow; the recorder must tail, not snapshot")
	}
	if opts[0].Since != "" {
		t.Errorf("Since = %q for a first attach, want empty (take everything the runtime has)", opts[0].Since)
	}
}

// TestRecorderResumesFromTheStore covers the restart boundary: on reattach the
// recorder must ask the runtime only for what it does not already hold, or every
// server restart would re-record the whole retained log.
func TestRecorderResumesFromTheStore(t *testing.T) {
	mark := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeObsStore{newest: map[string]time.Time{"web": mark}}
	src := &fakeLogSource{write: func(string, io.Writer) {}}
	r := newTestRecorder(store, src)

	since := r.resumeSince(context.Background(), "web", 0)
	if !since.Equal(mark) {
		t.Fatalf("resumeSince = %v, want %v", since, mark)
	}

	_ = r.stream(context.Background(), src, "web", 0, since, map[string]string{"service.name": "web"})
	opts := src.options()
	if len(opts) != 1 {
		t.Fatalf("Logs called %d times, want 1", len(opts))
	}
	want := mark.Format(time.RFC3339Nano)
	if opts[0].Since != want {
		t.Errorf("Since = %q, want %q", opts[0].Since, want)
	}

	// The resume query must be the cheap one: newest-first, limited to a single
	// row. Scanning the whole retained log on every reattach would make restarts
	// quadratic in retained volume.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.queries) != 1 {
		t.Fatalf("resume issued %d queries, want 1", len(store.queries))
	}
	q := store.queries[0]
	if !q.Newest || q.Limit != 1 || q.Service != "web" {
		t.Errorf("resume query = %+v, want {Service: web, Newest: true, Limit: 1}", q)
	}
}

// TestRecorderSplitsLinesAcrossWrites covers the framing reality: StdCopy hands
// the sink frame-sized chunks, not lines, so a single log line routinely arrives
// in pieces and must not be recorded as several.
func TestRecorderSplitsLinesAcrossWrites(t *testing.T) {
	store := &fakeObsStore{}
	r := newTestRecorder(store, &fakeLogSource{})
	sink := newLineSink(r, map[string]string{"service.name": "web"}, "stdout", 0)

	sink.Write([]byte("2026-01-02T03:04:05Z hello "))
	sink.Write([]byte("world\n2026-01-02T03:04:06Z second\n"))
	sink.Close(context.Background())

	got := store.snapshot()
	if len(got) != 2 {
		t.Fatalf("recorded %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Body != "hello world" {
		t.Errorf("split line reassembled as %q, want \"hello world\"", got[0].Body)
	}
	if got[1].Body != "second" {
		t.Errorf("second line = %q", got[1].Body)
	}
}

// TestRecorderFlushesTrailingPartialLine matters because a container's last
// output before it dies very often has no trailing newline — which is exactly
// the line someone is looking for.
func TestRecorderFlushesTrailingPartialLine(t *testing.T) {
	store := &fakeObsStore{}
	r := newTestRecorder(store, &fakeLogSource{})
	sink := newLineSink(r, map[string]string{"service.name": "web"}, "stdout", 0)

	sink.Write([]byte("2026-01-02T03:04:05Z panic: nil map"))
	sink.Close(context.Background())

	got := store.snapshot()
	if len(got) != 1 {
		t.Fatalf("recorded %d entries, want 1 (the unterminated final line): %+v", len(got), got)
	}
	if got[0].Body != "panic: nil map" {
		t.Errorf("body = %q", got[0].Body)
	}
}

// TestRecorderCapsAnUnterminatedLine keeps a workload that prints without
// newlines from growing the server's buffer without bound.
func TestRecorderCapsAnUnterminatedLine(t *testing.T) {
	store := &fakeObsStore{}
	r := newTestRecorder(store, &fakeLogSource{})
	sink := newLineSink(r, map[string]string{"service.name": "web"}, "stdout", 0)

	sink.Write([]byte(strings.Repeat("x", logMaxLineBytes+100)))
	sink.Close(context.Background())

	got := store.snapshot()
	if len(got) < 2 {
		t.Fatalf("recorded %d entries, want the capped chunk plus its remainder", len(got))
	}
	if len(got[0].Body) != logMaxLineBytes {
		t.Errorf("first chunk is %d bytes, want the %d-byte cap", len(got[0].Body), logMaxLineBytes)
	}
}

// TestRecorderCountsShedRecords is what lets an operator distinguish a quiet
// workload from a dropped one. An uncounted drop makes an empty query result
// mean two opposite things.
func TestRecorderCountsShedRecords(t *testing.T) {
	store := &fakeObsStore{failWith: obsstore.ErrBackpressure}
	r := newTestRecorder(store, &fakeLogSource{})
	sink := newLineSink(r, map[string]string{"service.name": "web"}, "stdout", 0)

	sink.Write([]byte("2026-01-02T03:04:05Z one\n2026-01-02T03:04:06Z two\n"))
	sink.Close(context.Background())

	if got := r.Dropped(); got != 2 {
		t.Errorf("Dropped() = %d, want 2", got)
	}
}

// TestReconcileStartsAndStopsTails covers the lifecycle: a new deployment gains a
// tail, and a departed one loses it rather than leaking a goroutine holding a
// stream open forever.
func TestReconcileStartsAndStopsTails(t *testing.T) {
	store := &fakeObsStore{}
	src := &fakeLogSource{
		list:    []api.DeployStatus{{Name: "web"}},
		blockCh: make(chan struct{}),
	}
	r := newTestRecorder(store, src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.reconcile(ctx)
	waitFor(t, "one attached tail", func() bool { return r.attached.Load() == 1 })

	r.mu.Lock()
	_, tracked := r.tails[tailKey("web", 0)]
	r.mu.Unlock()
	if !tracked {
		t.Fatal("reconcile did not track a tail for web replica 0")
	}

	// The workload goes away; the next reconcile must drop it.
	src.setList(nil)
	r.reconcile(ctx)
	waitFor(t, "no attached tails", func() bool { return r.attached.Load() == 0 })

	r.mu.Lock()
	n := len(r.tails)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("tails still tracked after the workload went away: %d", n)
	}
}

// TestReconcileIsIdempotent guards against the obvious bug in a poll loop:
// starting a second tail for a workload that already has one, doubling every
// recorded line.
func TestReconcileIsIdempotent(t *testing.T) {
	store := &fakeObsStore{}
	src := &fakeLogSource{
		list:    []api.DeployStatus{{Name: "web"}},
		blockCh: make(chan struct{}),
	}
	r := newTestRecorder(store, src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 5; i++ {
		r.reconcile(ctx)
	}
	waitFor(t, "exactly one attached tail", func() bool { return r.attached.Load() == 1 })
	if got := r.attached.Load(); got != 1 {
		t.Errorf("attached tails = %d after 5 reconciles, want 1", got)
	}
}

// TestResourceAttrsMatchTelemetryWiring pins the join between the two feeds. A
// line recorded off stdout and a span exported by the app's own SDK have to
// agree on service.name and cornus.deployment, or correlating them is manual.
func TestResourceAttrsMatchTelemetryWiring(t *testing.T) {
	res := resourceAttrsFor(api.DeployStatus{Name: "checkout", Backend: "kubernetes"})
	if res["service.name"] != "checkout" {
		t.Errorf("service.name = %q", res["service.name"])
	}
	if res["cornus.deployment"] != "checkout" {
		t.Errorf("cornus.deployment = %q", res["cornus.deployment"])
	}
	if res["telemetry.distro.name"] != "cornus" {
		t.Errorf("telemetry.distro.name = %q", res["telemetry.distro.name"])
	}
	if res["cornus.backend"] != "kubernetes" {
		t.Errorf("cornus.backend = %q", res["cornus.backend"])
	}
}

func TestSplitLogTimestamp(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 600000000, time.UTC)
	cases := []struct {
		name     string
		in       string
		wantTime time.Time
		wantBody string
	}{
		{"docker prefix", when.Format(time.RFC3339Nano) + " hello world", when, "hello world"},
		{"body with spaces", when.Format(time.RFC3339Nano) + " a b c", when, "a b c"},
		{"empty body", when.Format(time.RFC3339Nano) + " ", when, ""},
		{"no timestamp", "just a line", time.Time{}, "just a line"},
		{"single token", "oneword", time.Time{}, "oneword"},
		{"bare timestamp", when.Format(time.RFC3339Nano), when, ""},
		{"timestamp-looking prefix that is not", "2026-99-99T00:00:00Z oops", time.Time{}, "2026-99-99T00:00:00Z oops"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotTime, gotBody := splitLogTimestamp(c.in)
			if !gotTime.Equal(c.wantTime) {
				t.Errorf("time = %v, want %v", gotTime, c.wantTime)
			}
			if gotBody != c.wantBody {
				t.Errorf("body = %q, want %q", gotBody, c.wantBody)
			}
		})
	}
}

// waitFor polls cond until it holds or the test times out, so the concurrency
// tests do not depend on a fixed sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
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

// TestReconcileTailsEveryReplica is the fix for a limitation that mattered far
// more here than in the command it was inherited from: `compose logs` showing one
// replica is a visible partial view a user can work around, but the RECORDER
// silently building an incomplete durable record breaks the store's whole claim
// to hold everything the live tail shows.
func TestReconcileTailsEveryReplica(t *testing.T) {
	store := &fakeObsStore{}
	src := &fakeLogSource{
		list: []api.DeployStatus{{Name: "web", Instances: []api.InstanceStatus{
			{ID: "a", Running: true}, {ID: "b", Running: true}, {ID: "c", Running: true},
		}}},
		blockCh: make(chan struct{}),
	}
	r := newTestRecorder(store, src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.reconcile(ctx)
	waitFor(t, "three attached tails", func() bool { return r.attached.Load() == 3 })

	// Each replica must be asked for by its own index, or two tails would race on
	// the same container and record it twice.
	waitFor(t, "one Logs call per replica", func() bool { return len(src.options()) >= 3 })
	seen := map[int]bool{}
	for _, o := range src.options() {
		seen[o.Instance] = true
	}
	for i := 0; i < 3; i++ {
		if !seen[i] {
			t.Errorf("no tail asked for replica %d; instances seen: %v", i, seen)
		}
	}
}

// TestReconcileStopsTailsOnScaleDown: a departed replica's tail must be dropped,
// not left holding a stream against a container that no longer exists.
func TestReconcileStopsTailsOnScaleDown(t *testing.T) {
	store := &fakeObsStore{}
	src := &fakeLogSource{
		list: []api.DeployStatus{{Name: "web", Instances: []api.InstanceStatus{
			{ID: "a"}, {ID: "b"}, {ID: "c"},
		}}},
		blockCh: make(chan struct{}),
	}
	r := newTestRecorder(store, src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.reconcile(ctx)
	waitFor(t, "three attached tails", func() bool { return r.attached.Load() == 3 })

	src.setList([]api.DeployStatus{{Name: "web", Instances: []api.InstanceStatus{{ID: "a"}}}})
	r.reconcile(ctx)
	waitFor(t, "one attached tail after scale-down", func() bool { return r.attached.Load() == 1 })

	r.mu.Lock()
	n := len(r.tails)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("tails tracked after scale-down = %d, want 1", n)
	}
}

// TestRecorderStampsTheReplica: without it a multi-replica workload's records are
// unattributable, and the per-replica resume watermark cannot be expressed.
func TestRecorderStampsTheReplica(t *testing.T) {
	store := &fakeObsStore{}
	r := newTestRecorder(store, &fakeLogSource{})
	sink := newLineSink(r, map[string]string{"service.name": "web"}, "stdout", 2)
	sink.Write([]byte("2026-01-02T03:04:05Z from replica two\n"))
	sink.Close(context.Background())

	got := store.snapshot()
	if len(got) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(got))
	}
	// It has to be a RECORD attribute: the query surface filters on those, so a
	// resource-level stamp would be invisible to --replica and to the recorder's
	// own resume watermark.
	if got[0].Replica != "2" {
		t.Errorf("record replica = %q, want \"2\"", got[0].Replica)
	}
}

// TestResumeIsPerReplica pins the watermark scoping. A shared one would let a
// chatty replica advance the resume point past a quiet replica's unread lines,
// silently losing them on the next reattach — a data-loss bug that would look
// like nothing at all.
func TestResumeIsPerReplica(t *testing.T) {
	store := &fakeObsStore{}
	r := newTestRecorder(store, &fakeLogSource{})

	r.resumeSince(context.Background(), "web", 3)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.queries) != 1 {
		t.Fatalf("resume issued %d queries, want 1", len(store.queries))
	}
	if got := store.queries[0].Attrs[attrReplica]; got != "3" {
		t.Errorf("resume query scoped to replica %q, want \"3\"", got)
	}
}

// TestTailKeyRoundTrip guards the split that reconcile uses to decide whether a
// tracked tail's replica still exists.
func TestTailKeyRoundTrip(t *testing.T) {
	for _, c := range []struct {
		name string
		idx  int
	}{{"web", 0}, {"api-gateway", 12}, {"a.b-c_d", 3}} {
		name, idx := parseTailKey(tailKey(c.name, c.idx))
		if name != c.name || idx != c.idx {
			t.Errorf("round trip of (%q,%d) = (%q,%d)", c.name, c.idx, name, idx)
		}
	}
}
