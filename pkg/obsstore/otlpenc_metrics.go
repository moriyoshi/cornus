package obsstore

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// The metrics half of the encoder, alongside EncodeLogs.
//
// It exists for the same reason: cornus's own collectors produce readings, and
// the store ingests OTLP/HTTP protobuf, so something has to sit between them.
// Doing it here rather than in the collector keeps the store's feeds symmetric —
// a datapoint sampled off a cgroup and one exported by an app's own SDK reach
// the engine as the same wire format, and nothing downstream has to know which
// produced it.
//
// The subset is deliberately small: sums and gauges of float64, which is what a
// resource sampler produces. Histograms belong to the SDK bridge (see
// pkg/observability), which has a real metricdata.Histogram to translate; adding
// a histogram shape here that nothing constructs would be untested surface.

// MetricKind selects a datapoint's aggregation.
type MetricKind int

const (
	// KindGauge is a value that means something on its own at the instant it was
	// read — memory in use, a limit, a process count.
	KindGauge MetricKind = iota
	// KindSum is a cumulative counter that only ever increases, meaningful as a
	// rate. Bytes transferred and CPU time are counters; recording them as
	// gauges would leave a reader unable to ask "how fast".
	KindSum
)

// Metric is one metric with its datapoints.
type Metric struct {
	Name        string
	Description string
	// Unit is a UCUM code, as OTel semantic conventions specify: "s", "By",
	// "{cpu}", "{process}". Grafana and Prometheus translation both key off it,
	// so an omitted unit costs a correctly-suffixed Prometheus name.
	Unit   string
	Kind   MetricKind
	Points []NumberPoint
}

// NumberPoint is one reading.
type NumberPoint struct {
	// Time is when the value was observed.
	Time time.Time
	// Start is when the counter this value belongs to began accumulating. It is
	// meaningful only for KindSum, where a consumer needs it to tell a genuine
	// reset apart from a counter that simply restarted at zero. Zero means
	// unknown, which is honest for a counter cornus reads rather than owns.
	Start time.Time
	Value float64
	// Attributes are the datapoint's labels. These, NOT the resource attributes,
	// are what a PromQL query filters and groups on, so anything a reader will
	// want to slice by (the replica ordinal, an interface name, a direction)
	// belongs here.
	Attributes map[string]string
}

// EncodeMetrics builds the OTLP/HTTP export-request protobuf for one resource's
// metrics — the exact bytes an OTLP exporter would put on the wire, which is
// also exactly what Store.IngestMetrics takes.
func EncodeMetrics(resource map[string]string, metrics []Metric) ([]byte, error) {
	if len(metrics) == 0 {
		return nil, nil
	}
	out := make([]*metricspb.Metric, 0, len(metrics))
	for _, m := range metrics {
		if len(m.Points) == 0 {
			// A metric with no datapoints is not a zero reading, it is the
			// absence of a reading. Emitting the envelope anyway would create an
			// empty series that a dashboard renders as a gap-with-a-legend.
			continue
		}
		pts := make([]*metricspb.NumberDataPoint, 0, len(m.Points))
		for _, p := range m.Points {
			pts = append(pts, &metricspb.NumberDataPoint{
				TimeUnixNano:      uint64(p.Time.UnixNano()),
				StartTimeUnixNano: startNanos(p.Start),
				Value:             &metricspb.NumberDataPoint_AsDouble{AsDouble: p.Value},
				Attributes:        keyValues(p.Attributes),
			})
		}
		pb := &metricspb.Metric{Name: m.Name, Description: m.Description, Unit: m.Unit}
		switch m.Kind {
		case KindSum:
			pb.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				DataPoints: pts,
				// Cumulative, not delta: the datapoints are running totals read
				// from a counter cornus does not reset, and it is what a PromQL
				// store expects. Declaring delta here would make every rate()
				// read as the total since the beginning of time.
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
			}}
		default:
			pb.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: pts}}
		}
		out = append(out, pb)
	}
	if len(out) == 0 {
		return nil, nil
	}
	req := &colmetrics.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: keyValues(resource)},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: ScopeName},
				Metrics: out,
			}},
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("obsstore: encode metrics: %w", err)
	}
	return b, nil
}

// startNanos renders a start time, mapping the zero time to 0 ("unknown") rather
// than to the enormous negative nanosecond count time.Time's zero would produce.
func startNanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}
