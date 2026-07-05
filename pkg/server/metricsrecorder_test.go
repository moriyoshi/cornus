package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/obsstore"
)

// fakeSampler is a deploy.Backend-shaped stand-in: enough of List plus
// SampleMetrics to drive the recorder with no container runtime anywhere near
// the test.
type fakeSampler struct {
	list []api.DeployStatus
	// samples answers per (name, instance); a missing key returns ErrNotFound,
	// which is how a scaled-down or restarting replica behaves.
	samples map[string]api.ResourceSample
	err     error

	mu   sync.Mutex
	seen []string
}

func (f *fakeSampler) List(context.Context) ([]api.DeployStatus, error) { return f.list, nil }

func (f *fakeSampler) SampleMetrics(_ context.Context, name string, instance int) (api.ResourceSample, error) {
	key := fmt.Sprintf("%s#%d", name, instance)
	f.mu.Lock()
	f.seen = append(f.seen, key)
	f.mu.Unlock()
	if f.err != nil {
		return api.ResourceSample{}, f.err
	}
	s, ok := f.samples[key]
	if !ok {
		return api.ResourceSample{}, fmt.Errorf("no such instance: %w", deploy.ErrNotFound)
	}
	return s, nil
}

func (f *fakeSampler) Name() string { return "fake" }

func (f *fakeSampler) sampled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// collector captures what the recorder handed to the acceptance path.
type collector struct {
	mu     sync.Mutex
	bodies [][]byte
	err    error
}

func (c *collector) accept(signal string, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if signal != obsSignalMetrics {
		return fmt.Errorf("recorder used signal %q, want %q", signal, obsSignalMetrics)
	}
	if c.err != nil {
		return c.err
	}
	c.bodies = append(c.bodies, body)
	return nil
}

func (c *collector) decoded(t *testing.T) []*metricspb.Metric {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*metricspb.Metric
	for _, b := range c.bodies {
		var req colmetrics.ExportMetricsServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			t.Fatalf("recorder emitted bytes that are not an OTLP metrics export: %v", err)
		}
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				out = append(out, sm.Metrics...)
			}
		}
	}
	return out
}

func newTestMetricsRecorder(src *fakeSampler, c *collector) *metricsRecorder {
	return newMetricsRecorder(c.accept, func() (metricsSource, error) { return src, nil }, time.Hour)
}

func sample() api.ResourceSample {
	read, write := uint64(4096), uint64(8192)
	return api.ResourceSample{
		Time:     time.Unix(1700000000, 0).UTC(),
		CPUTime:  &api.CPUTime{Total: 9e9, User: 6e9, System: 3e9},
		MemUsage: 128 << 20,
		MemLimit: 512 << 20,
		Pids:     11,
		Networks: map[string]api.NetCounters{
			"eth0": {RxBytes: 1000, TxBytes: 2000},
		},
		DiskRead:  &read,
		DiskWrite: &write,
	}
}

// TestRecorderSamplesEveryReplica is the per-replica property the whole
// StatsOptions.Instance chain exists for. Recording only the first would
// produce a series per replica that all read identically — which renders as a
// perfectly balanced workload rather than as missing data.
func TestRecorderSamplesEveryReplica(t *testing.T) {
	src := &fakeSampler{
		list: []api.DeployStatus{{
			Name:      "web",
			Backend:   "fake",
			Instances: []api.InstanceStatus{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		}},
		samples: map[string]api.ResourceSample{
			"web#0": sample(), "web#1": sample(), "web#2": sample(),
		},
	}
	c := &collector{}
	r := newTestMetricsRecorder(src, c)
	r.collect(context.Background())

	got := src.sampled()
	if len(got) != 3 {
		t.Fatalf("sampled %v, want one call per replica", got)
	}
	want := map[string]bool{"web#0": true, "web#1": true, "web#2": true}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected sample %q", k)
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("never sampled %v", want)
	}
	if r.Sampled() != 3 {
		t.Errorf("Sampled() = %d, want 3", r.Sampled())
	}
}

// TestReplicaOrdinalIsADatapointAttribute is the record-vs-resource distinction
// that already bit the log recorder once.
//
// PromQL filters and groups on DATAPOINT attributes. A replica ordinal placed on
// the resource would be encoded, stored, and completely invisible to the query
// that wants to compare replicas — which is most of the reason to sample each
// one separately.
func TestReplicaOrdinalIsADatapointAttribute(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web", Instances: []api.InstanceStatus{{ID: "a"}, {ID: "b"}}}},
		samples: map[string]api.ResourceSample{"web#0": sample(), "web#1": sample()},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())

	seen := map[string]bool{}
	for _, m := range c.decoded(t) {
		if m.Name != metricMemUsage {
			continue
		}
		for _, p := range m.GetGauge().GetDataPoints() {
			v, ok := attrString(p.Attributes, attrMetricReplica)
			if !ok {
				t.Fatalf("%s datapoint carries no %s attribute: %v", m.Name, attrMetricReplica, p.Attributes)
			}
			seen[v] = true
		}
	}
	if !seen["0"] || !seen["1"] {
		t.Errorf("replica ordinals present = %v, want both 0 and 1", seen)
	}
}

// TestDatapointAttributeKeysAreUnderscored guards a property that is invisible
// from inside Go and silent when broken.
//
// The store's PromQL cannot express a label name containing a dot — not as a
// bare identifier, not quoted, not in a `by` clause. So a datapoint labelled
// `cornus.replica` (the semconv spelling, and the one the LOG recorder correctly
// uses) is not merely awkward to query, it is unreachable:
//
//	container_memory_usage{cornus_replica="0"}   -> 0 series, and no error
//
// Nothing in Go fails, no test that only checks encoding fails, and the feature
// simply does not work. If someone "corrects" these keys back to semconv dots,
// this test is what tells them why they cannot.
func TestDatapointAttributeKeysAreUnderscored(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web"}},
		samples: map[string]api.ResourceSample{"web#0": sample()},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())

	for _, m := range c.decoded(t) {
		for _, p := range allDataPoints(m) {
			for _, kv := range p.Attributes {
				if strings.Contains(kv.Key, ".") {
					t.Errorf("metric %q carries datapoint attribute %q; a dot makes it unqueryable in PromQL", m.Name, kv.Key)
				}
			}
		}
	}
}

func allDataPoints(m *metricspb.Metric) []*metricspb.NumberDataPoint {
	if g := m.GetGauge(); g != nil {
		return g.DataPoints
	}
	if s := m.GetSum(); s != nil {
		return s.DataPoints
	}
	return nil
}

// TestRecordedMetricsGoThroughTheAcceptancePath is the regression guard for the
// bug an E2E caught in the log recorder: a zero-touch feed that writes to the
// store directly reaches the store and NOTHING else, so re-export silently drops
// half the data while every unit test passes. The recorder must never hold a
// reference to the store.
func TestRecordedMetricsGoThroughTheAcceptancePath(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web"}},
		samples: map[string]api.ResourceSample{"web#0": sample()},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())

	c.mu.Lock()
	n := len(c.bodies)
	c.mu.Unlock()
	if n == 0 {
		t.Fatal("nothing reached the acceptance path; the recorder must not write to the store directly")
	}
}

func TestRecorderEmitsSemconvMetrics(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web", Backend: "fake"}},
		samples: map[string]api.ResourceSample{"web#0": sample()},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())

	byName := map[string]*metricspb.Metric{}
	for _, m := range c.decoded(t) {
		byName[m.Name] = m
	}
	for _, want := range []string{metricCPUTime, metricMemUsage, metricMemLimit, metricPids, metricNetIO, metricDiskIO} {
		if byName[want] == nil {
			t.Errorf("metric %q was not emitted; got %v", want, names(byName))
		}
	}

	// CPU time is a monotonic cumulative SUM in seconds. A gauge would leave a
	// reader unable to ask how fast; delta temporality would make every rate()
	// read as the total since the process started.
	cpu := byName[metricCPUTime]
	if cpu == nil {
		t.Fatal("no CPU time metric")
	}
	if cpu.Unit != "s" {
		t.Errorf("%s unit = %q, want \"s\" per semconv", metricCPUTime, cpu.Unit)
	}
	sum := cpu.GetSum()
	if sum == nil {
		t.Fatalf("%s is not a Sum: %T", metricCPUTime, cpu.Data)
	}
	if !sum.IsMonotonic {
		t.Errorf("%s is not monotonic", metricCPUTime)
	}
	if sum.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Errorf("%s temporality = %v, want cumulative", metricCPUTime, sum.AggregationTemporality)
	}
	modes := map[string]float64{}
	for _, p := range sum.DataPoints {
		mode, _ := attrString(p.Attributes, attrCPUMode)
		modes[mode] = p.GetAsDouble()
	}
	if modes["user"] != 6 || modes["system"] != 3 {
		t.Errorf("CPU seconds by mode = %v, want user 6 / system 3 (nanoseconds converted)", modes)
	}

	// Network is split by direction and labelled with the interface, so a reader
	// can ask about one NIC without the others folded in.
	net := byName[metricNetIO].GetSum()
	if net == nil || len(net.DataPoints) != 2 {
		t.Fatalf("%s datapoints = %v, want one per direction", metricNetIO, net)
	}
	for _, p := range net.DataPoints {
		if _, ok := attrString(p.Attributes, attrNetInterface); !ok {
			t.Errorf("%s datapoint has no interface name: %v", metricNetIO, p.Attributes)
		}
	}
}

// A backend that reports a rate rather than a counter (kubernetes via
// metrics-server) must land under its own gauge name. Folding it into
// container.cpu.time would make a synthesized counter indistinguishable from a
// real one.
func TestRateOnlyBackendEmitsAGauge(t *testing.T) {
	cores := 0.75
	src := &fakeSampler{
		list: []api.DeployStatus{{Name: "web"}},
		samples: map[string]api.ResourceSample{
			"web#0": {Time: time.Now(), CPUCores: &cores, MemUsage: 100},
		},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())

	var usage, cpuTime *metricspb.Metric
	for _, m := range c.decoded(t) {
		switch m.Name {
		case metricCPUUsage:
			usage = m
		case metricCPUTime:
			cpuTime = m
		}
	}
	if cpuTime != nil {
		t.Errorf("%s was emitted for a backend with no cumulative source", metricCPUTime)
	}
	if usage == nil || usage.GetGauge() == nil {
		t.Fatalf("%s missing or not a gauge: %v", metricCPUUsage, usage)
	}
	if got := usage.GetGauge().DataPoints[0].GetAsDouble(); got != cores {
		t.Errorf("%s = %v, want %v", metricCPUUsage, got, cores)
	}
}

// TestUnobservedFamiliesEmitNoSeries: a backend that cannot see network or disk
// must produce no series at all, not a series reading zero. The second would
// assert the container moved no bytes, which is a different (and false) claim.
func TestUnobservedFamiliesEmitNoSeries(t *testing.T) {
	src := &fakeSampler{
		list: []api.DeployStatus{{Name: "web"}},
		samples: map[string]api.ResourceSample{
			"web#0": {Time: time.Now(), MemUsage: 100},
		},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())

	for _, m := range c.decoded(t) {
		if m.Name == metricNetIO || m.Name == metricDiskIO {
			t.Errorf("emitted %q for a sample that observed none", m.Name)
		}
	}
}

// A replica that vanished between the List and the sample is the normal state of
// a restarting workload, not a fault. Counting it as a failure would make the
// health counter useless on anything that ever restarts.
func TestDepartedReplicaIsNotAFailure(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web", Instances: []api.InstanceStatus{{ID: "a"}, {ID: "b"}}}},
		samples: map[string]api.ResourceSample{"web#0": sample()},
	}
	c := &collector{}
	r := newTestMetricsRecorder(src, c)
	r.collect(context.Background())

	if r.Failed() != 0 {
		t.Errorf("Failed() = %d, want 0 for a replica reported as gone", r.Failed())
	}
	if r.Sampled() != 1 {
		t.Errorf("Sampled() = %d, want 1", r.Sampled())
	}

	// A real error, by contrast, must be counted — that is what tells a quiet
	// server apart from a broken one.
	src.err = errors.New("daemon unreachable")
	r.collect(context.Background())
	if r.Failed() == 0 {
		t.Error("Failed() = 0 after a genuine backend error")
	}
}

func TestBackpressureIsCountedNotLogged(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web"}},
		samples: map[string]api.ResourceSample{"web#0": sample()},
	}
	c := &collector{err: obsstore.ErrBackpressure}
	r := newTestMetricsRecorder(src, c)
	r.collect(context.Background())

	if r.Dropped() != 1 {
		t.Errorf("Dropped() = %d, want 1", r.Dropped())
	}
	if r.Sampled() != 0 {
		t.Errorf("Sampled() = %d; a shed batch was not recorded", r.Sampled())
	}
}

// A backend with no metrics source must be skipped quietly and reported once,
// not treated as an error every interval forever.
func TestUnsupportedBackendIsReportedOnce(t *testing.T) {
	r := newMetricsRecorder(
		func(string, []byte) error { return nil },
		func() (metricsSource, error) { return listOnlySource{}, nil },
		time.Hour,
	)
	for i := 0; i < 3; i++ {
		r.collect(context.Background())
	}
	if r.Failed() != 0 || r.Sampled() != 0 {
		t.Errorf("counters = %d failed / %d sampled, want 0/0 for a backend that cannot sample", r.Failed(), r.Sampled())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.unsupported["listonly"] {
		t.Error("the unsupported backend was not remembered, so the fact would be logged every interval")
	}
}

type listOnlySource struct{}

func (listOnlySource) List(context.Context) ([]api.DeployStatus, error) {
	return []api.DeployStatus{{Name: "web"}}, nil
}
func (listOnlySource) Name() string { return "listonly" }

// A deployment whose status has not filled in yet still gets one sample, so a
// just-deployed workload is not skipped until its instance list appears.
func TestDeploymentWithNoReportedInstancesStillSamples(t *testing.T) {
	src := &fakeSampler{
		list:    []api.DeployStatus{{Name: "web"}},
		samples: map[string]api.ResourceSample{"web#0": sample()},
	}
	c := &collector{}
	newTestMetricsRecorder(src, c).collect(context.Background())
	if got := src.sampled(); len(got) != 1 || got[0] != "web#0" {
		t.Errorf("sampled %v, want [web#0]", got)
	}
}

// TestEncodeMetricsOmitsEmptyMetrics: a metric with no datapoints is the absence
// of a reading, not a zero one, and an empty envelope renders as a series with a
// legend and no data.
func TestEncodeMetricsOmitsEmptyMetrics(t *testing.T) {
	body, err := obsstore.EncodeMetrics(map[string]string{"service.name": "web"},
		[]obsstore.Metric{{Name: "empty", Kind: obsstore.KindGauge}})
	if err != nil {
		t.Fatalf("EncodeMetrics: %v", err)
	}
	if body != nil {
		t.Errorf("EncodeMetrics produced %d bytes for a metric with no datapoints", len(body))
	}
}

func attrString(attrs []*commonpb.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value.GetStringValue(), true
		}
	}
	return "", false
}

func names(m map[string]*metricspb.Metric) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ", ")
}
