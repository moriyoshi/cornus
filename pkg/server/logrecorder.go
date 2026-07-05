package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/obsstore"
	"cornus/pkg/supervisor"
)

// The log recorder is the zero-touch half of the built-in observability store:
// it tees every managed workload's stdout/stderr into the store as OTLP log
// records, so an app with no instrumentation at all still gets durable,
// searchable logs.
//
// It exists because a live tail answers the wrong question. `compose logs` shows
// what a container is printing; the question people actually arrive with is what
// it printed before it died. The runtime keeps that only as long as it keeps the
// container, which on a crash-looping or recreated workload is no time at all.
//
// # Why the server and not a sidecar
//
// Every backend already implements Backend.Logs with one framing contract, so
// recording from the server needs no per-backend work, no extra container, and
// no cooperation from the workload. The cost is a standing follow-stream per
// workload, which is why it is a separate switch (ObsRecordLogs) rather than
// implied by opening the store.
//
// # Delivery
//
// At-least-once at a restart boundary. On (re)attaching to a workload the
// recorder resumes from the newest timestamp already stored for it, and the
// runtime's `since` filter is inclusive, so the line exactly on that boundary can
// be recorded twice. That is the deliberate direction to err: a duplicated log
// line is visible and harmless, a dropped one is neither.
//
// Under flooding the store sheds rather than growing the server's heap, and what
// it sheds is counted (see logRecorder.Dropped) so an absence of logs can be told
// apart from an absence of ingest.

// attrReplica is the resource attribute carrying a record's replica ordinal.
// It matches the key pkg/obsstore promotes to a dedicated column.
const attrReplica = "cornus.replica"

const (
	// logReconcileInterval is how often the recorder re-reads the workload list
	// to pick up new deployments and drop departed ones. It also bounds how long
	// a just-deployed workload's first lines can be missed, so it is short.
	logReconcileInterval = 5 * time.Second
	// logFlushInterval bounds how long a recorded line waits before it is
	// queryable. A quiet workload emitting one line a minute must not have that
	// line sit in a buffer until the next one arrives.
	logFlushInterval = time.Second
	// logBatchSize is how many records accumulate before a flush happens
	// regardless of the timer.
	logBatchSize = 256
	// logMaxLineBytes caps one recorded line. A workload that prints a megabyte
	// without a newline is not a reason for the server to hold a megabyte.
	logMaxLineBytes = 64 << 10
	// logTailBackoff is the pause before re-attaching to a workload whose log
	// stream ended (a restart, a recreate, a transient backend error).
	logTailBackoff = 2 * time.Second
)

// logSource is the slice of deploy.Backend the recorder needs.
//
// It is narrowed to two methods on purpose: this is the seam that lets the whole
// recorder be tested against a few dozen lines of fake writing canned stdcopy
// frames, with no container runtime anywhere near the test.
type logSource interface {
	List(ctx context.Context) ([]api.DeployStatus, error)
	Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error
}

// logRecorder tails every managed workload into the observability store.
type logRecorder struct {
	// store is the READ side: resuming from the newest recorded timestamp.
	store obsstore.Store
	// accept is the WRITE side, and it is deliberately not store.IngestLogs.
	// Recorded lines have to reach every sink a received export does — including
	// the re-export upstream — so the recorder goes through the same acceptance
	// path as the OTLP receiver and the caretaker mux rather than writing to the
	// store directly. Writing directly is exactly the bug an E2E caught: the
	// zero-touch feed silently skipped re-export while every unit test passed.
	accept func(signal string, body []byte) error
	source func() (logSource, error)

	reconcileInterval time.Duration
	flushInterval     time.Duration
	backoff           time.Duration

	mu    sync.Mutex
	tails map[string]context.CancelFunc

	dropped atomic.Int64
	// attached counts workloads currently being tailed, for tests and for the
	// status endpoint.
	attached atomic.Int64
}

func newLogRecorder(store obsstore.Store, accept func(string, []byte) error, source func() (logSource, error)) *logRecorder {
	return &logRecorder{
		store:             store,
		accept:            accept,
		source:            source,
		reconcileInterval: logReconcileInterval,
		flushInterval:     logFlushInterval,
		backoff:           logTailBackoff,
		tails:             map[string]context.CancelFunc{},
	}
}

// Dropped reports how many records were shed under backpressure.
func (r *logRecorder) Dropped() int64 { return r.dropped.Load() }

// Serve runs the reconcile loop until ctx is cancelled, then stops every tail.
// It satisfies supervisor.Service.
func (r *logRecorder) Serve(ctx context.Context) error {
	defer r.stopAll()
	t := time.NewTicker(r.reconcileInterval)
	defer t.Stop()
	r.reconcile(ctx) // once up front, so a restart does not wait a full tick
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			r.reconcile(ctx)
		}
	}
}

// reconcile starts tails for workloads that gained one and stops tails for
// workloads that are gone.
//
// A List failure is logged and skipped rather than returned: the backend may be
// briefly unavailable (reconnecting to a Docker socket, a cluster blip), and
// tearing down every live tail over a transient read would lose more than it
// protects.
func (r *logRecorder) reconcile(ctx context.Context) {
	src, err := r.source()
	if err != nil {
		logging.FromContext(ctx).DebugContext(ctx, "log recorder: no deploy backend yet", "error", err)
		return
	}
	list, err := src.List(ctx)
	if err != nil {
		logging.FromContext(ctx).DebugContext(ctx, "log recorder: listing workloads failed", "error", err)
		return
	}

	live := make(map[string]api.DeployStatus, len(list))
	for _, st := range list {
		if st.Name != "" {
			live[st.Name] = st
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for key, cancel := range r.tails {
		name, idx := parseTailKey(key)
		st, ok := live[name]
		// A scale-down leaves a tail for a replica that no longer exists, so the
		// index is checked as well as the name.
		if !ok || idx >= max(1, len(st.Instances)) {
			cancel()
			delete(r.tails, key)
		}
	}
	for name, st := range live {
		// One tail per REPLICA. Recording only the first would make the store
		// quietly incomplete for a scaled workload while still claiming to hold
		// everything the live tail shows — the property the whole feature rests
		// on. A deployment with no reported instances yet still gets one tail at
		// index 0, so a workload whose status has not filled in is not skipped.
		for i := 0; i < max(1, len(st.Instances)); i++ {
			key := tailKey(name, i)
			if _, ok := r.tails[key]; ok {
				continue
			}
			tctx, cancel := context.WithCancel(ctx)
			r.tails[key] = cancel
			res := resourceAttrsFor(st)
			go r.tail(tctx, src, name, i, res)
		}
	}
}

func (r *logRecorder) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, cancel := range r.tails {
		cancel()
		delete(r.tails, name)
	}
}

// resourceAttrsFor builds the resource attributes stamped on every record from a
// workload.
//
// These deliberately match what deploy.BuildTelemetryWiring injects as
// OTEL_RESOURCE_ATTRIBUTES for the opt-in feed, so a line recorded off stdout and
// a span exported by the app's own SDK join on service.name and
// cornus.deployment without a reader knowing which feed produced which.
func resourceAttrsFor(st api.DeployStatus) map[string]string {
	res := map[string]string{
		"service.name":          st.Name,
		"cornus.deployment":     st.Name,
		"telemetry.distro.name": "cornus",
	}
	if st.Backend != "" {
		res["cornus.backend"] = st.Backend
	}
	// Lineage (project, host, user, git) when the deployment recorded it, using
	// the same key vocabulary as the deploy labels so a query can filter by
	// project without a second mapping.
	for k, v := range deploy.OriginToLabels(st.Origin) {
		if v != "" {
			res[k] = v
		}
	}
	return res
}

// tail keeps one workload attached until ctx is cancelled.
//
// The stream ends whenever the container does — a restart, a recreate, a crash —
// so ending is normal, not an error, and the loop simply re-attaches after a
// pause. That is also why the resume point is recomputed each time around rather
// than tracked in memory: after a crash-and-recreate the store is the only thing
// that still knows how far the record got.
// tailKey identifies one replica's tail. The separator is '#' because a
// deployment name cannot contain one, so the split back is unambiguous.
func tailKey(name string, replica int) string { return name + "#" + strconv.Itoa(replica) }

func parseTailKey(key string) (string, int) {
	name, idxs, ok := strings.Cut(key, "#")
	if !ok {
		return key, 0
	}
	idx, err := strconv.Atoi(idxs)
	if err != nil {
		return name, 0
	}
	return name, idx
}

func (r *logRecorder) tail(ctx context.Context, src logSource, name string, replica int, res map[string]string) {
	r.attached.Add(1)
	defer r.attached.Add(-1)

	for ctx.Err() == nil {
		since := r.resumeSince(ctx, name, replica)
		err := r.stream(ctx, src, name, replica, since, res)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, io.EOF) {
			logging.FromContext(ctx).DebugContext(ctx, "log recorder: stream ended",
				"workload", name, "replica", replica, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.backoff):
		}
	}
}

// resumeSince returns the newest timestamp already recorded for a workload, so a
// re-attach does not re-record everything the runtime still holds. A zero time
// means "take whatever the runtime has", which is what a workload seen for the
// first time wants.
func (r *logRecorder) resumeSince(ctx context.Context, name string, replica int) time.Time {
	entries, err := r.store.QueryLogs(ctx, obsstore.LogQuery{
		Service: name,
		// Scoped to THIS replica: a shared watermark would let a chatty replica
		// advance the resume point past a quiet one's unread lines, silently
		// losing them on the next reattach.
		Attrs:  map[string]string{attrReplica: strconv.Itoa(replica)},
		Newest: true,
		Limit:  1,
	})
	if err != nil || len(entries) == 0 {
		return time.Time{}
	}
	return entries[0].Time
}

// stream attaches to one workload's log stream and records it until it ends.
func (r *logRecorder) stream(ctx context.Context, src logSource, name string, replica int, since time.Time, res map[string]string) error {
	opts := api.LogOptions{
		Follow:   true,
		Tail:     "all",
		Instance: replica,
		// Timestamps are what make this a recorder rather than a transcript:
		// without them every line would be stamped with the moment cornus
		// happened to read it, and the resume-on-reattach point would drift
		// forward on every restart.
		Timestamps: true,
	}
	if !since.IsZero() {
		opts.Since = since.UTC().Format(time.RFC3339Nano)
	}

	out := newLineSink(r, res, "stdout", replica)
	errSink := newLineSink(r, res, "stderr", replica)

	// Backend.Logs writes stdcopy-multiplexed frames to a Writer, while StdCopy
	// demultiplexes from a Reader, so a pipe joins them — the same shape the
	// compose client uses for the identical problem.
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(src.Logs(ctx, name, opts, pw)) }()

	// Flush on a timer so a workload that prints one line a minute does not have
	// that line sit unqueryable until the next one arrives.
	stopFlush := make(chan struct{})
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		t := time.NewTicker(r.flushInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopFlush:
				return
			case <-t.C:
				out.Flush(ctx)
				errSink.Flush(ctx)
			}
		}
	}()

	_, err := stdcopy.StdCopy(out, errSink, pr)
	pr.CloseWithError(err)

	// Stop the flusher and wait for it before draining, so the final partial
	// line cannot race a timed flush.
	close(stopFlush)
	<-flushDone
	out.Close(ctx)
	errSink.Close(ctx)
	return err
}

// lineSink turns one demultiplexed byte stream into batched OTLP log records.
//
// It is an io.Writer because that is what StdCopy hands it, but the writes it
// receives are frame-sized, not line-sized, so it does its own line splitting and
// carries the remainder between calls.
type lineSink struct {
	rec     *logRecorder
	res     map[string]string
	stream  string
	replica int

	mu   sync.Mutex
	buf  []byte
	recs []obsstore.Record
}

func newLineSink(rec *logRecorder, res map[string]string, stream string, replica int) *lineSink {
	return &lineSink{rec: rec, res: res, stream: stream, replica: replica}
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			// Cap an unterminated line so a workload printing without newlines
			// cannot grow this buffer without bound. The truncated part is
			// emitted as its own record rather than discarded.
			if len(s.buf) > logMaxLineBytes {
				s.appendLine(s.buf[:logMaxLineBytes])
				s.buf = s.buf[logMaxLineBytes:]
				continue
			}
			break
		}
		s.appendLine(s.buf[:i])
		s.buf = s.buf[i+1:]
	}
	full := len(s.recs) >= logBatchSize
	s.mu.Unlock()

	if full {
		s.Flush(context.Background())
	}
	return len(p), nil
}

// appendLine converts one raw line into a record. Caller holds the lock.
func (s *lineSink) appendLine(line []byte) {
	text := strings.TrimSuffix(string(line), "\r")
	when, body := splitLogTimestamp(text)
	if body == "" && when.IsZero() {
		return // a blank keepalive line carries nothing worth storing
	}
	if when.IsZero() {
		when = time.Now().UTC()
	}
	s.recs = append(s.recs, obsstore.Record{
		Time: when,
		// Severity stays unspecified on purpose. cornus does not guess a level
		// by pattern-matching the text, and stderr does not mean "error" —
		// plenty of tools log informational output there. The stream is
		// recorded as an attribute so a reader can filter on the fact instead
		// of on a guess.
		Body: body,
		// Both keys are RECORD attributes, not resource attributes. That is not
		// cosmetic: the query surface filters on record attributes (LogQuery.Attrs),
		// so a replica ordinal stamped on the resource would be invisible to
		// `--replica` AND to the recorder's own per-replica resume watermark —
		// which would silently re-record a replica from scratch on every reattach.
		Attributes: map[string]string{
			"cornus.stream": s.stream,
			attrReplica:     strconv.Itoa(s.replica),
		},
	})
}

// Flush writes the accumulated batch to the store.
func (s *lineSink) Flush(ctx context.Context) {
	s.mu.Lock()
	batch := s.recs
	s.recs = nil
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	otlp, err := obsstore.EncodeLogs(s.res, batch)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "log recorder: encoding failed", "error", err)
		return
	}
	if err := s.rec.accept(obsSignalLogs, otlp); err != nil {
		if errors.Is(err, obsstore.ErrBackpressure) {
			// Shedding is the designed response to a flood; count it so an
			// operator can tell a quiet workload from a dropped one.
			s.rec.dropped.Add(int64(len(batch)))
			return
		}
		logging.FromContext(ctx).WarnContext(ctx, "log recorder: ingest failed", "error", err)
	}
}

// Close emits any trailing partial line, then flushes.
func (s *lineSink) Close(ctx context.Context) {
	s.mu.Lock()
	if len(s.buf) > 0 {
		s.appendLine(s.buf)
		s.buf = nil
	}
	s.mu.Unlock()
	s.Flush(ctx)
}

// splitLogTimestamp splits a runtime-prefixed log line into its event time and
// the text the workload actually wrote.
//
// With LogOptions.Timestamps the backends prefix each line with an RFC3339Nano
// instant and a space. A line that does not carry one (a backend that ignored the
// option, or output that happens to start with something else) returns a zero
// time, and the caller stamps it with now — which is a worse timestamp, but a
// timestamp.
func splitLogTimestamp(line string) (time.Time, string) {
	head, rest, ok := strings.Cut(line, " ")
	if !ok {
		// A whole line with no space could still be a bare timestamp; try, and
		// otherwise treat it as body text.
		if ts, err := time.Parse(time.RFC3339Nano, line); err == nil {
			return ts.UTC(), ""
		}
		return time.Time{}, line
	}
	ts, err := time.Parse(time.RFC3339Nano, head)
	if err != nil {
		return time.Time{}, line
	}
	return ts.UTC(), rest
}

// superviseLogRecorder registers the recorder as a supervised child, so a panic
// or unexpected exit restarts it in place and each run lands in the flight log.
func (s *Server) superviseLogRecorder() {
	if !s.obsEnabled() || !s.cfg.ObsRecordLogs {
		return
	}
	s.logRecorder = newLogRecorder(s.obs, s.acceptOTLP, func() (logSource, error) {
		b, err := s.getBackend()
		if err != nil {
			return nil, err
		}
		return b, nil
	})
	s.sup.Add("obs-log-recorder", supervisor.ServiceFunc(s.logRecorder.Serve), supervisor.Restart)
}
