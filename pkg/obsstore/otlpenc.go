package obsstore

import (
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// ScopeName is the instrumentation scope stamped on records cornus itself
// produces, so a reader can tell a line the server recorded off a container's
// stdout from one the app's own SDK exported.
const ScopeName = "cornus/obsstore"

// Record is one log record to encode. It is deliberately flat: the only producer
// is the server's log recorder, which has a line, a timestamp, and a fixed set of
// attributes — nothing that warrants exposing the full OTLP shape.
type Record struct {
	Time time.Time
	// Severity is an OTLP severity number. Zero means unspecified, which is the
	// honest value for a plain stdout line: cornus does not guess a level by
	// pattern-matching the text.
	Severity     int
	SeverityText string
	Body         string
	Attributes   map[string]string
}

// EncodeLogs builds the OTLP/HTTP export-request protobuf for one resource's
// records — the exact bytes an OTLP exporter would put on the wire, which is
// also exactly what Store.IngestLogs takes.
//
// Encoding here rather than in the recorder keeps the store's two feeds
// symmetric: whether a record came from a container's stdout or from the app's
// own SDK, it reaches the engine as the same wire format, so nothing downstream
// has to know which produced it.
func EncodeLogs(resource map[string]string, records []Record) ([]byte, error) {
	if len(records) == 0 {
		return nil, nil
	}
	out := make([]*logspb.LogRecord, 0, len(records))
	for _, r := range records {
		ts := uint64(0)
		if !r.Time.IsZero() {
			ts = uint64(r.Time.UnixNano())
		}
		out = append(out, &logspb.LogRecord{
			TimeUnixNano: ts,
			// Observed time is when cornus saw the line. For the stdout feed
			// the two are the same by construction; setting it anyway keeps
			// records valid for readers that fall back to it.
			ObservedTimeUnixNano: ts,
			SeverityNumber:       logspb.SeverityNumber(r.Severity),
			SeverityText:         r.SeverityText,
			Body: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_StringValue{StringValue: r.Body},
			},
			Attributes: keyValues(r.Attributes),
		})
	}
	req := &collogs.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: keyValues(resource)},
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commonpb.InstrumentationScope{Name: ScopeName},
				LogRecords: out,
			}},
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("obsstore: encode logs: %w", err)
	}
	return b, nil
}

// keyValues renders a string map as OTLP attributes, key-sorted so the encoded
// bytes are deterministic for a given input (which is what makes the encoder
// testable by comparison rather than by re-decoding).
func keyValues(m map[string]string) []*commonpb.KeyValue {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*commonpb.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, &commonpb.KeyValue{
			Key:   k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: m[k]}},
		})
	}
	return out
}
