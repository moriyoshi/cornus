package obsstore

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

func at(sec int) time.Time { return time.Unix(int64(sec), 0).UTC() }

// TestAssembleTraceBuildsForest covers the ordinary case: one root, two children,
// one grandchild, all reachable and stably ordered.
func TestAssembleTraceBuildsForest(t *testing.T) {
	spans := []Span{
		{SpanID: "b", ParentSpanID: "a", Start: at(2)},
		{SpanID: "a", Start: at(1)},
		{SpanID: "c", ParentSpanID: "a", Start: at(3)},
		{SpanID: "d", ParentSpanID: "b", Start: at(4)},
	}
	roots := AssembleTrace(spans)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if roots[0].SpanID != "a" {
		t.Fatalf("root = %q, want a", roots[0].SpanID)
	}
	if len(roots[0].Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(roots[0].Children))
	}
	if got := roots[0].Children[0].SpanID; got != "b" {
		t.Errorf("first child = %q, want b (earlier start)", got)
	}
	if got := roots[0].Children[1].SpanID; got != "c" {
		t.Errorf("second child = %q, want c", got)
	}
	if kids := roots[0].Children[0].Children; len(kids) != 1 || kids[0].SpanID != "d" {
		t.Errorf("grandchildren = %+v, want [d]", kids)
	}
}

// TestAssembleTraceSurfacesOrphansAsRoots is the important one: a partially
// collected trace is exactly when someone is looking at it, so a span whose
// parent is missing must still appear rather than being silently dropped.
func TestAssembleTraceSurfacesOrphansAsRoots(t *testing.T) {
	spans := []Span{
		{SpanID: "a", Start: at(1)},
		{SpanID: "orphan", ParentSpanID: "missing", Start: at(2)},
	}
	roots := AssembleTrace(spans)
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2 (the real root plus the orphan)", len(roots))
	}
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r.SpanID] = true
	}
	if !seen["orphan"] {
		t.Errorf("orphan was dropped; roots = %v", seen)
	}
}

// TestAssembleTraceIsStableRegardlessOfInputOrder pins the ordering contract:
// the same spans in a different order must produce the same forest, because the
// implementation indexes through a map.
func TestAssembleTraceIsStableRegardlessOfInputOrder(t *testing.T) {
	forward := []Span{
		{SpanID: "a", Start: at(1)},
		{SpanID: "b", ParentSpanID: "a", Start: at(2)},
		{SpanID: "c", ParentSpanID: "a", Start: at(2)}, // same start: ties break on span id
	}
	reversed := []Span{forward[2], forward[1], forward[0]}

	got := AssembleTrace(reversed)
	if len(got) != 1 || len(got[0].Children) != 2 {
		t.Fatalf("unexpected shape: %d roots", len(got))
	}
	if got[0].Children[0].SpanID != "b" || got[0].Children[1].SpanID != "c" {
		t.Errorf("children = %q,%q; want b,c (tie broken on span id)",
			got[0].Children[0].SpanID, got[0].Children[1].SpanID)
	}
}

// TestAssembleTraceDropsDuplicateSpanIDs keeps the first occurrence, so a
// re-delivered span cannot fork the tree.
func TestAssembleTraceDropsDuplicateSpanIDs(t *testing.T) {
	spans := []Span{
		{SpanID: "a", Name: "first", Start: at(1)},
		{SpanID: "a", Name: "second", Start: at(9)},
	}
	roots := AssembleTrace(spans)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if roots[0].Name != "first" {
		t.Errorf("kept %q, want the first occurrence", roots[0].Name)
	}
}

func TestAssembleTraceEmpty(t *testing.T) {
	if got := AssembleTrace(nil); got != nil {
		t.Errorf("AssembleTrace(nil) = %v, want nil", got)
	}
}

// TestEncodeLogsRoundTrips proves the encoder emits a real OTLP export-request:
// the bytes must decode as one, with the resource attributes, the body, and the
// timestamp intact. This runs in the default build, so the wire format stays
// covered even where the engine is not compiled in.
func TestEncodeLogsRoundTrips(t *testing.T) {
	when := time.Unix(1700000000, 123).UTC()
	b, err := EncodeLogs(
		map[string]string{"service.name": "web", "cornus.deployment": "web"},
		[]Record{{
			Time:         when,
			Severity:     SeverityError,
			SeverityText: "ERROR",
			Body:         "connection refused",
			Attributes:   map[string]string{"cornus.stream": "stderr"},
		}},
	)
	if err != nil {
		t.Fatalf("EncodeLogs: %v", err)
	}

	var req collogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(b, &req); err != nil {
		t.Fatalf("emitted bytes are not an ExportLogsServiceRequest: %v", err)
	}
	if len(req.ResourceLogs) != 1 || len(req.ResourceLogs[0].ScopeLogs) != 1 {
		t.Fatalf("unexpected envelope shape: %+v", &req)
	}
	sl := req.ResourceLogs[0].ScopeLogs[0]
	if sl.Scope.GetName() != ScopeName {
		t.Errorf("scope = %q, want %q", sl.Scope.GetName(), ScopeName)
	}
	if len(sl.LogRecords) != 1 {
		t.Fatalf("records = %d, want 1", len(sl.LogRecords))
	}
	rec := sl.LogRecords[0]
	if got := rec.Body.GetStringValue(); got != "connection refused" {
		t.Errorf("body = %q", got)
	}
	if got := int64(rec.TimeUnixNano); got != when.UnixNano() {
		t.Errorf("time = %d, want %d", got, when.UnixNano())
	}
	if rec.ObservedTimeUnixNano != rec.TimeUnixNano {
		t.Errorf("observed time %d != event time %d", rec.ObservedTimeUnixNano, rec.TimeUnixNano)
	}
	if int(rec.SeverityNumber) != SeverityError {
		t.Errorf("severity = %d, want %d", rec.SeverityNumber, SeverityError)
	}

	res := map[string]string{}
	for _, kv := range req.ResourceLogs[0].Resource.Attributes {
		res[kv.Key] = kv.Value.GetStringValue()
	}
	if res["service.name"] != "web" || res["cornus.deployment"] != "web" {
		t.Errorf("resource attributes = %v", res)
	}
}

// TestEncodeLogsIsDeterministic matters because the encoder sorts attribute
// keys: without that, two identical batches would produce different bytes and
// nothing downstream could be compared or cached.
func TestEncodeLogsIsDeterministic(t *testing.T) {
	res := map[string]string{"b": "2", "a": "1", "c": "3"}
	recs := []Record{{Time: at(1), Body: "x", Attributes: map[string]string{"z": "1", "y": "2"}}}
	first, err := EncodeLogs(res, recs)
	if err != nil {
		t.Fatalf("EncodeLogs: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := EncodeLogs(res, recs)
		if err != nil {
			t.Fatalf("EncodeLogs: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("encoding is not deterministic across calls (iteration %d)", i)
		}
	}
}

func TestEncodeLogsEmpty(t *testing.T) {
	b, err := EncodeLogs(map[string]string{"service.name": "web"}, nil)
	if err != nil {
		t.Fatalf("EncodeLogs: %v", err)
	}
	if b != nil {
		t.Errorf("EncodeLogs(no records) = %d bytes, want nil", len(b))
	}
}
