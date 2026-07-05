package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/proto"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// The bridge is tested through a REAL MeterProvider rather than by handing
// encodeResourceMetrics a literal. The translation is mechanical enough that a
// hand-built input mostly tests the test; what can actually be wrong is which
// metricdata shape the SDK produces for a given instrument, and only the SDK can
// answer that.

// collectThrough records instruments on a provider whose only reader is the
// bridge exporter, forces a collection, and returns the decoded OTLP.
func collectThrough(t *testing.T, record func(metric.Meter)) *colmetrics.ExportMetricsServiceRequest {
	t.Helper()

	var body []byte
	SetMetricSink(func(b []byte) error { body = b; return nil })
	t.Cleanup(func() { SetMetricSink(nil) })

	reader := sdkmetric.NewPeriodicReader(sinkExporter{}, sdkmetric.WithInterval(time.Hour))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	record(mp.Meter("cornus-test"))

	// Shutdown forces a final collection, which is how the test gets a cycle
	// without waiting out the interval.
	if err := mp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if body == nil {
		t.Fatal("the bridge produced no export")
	}
	var req colmetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		t.Fatalf("the bridge produced bytes that are not an OTLP metrics export: %v", err)
	}
	return &req
}

func metricsByName(req *colmetrics.ExportMetricsServiceRequest) map[string]*metricspb.Metric {
	out := map[string]*metricspb.Metric{}
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				out[m.Name] = m
			}
		}
	}
	return out
}

func TestBridgeCarriesEveryInstrumentShapeCornusUses(t *testing.T) {
	req := collectThrough(t, func(m metric.Meter) {
		ctr, _ := m.Int64Counter("cornus.test.counter", metric.WithUnit("By"))
		ctr.Add(context.Background(), 7, metric.WithAttributes(attribute.String("direction", "rx")))

		hist, _ := m.Float64Histogram("cornus.test.duration", metric.WithUnit("s"))
		hist.Record(context.Background(), 1.5)

		gauge, _ := m.Int64UpDownCounter("cornus.test.gauge")
		gauge.Add(context.Background(), 42)

		obs, _ := m.Float64ObservableCounter("cornus.test.observable")
		_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			o.ObserveFloat64(obs, 3.25)
			return nil
		}, obs)
	})

	got := metricsByName(req)

	// An Int64Counter must survive as an INT datapoint, not be widened to
	// double: a byte counter past 2^53 would start losing its low bits, and byte
	// counters are most of what rides this bridge.
	c := got["cornus.test.counter"]
	if c == nil || c.GetSum() == nil {
		t.Fatalf("counter missing or not a Sum: %v", c)
	}
	if !c.GetSum().IsMonotonic {
		t.Error("counter is not monotonic")
	}
	p := c.GetSum().DataPoints[0]
	if p.GetAsInt() != 7 {
		t.Errorf("counter value = %v (as_double %v), want the integer 7", p.GetAsInt(), p.GetAsDouble())
	}
	if c.Unit != "By" {
		t.Errorf("counter unit = %q, want \"By\"", c.Unit)
	}
	if len(p.Attributes) != 1 || p.Attributes[0].Key != "direction" {
		t.Errorf("counter attributes = %v, want direction=rx", p.Attributes)
	}

	// Histograms are the shape cornus.build.duration needs and the one an
	// encoder is most likely to omit.
	h := got["cornus.test.duration"]
	if h == nil || h.GetHistogram() == nil {
		t.Fatalf("histogram missing or wrong shape: %v", h)
	}
	hp := h.GetHistogram().DataPoints[0]
	if hp.Count != 1 || hp.GetSum() != 1.5 {
		t.Errorf("histogram count/sum = %d/%v, want 1/1.5", hp.Count, hp.GetSum())
	}
	if len(hp.BucketCounts) == 0 || len(hp.ExplicitBounds) == 0 {
		t.Error("histogram carries no buckets, so a quantile cannot be computed from it")
	}

	if g := got["cornus.test.gauge"]; g == nil || g.GetSum() == nil || g.GetSum().IsMonotonic {
		t.Errorf("up-down counter missing or reported as monotonic: %v", g)
	}
	if o := got["cornus.test.observable"]; o == nil || o.GetSum() == nil {
		t.Errorf("observable counter missing or not a Sum: %v", o)
	}
}

// Cumulative, not delta. Delta temporality would make every rate() over the
// stored series read as the total since the process started.
func TestBridgeExportsCumulativeTemporality(t *testing.T) {
	if got := (sinkExporter{}).Temporality(sdkmetric.InstrumentKindCounter); got != metricdata.CumulativeTemporality {
		t.Errorf("counter temporality = %v, want cumulative", got)
	}
	req := collectThrough(t, func(m metric.Meter) {
		c, _ := m.Int64Counter("cornus.test.counter")
		c.Add(context.Background(), 1)
	})
	sum := metricsByName(req)["cornus.test.counter"].GetSum()
	if sum.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Errorf("temporality = %v, want cumulative", sum.AggregationTemporality)
	}
}

// With no sink registered the exporter must drop quietly. The reader is
// installed by Setup at process start and the destination is filled in later by
// the server, so this window is a normal part of startup, not an error.
func TestExportWithoutASinkIsNotAnError(t *testing.T) {
	SetMetricSink(nil)
	rm := &metricdata.ResourceMetrics{}
	if err := (sinkExporter{}).Export(context.Background(), rm); err != nil {
		t.Errorf("Export with no sink = %v, want nil", err)
	}
}

// An empty collection must not produce an envelope. A zero-metric export is
// legal OTLP, but forwarding it upstream every interval is pure noise.
func TestEmptyCollectionEncodesToNothing(t *testing.T) {
	body, err := encodeResourceMetrics(&metricdata.ResourceMetrics{})
	if err != nil {
		t.Fatalf("encodeResourceMetrics: %v", err)
	}
	if body != nil {
		t.Errorf("an empty collection encoded to %d bytes, want none", len(body))
	}
}
