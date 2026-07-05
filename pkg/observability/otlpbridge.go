package observability

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/proto"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// The in-process metric bridge: cornus's own instruments, delivered to a sink
// inside this process instead of over the network.
//
// It exists because the server's built-in observability store is reachable by a
// function call, not a URL. Everything the meter already carries —
// cornus.builds, cornus.deploys, cornus.mount.io.bytes, the Go runtime metrics,
// and the process resource metrics in pkg/server/selfmetrics.go — is exactly
// what an operator wants next to their workloads' metrics, and routing it back
// out through OTLP/HTTP to cornus's own listener would mean an HTTP round trip,
// an auth header, and a bootstrap ordering problem, all to move bytes between
// two objects in the same address space.
//
// # The sink is set after Setup, on purpose
//
// Setup runs at process start, before the server that owns the store exists. So
// the reader is installed here and the destination is filled in later, the same
// process-global shape promHandler already uses in this package. Exports before
// the sink is set are dropped; at any realistic reader interval that is none in
// practice, and dropping is the right answer anyway — there is nowhere to put
// them.
//
// # Why an encoder rather than an existing exporter
//
// The OTLP exporters in the OTel module all terminate in a network transport,
// and their metricdata -> protobuf translation lives in an `internal` package
// that cannot be imported. So the translation is written out below. It is
// mechanical, and the shapes it covers are exactly the ones cornus's own
// instruments produce.

// MetricSink receives one collection cycle's metrics, already encoded as an
// OTLP/HTTP ExportMetricsServiceRequest protobuf — the same bytes a real
// exporter would send, which is also what the store ingests.
type MetricSink func(otlp []byte) error

// metricSink holds the registered destination. atomic.Value rather than a mutex
// because it is written once and read on every collection.
var metricSink atomic.Value // of MetricSink

// SetMetricSink installs the destination for in-process metric exports. Passing
// nil detaches it, after which collected metrics are dropped.
//
// It is safe to call at any time and takes effect on the next collection cycle.
func SetMetricSink(fn MetricSink) {
	if fn == nil {
		metricSink.Store(MetricSink(nil))
		return
	}
	metricSink.Store(fn)
}

// currentMetricSink returns the registered sink, or nil.
func currentMetricSink() MetricSink {
	v, _ := metricSink.Load().(MetricSink)
	return v
}

// defaultInProcessInterval is how often the bridge collects when the caller
// names no interval.
const defaultInProcessInterval = time.Minute

// newInProcessReader builds the periodic reader that feeds the sink.
//
// The interval is the caller's, normally the same one the workload metrics
// sampler runs at. Sharing it is the point: an operator who asks for 15s
// resolution on their workloads and gets 60s on the server running them has two
// series they cannot line up at the moment they most want to — the workload
// spike and whatever the server was doing when it happened.
func newInProcessReader(interval time.Duration) sdkmetric.Reader {
	if interval <= 0 {
		interval = defaultInProcessInterval
	}
	return sdkmetric.NewPeriodicReader(sinkExporter{}, sdkmetric.WithInterval(interval))
}

// sinkExporter is a sdkmetric.Exporter that encodes to OTLP and hands the bytes
// to the registered sink.
type sinkExporter struct{}

// Temporality is cumulative for every instrument kind — the SDK default, and
// what a PromQL-answering store needs. Delta temporality would make every rate()
// read as the total since process start.
func (sinkExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (sinkExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (sinkExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	sink := currentMetricSink()
	if sink == nil || rm == nil {
		return nil
	}
	body, err := encodeResourceMetrics(rm)
	if err != nil || len(body) == 0 {
		return err
	}
	return sink(body)
}

// ForceFlush has nothing buffered of its own: the reader collects and calls
// Export synchronously, so by the time this runs there is nothing in flight.
func (sinkExporter) ForceFlush(context.Context) error { return nil }
func (sinkExporter) Shutdown(context.Context) error   { return nil }

// encodeResourceMetrics translates one collection into OTLP protobuf.
func encodeResourceMetrics(rm *metricdata.ResourceMetrics) ([]byte, error) {
	scopes := make([]*metricspb.ScopeMetrics, 0, len(rm.ScopeMetrics))
	for _, sm := range rm.ScopeMetrics {
		metrics := make([]*metricspb.Metric, 0, len(sm.Metrics))
		for _, m := range sm.Metrics {
			if pb := encodeMetric(m); pb != nil {
				metrics = append(metrics, pb)
			}
		}
		if len(metrics) == 0 {
			continue
		}
		scopes = append(scopes, &metricspb.ScopeMetrics{
			Scope: &commonpb.InstrumentationScope{
				Name:    sm.Scope.Name,
				Version: sm.Scope.Version,
			},
			Metrics: metrics,
		})
	}
	if len(scopes) == 0 {
		return nil, nil
	}
	var resAttrs []*commonpb.KeyValue
	if rm.Resource != nil {
		resAttrs = encodeAttrs(*rm.Resource.Set())
	}
	req := &colmetrics.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource:     &resourcepb.Resource{Attributes: resAttrs},
			ScopeMetrics: scopes,
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("observability: encode metrics: %w", err)
	}
	return b, nil
}

// encodeMetric translates one metric, returning nil for an aggregation this
// bridge does not carry.
//
// The unhandled cases are exponential histograms and summaries. Nothing in
// cornus creates either, and emitting a wrong-shaped approximation would be
// worse than the metric being absent — a reader can notice a missing series, but
// not a silently misrepresented one.
func encodeMetric(m metricdata.Metrics) *metricspb.Metric {
	pb := &metricspb.Metric{Name: m.Name, Description: m.Description, Unit: m.Unit}
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		pb.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: numberPoints(d.DataPoints)}}
	case metricdata.Gauge[float64]:
		pb.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: numberPoints(d.DataPoints)}}
	case metricdata.Sum[int64]:
		pb.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			DataPoints:             numberPoints(d.DataPoints),
			AggregationTemporality: temporality(d.Temporality),
			IsMonotonic:            d.IsMonotonic,
		}}
	case metricdata.Sum[float64]:
		pb.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			DataPoints:             numberPoints(d.DataPoints),
			AggregationTemporality: temporality(d.Temporality),
			IsMonotonic:            d.IsMonotonic,
		}}
	case metricdata.Histogram[int64]:
		pb.Data = &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
			DataPoints:             histogramPoints(d.DataPoints),
			AggregationTemporality: temporality(d.Temporality),
		}}
	case metricdata.Histogram[float64]:
		pb.Data = &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
			DataPoints:             histogramPoints(d.DataPoints),
			AggregationTemporality: temporality(d.Temporality),
		}}
	default:
		return nil
	}
	return pb
}

// number is the constraint over the two datapoint value types the SDK produces.
type number interface{ int64 | float64 }

func numberPoints[N number](pts []metricdata.DataPoint[N]) []*metricspb.NumberDataPoint {
	out := make([]*metricspb.NumberDataPoint, 0, len(pts))
	for _, p := range pts {
		pb := &metricspb.NumberDataPoint{
			Attributes:        encodeAttrs(p.Attributes),
			StartTimeUnixNano: unixNano(p.StartTime),
			TimeUnixNano:      unixNano(p.Time),
		}
		// The int64/float64 split is preserved rather than widening everything to
		// double: a counter of bytes past 2^53 would start losing its low bits,
		// and byte counters are precisely what this bridge carries.
		switch v := any(p.Value).(type) {
		case int64:
			pb.Value = &metricspb.NumberDataPoint_AsInt{AsInt: v}
		case float64:
			pb.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: v}
		}
		out = append(out, pb)
	}
	return out
}

func histogramPoints[N number](pts []metricdata.HistogramDataPoint[N]) []*metricspb.HistogramDataPoint {
	out := make([]*metricspb.HistogramDataPoint, 0, len(pts))
	for _, p := range pts {
		pb := &metricspb.HistogramDataPoint{
			Attributes:        encodeAttrs(p.Attributes),
			StartTimeUnixNano: unixNano(p.StartTime),
			TimeUnixNano:      unixNano(p.Time),
			Count:             p.Count,
			BucketCounts:      p.BucketCounts,
			ExplicitBounds:    p.Bounds,
		}
		sum := float64(p.Sum)
		pb.Sum = &sum
		// Min and Max are optional in OTLP and genuinely absent on an empty
		// histogram, so they are only set when the SDK recorded them.
		if v, ok := p.Min.Value(); ok {
			f := float64(v)
			pb.Min = &f
		}
		if v, ok := p.Max.Value(); ok {
			f := float64(v)
			pb.Max = &f
		}
		out = append(out, pb)
	}
	return out
}

func temporality(t metricdata.Temporality) metricspb.AggregationTemporality {
	if t == metricdata.DeltaTemporality {
		return metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	}
	return metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
}

func unixNano(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

// encodeAttrs renders an attribute set as OTLP key-values.
//
// attribute.Set iterates in sorted key order, so the encoded bytes are
// deterministic for a given set without sorting here.
func encodeAttrs(set attribute.Set) []*commonpb.KeyValue {
	if set.Len() == 0 {
		return nil
	}
	out := make([]*commonpb.KeyValue, 0, set.Len())
	for _, kv := range set.ToSlice() {
		out = append(out, &commonpb.KeyValue{
			Key:   string(kv.Key),
			Value: encodeValue(kv.Value),
		})
	}
	return out
}

func encodeValue(v attribute.Value) *commonpb.AnyValue {
	switch v.Type() {
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v.AsBool()}}
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v.AsInt64()}}
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v.AsFloat64()}}
	default:
		// Slice-valued attributes fall through to their string rendering. Nothing
		// in cornus emits one, and a metric LABEL that is a list is not something
		// a PromQL store can index anyway.
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.Emit()}}
	}
}
