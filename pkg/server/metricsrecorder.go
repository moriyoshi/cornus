package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/obsstore"
	"cornus/pkg/supervisor"
)

// The metrics recorder is the resource-usage half of the zero-touch feed: it
// samples every managed workload's CPU, memory, network, and disk on a timer and
// records the readings as OTLP metrics, so an app with no instrumentation gets a
// history of what it consumed.
//
// The numbers already existed before this, but only as a live stream a human
// watches: Backend.Stats feeds `docker stats` and the web UI's metrics pane, and
// nothing kept a copy. So the question people actually arrive with — "was it
// already swapping before it got OOM-killed?" — had no answer, for the same
// reason it had no answer for logs before the log recorder.
//
// # Why polling and not the live stream
//
// Backend.Stats can stream, and holding one open per replica would look like the
// log recorder's shape. It is the wrong shape here. A stream fixes the cadence at
// whatever the backend chose (one second, for the host backends), which is 15x
// more datapoints than a recorded series needs and 15x the storage to match. And
// the log recorder's justification does not transfer: a log line missed while
// detached is gone forever, whereas a resource reading is a sample of a
// continuous quantity — missing one costs resolution, not information.
//
// # Why it does not ride the meter
//
// The server's own metrics are registered as observable instruments on the
// global meter (selfmetrics.go), which is cheaper and reaches more destinations.
// Workload metrics deliberately do not: an observable callback runs during
// collection, so a backend that hangs — a wedged Docker socket, an unreachable
// API server — would stall the entire collection cycle including the server's own
// metrics. Sampling on an owned ticker with a per-sample timeout keeps one bad
// backend from taking the rest down with it.
//
// # Delivery
//
// Best-effort, and unlike logs there is no resume: a gap in a sampled series is
// a gap, and back-filling it would mean inventing readings for a window nothing
// observed. Under backpressure the store sheds and the shed count is recorded
// (see metricsRecorder.Dropped), so an absence of datapoints can be told apart
// from an absence of ingest.

const (
	// metricsSampleTimeout bounds one backend call. Longer than this and the
	// reading would be stamped so far from when it was requested that it
	// misrepresents the interval anyway.
	metricsSampleTimeout = 10 * time.Second
	// metricsSampleConcurrency caps how many replicas are sampled at once. A
	// deployment of 200 replicas must not open 200 simultaneous calls to one
	// Docker socket; a handful in flight keeps a slow replica from serializing
	// the whole cycle without turning the collector into a load generator.
	metricsSampleConcurrency = 8
)

// metricsSource is the slice of deploy.Backend the recorder needs: the workload
// list, plus the optional sampler extension.
//
// Narrowed to one method for the same reason logSource is, and the sampler is
// reached by type assertion rather than being part of this interface — a backend
// that cannot sample is a normal condition to handle, not a compile error.
type metricsSource interface {
	List(ctx context.Context) ([]api.DeployStatus, error)
}

// metricsRecorder samples every managed workload into the observability store.
type metricsRecorder struct {
	// accept is the write side, and it is deliberately not store.IngestMetrics.
	// A recorded sample has to reach every sink a received export does —
	// including the re-export upstream — so this goes through the same acceptance
	// path as the OTLP receiver. Writing to the store directly is exactly the bug
	// an E2E caught in the log recorder: the zero-touch feed silently skipped
	// re-export while every unit test passed.
	accept func(signal string, body []byte) error
	source func() (metricsSource, error)

	interval time.Duration
	timeout  time.Duration

	dropped  atomic.Int64
	sampled  atomic.Int64
	failed   atomic.Int64
	replicas atomic.Int64

	// unsupported remembers backends that answered "no metrics source", so the
	// fact is logged once rather than every interval forever. Keyed by backend
	// name: a server whose backend is swapped should report the new one.
	mu          sync.Mutex
	unsupported map[string]bool
}

func newMetricsRecorder(accept func(string, []byte) error, source func() (metricsSource, error), interval time.Duration) *metricsRecorder {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &metricsRecorder{
		accept:      accept,
		source:      source,
		interval:    interval,
		timeout:     metricsSampleTimeout,
		unsupported: map[string]bool{},
	}
}

// Dropped reports how many datapoint batches were shed under backpressure.
func (r *metricsRecorder) Dropped() int64 { return r.dropped.Load() }

// Sampled reports how many replica readings have been recorded, and Failed how
// many sample attempts errored. The pair is what distinguishes a quiet server
// from a broken one: zero sampled with zero failed means nothing is deployed,
// zero sampled with a rising failed count means the backend is refusing.
func (r *metricsRecorder) Sampled() int64  { return r.sampled.Load() }
func (r *metricsRecorder) Failed() int64   { return r.failed.Load() }
func (r *metricsRecorder) Replicas() int64 { return r.replicas.Load() }

// Serve samples on a ticker until ctx is cancelled. It satisfies
// supervisor.Service.
func (r *metricsRecorder) Serve(ctx context.Context) error {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	// Once up front so a restart does not leave a hole the length of a full
	// interval at the exact moment an operator is most likely to be looking.
	r.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			r.collect(ctx)
		}
	}
}

// collect samples every replica of every workload once.
func (r *metricsRecorder) collect(ctx context.Context) {
	src, err := r.source()
	if err != nil {
		logging.FromContext(ctx).DebugContext(ctx, "metrics recorder: no deploy backend yet", "error", err)
		return
	}
	sampler, ok := src.(deploy.MetricsSampler)
	if !ok {
		r.noteUnsupported(ctx, backendNameOf(src), "backend has no metrics source")
		return
	}
	list, err := src.List(ctx)
	if err != nil {
		logging.FromContext(ctx).DebugContext(ctx, "metrics recorder: listing workloads failed", "error", err)
		return
	}

	// Flatten to (workload, replica) targets first, so the concurrency cap
	// applies across the whole cycle rather than per workload — otherwise a
	// hundred single-replica deployments would open a hundred calls at once
	// while honoring a per-workload limit of eight.
	type target struct {
		st      api.DeployStatus
		replica int
	}
	var targets []target
	for _, st := range list {
		if st.Name == "" {
			continue
		}
		for i := 0; i < max(1, len(st.Instances)); i++ {
			targets = append(targets, target{st: st, replica: i})
		}
	}
	r.replicas.Store(int64(len(targets)))
	if len(targets) == 0 {
		return
	}

	sem := make(chan struct{}, metricsSampleConcurrency)
	var wg sync.WaitGroup
	for _, tg := range targets {
		wg.Add(1)
		go func(tg target) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			r.sampleOne(ctx, sampler, tg.st, tg.replica)
		}(tg)
	}
	wg.Wait()
}

// sampleOne records one replica's reading.
func (r *metricsRecorder) sampleOne(ctx context.Context, sampler deploy.MetricsSampler, st api.DeployStatus, replica int) {
	sctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	sample, err := sampler.SampleMetrics(sctx, st.Name, replica)
	if err != nil {
		// A replica that is not running is the normal state of a workload
		// mid-restart or scaled down between the List and the sample, not a
		// fault. Counting it as a failure would make the health counter useless
		// on any workload that ever restarts.
		if errors.Is(err, deploy.ErrNotFound) {
			return
		}
		r.failed.Add(1)
		logging.FromContext(ctx).DebugContext(ctx, "metrics recorder: sampling failed",
			"workload", st.Name, "replica", replica, "error", err)
		return
	}

	metrics := buildWorkloadMetrics(sample, replica)
	if len(metrics) == 0 {
		return
	}
	body, err := obsstore.EncodeMetrics(resourceAttrsFor(st), metrics)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "metrics recorder: encoding failed", "error", err)
		return
	}
	if err := r.accept(obsSignalMetrics, body); err != nil {
		if errors.Is(err, obsstore.ErrBackpressure) {
			r.dropped.Add(1)
			return
		}
		logging.FromContext(ctx).WarnContext(ctx, "metrics recorder: ingest failed", "error", err)
		return
	}
	r.sampled.Add(1)
}

// Attribute and metric names.
//
// The metric names follow OpenTelemetry's container semantic conventions rather
// than inventing a cornus vocabulary, so a stock Grafana dashboard written
// against any OTel-instrumented system works here unchanged. The two that
// semconv has no name for carry a `cornus.` prefix, which is the honest marker
// that they are ours and may not mean the same thing elsewhere.
const (
	metricCPUTime  = "container.cpu.time"
	metricCPUUsage = "container.cpu.usage"
	metricMemUsage = "container.memory.usage"
	metricNetIO    = "container.network.io"
	metricDiskIO   = "container.disk.io"
	metricMemLimit = "cornus.container.memory.limit"
	metricPids     = "cornus.container.pids"
)

// Datapoint attribute keys, underscored where semconv spells them with dots.
//
// This is not a style choice, and it is the one place the metrics feed
// deliberately departs from the log recorder (which uses "cornus.replica").
// Prometheus's data model does not admit a dot in a label name at all, and the
// store's PromQL parser rejects every syntax for quoting one — so a datapoint
// labelled `cornus.replica` is not merely awkward to query, it is UNREACHABLE:
//
//	container_memory_usage{cornus_replica="0"}   -> 0 series, no error
//
// Silently, which is the worst version. Emitting the underscored form is also
// what any OTLP-to-Prometheus bridge does on the way in, so this is the
// convention for the destination rather than a deviation from one. Metric NAMES
// need no such treatment: the store normalizes those itself at query time.
const (
	attrMetricReplica = "cornus_replica"
	attrCPUMode       = "cpu_mode"
	attrNetDirection  = "network_io_direction"
	attrNetInterface  = "network_interface_name"
	attrDiskDirection = "disk_io_direction"
)

// buildWorkloadMetrics projects one reading onto the metrics to record.
//
// Every attribute here is a DATAPOINT attribute, not a resource attribute. That
// is not cosmetic: PromQL filters and groups on datapoint attributes, so a
// replica ordinal stamped on the resource would be invisible to a query trying
// to compare replicas — which is most of the reason to record per-replica at
// all. The log recorder learned the same lesson the hard way.
func buildWorkloadMetrics(s api.ResourceSample, replica int) []obsstore.Metric {
	when := s.Time
	if when.IsZero() {
		when = time.Now().UTC()
	}
	base := map[string]string{attrMetricReplica: strconv.Itoa(replica)}
	// with returns base plus one extra label, without mutating base — every
	// datapoint needs its own map, since the encoder reads them independently.
	with := func(k, v string) map[string]string {
		m := make(map[string]string, len(base)+1)
		for bk, bv := range base {
			m[bk] = bv
		}
		m[k] = v
		return m
	}
	point := func(v float64, attrs map[string]string) obsstore.NumberPoint {
		return obsstore.NumberPoint{Time: when, Value: v, Attributes: attrs}
	}

	var out []obsstore.Metric

	if s.CPUTime != nil {
		// Seconds, not nanoseconds: semconv specifies `s`, and a Prometheus
		// translation derives the `_seconds_total` suffix from the unit.
		out = append(out, obsstore.Metric{
			Name:        metricCPUTime,
			Description: "Total CPU time consumed.",
			Unit:        "s",
			Kind:        obsstore.KindSum,
			Points: []obsstore.NumberPoint{
				point(float64(s.CPUTime.User)/1e9, with(attrCPUMode, "user")),
				point(float64(s.CPUTime.System)/1e9, with(attrCPUMode, "system")),
			},
		})
	}
	if s.CPUCores != nil {
		// The rate-only shape (kubernetes via metrics-server). Recorded as a
		// gauge under its own name rather than being integrated into
		// container.cpu.time: a synthesized counter would look identical to a
		// real one while silently losing every sample the collector missed.
		out = append(out, obsstore.Metric{
			Name:        metricCPUUsage,
			Description: "Instantaneous CPU consumption, in cores.",
			Unit:        "{cpu}",
			Kind:        obsstore.KindGauge,
			Points:      []obsstore.NumberPoint{point(*s.CPUCores, copyLabels(base))},
		})
	}

	out = append(out, obsstore.Metric{
		Name:        metricMemUsage,
		Description: "Memory in use, excluding reclaimable page cache.",
		Unit:        "By",
		Kind:        obsstore.KindGauge,
		Points:      []obsstore.NumberPoint{point(float64(s.MemUsage), copyLabels(base))},
	})
	if s.MemLimit > 0 {
		out = append(out, obsstore.Metric{
			Name:        metricMemLimit,
			Description: "Memory limit enforced on the instance.",
			Unit:        "By",
			Kind:        obsstore.KindGauge,
			Points:      []obsstore.NumberPoint{point(float64(s.MemLimit), copyLabels(base))},
		})
	}
	if s.Pids > 0 {
		out = append(out, obsstore.Metric{
			Name:        metricPids,
			Description: "Processes and threads running in the instance.",
			Unit:        "{process}",
			Kind:        obsstore.KindGauge,
			Points:      []obsstore.NumberPoint{point(float64(s.Pids), copyLabels(base))},
		})
	}

	if len(s.Networks) > 0 {
		var pts []obsstore.NumberPoint
		for iface, n := range s.Networks {
			rx := with(attrNetDirection, "receive")
			rx[attrNetInterface] = iface
			tx := with(attrNetDirection, "transmit")
			tx[attrNetInterface] = iface
			pts = append(pts, point(float64(n.RxBytes), rx), point(float64(n.TxBytes), tx))
		}
		out = append(out, obsstore.Metric{
			Name:        metricNetIO,
			Description: "Bytes transferred over the instance's network interfaces.",
			Unit:        "By",
			Kind:        obsstore.KindSum,
			Points:      pts,
		})
	}
	if s.DiskRead != nil || s.DiskWrite != nil {
		var pts []obsstore.NumberPoint
		if s.DiskRead != nil {
			pts = append(pts, point(float64(*s.DiskRead), with(attrDiskDirection, "read")))
		}
		if s.DiskWrite != nil {
			pts = append(pts, point(float64(*s.DiskWrite), with(attrDiskDirection, "write")))
		}
		out = append(out, obsstore.Metric{
			Name:        metricDiskIO,
			Description: "Bytes read from and written to block devices.",
			Unit:        "By",
			Kind:        obsstore.KindSum,
			Points:      pts,
		})
	}
	return out
}

// copyLabels copies a label set. Datapoints must not share one: the encoder reads each
// independently, and a shared map would make a later mutation rewrite history.
func copyLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// noteUnsupported logs a backend's lack of a metrics source exactly once.
//
// Once, because this is a permanent property of the backend, not an incident:
// repeating it every interval would fill the log with a fact that never changes
// and train the reader to ignore the recorder's output entirely.
func (r *metricsRecorder) noteUnsupported(ctx context.Context, backend, reason string) {
	r.mu.Lock()
	seen := r.unsupported[backend]
	r.unsupported[backend] = true
	r.mu.Unlock()
	if seen {
		return
	}
	logging.FromContext(ctx).InfoContext(ctx,
		"metrics recorder: not recording workload metrics on this backend",
		"backend", backend, "reason", reason)
}

// backendNameOf reports a backend's name for logging, falling back to its type
// when it does not have one.
func backendNameOf(src metricsSource) string {
	if n, ok := src.(interface{ Name() string }); ok {
		return n.Name()
	}
	return fmt.Sprintf("%T", src)
}

// superviseMetricsRecorder registers the recorder as a supervised child, so a
// panic or unexpected exit restarts it in place and each run lands in the flight
// log — the same treatment the log recorder gets.
func (s *Server) superviseMetricsRecorder() {
	// Unlike the log recorder this does not require a store: the re-export path
	// accepts metrics with no local store at all (gateway mode), and a server
	// forwarding to an upstream Grafana stack has exactly as much use for
	// workload metrics as one keeping them.
	if !s.cfg.ObsRecordMetrics || (!s.obsEnabled() && s.obsExport == nil) {
		return
	}
	s.metricsRecorder = newMetricsRecorder(s.acceptOTLP, func() (metricsSource, error) {
		b, err := s.getBackend()
		if err != nil {
			return nil, err
		}
		return b, nil
	}, s.cfg.ObsMetricsInterval)
	s.sup.Add("obs-metrics-recorder", supervisor.ServiceFunc(s.metricsRecorder.Serve), supervisor.Restart)
}
