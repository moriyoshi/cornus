package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cornus/pkg/obsstore"
)

// linePrinter collects what a Result.Human renders, so the tests assert on the
// text a user actually sees.
type linePrinter struct{ lines []string }

func (p *linePrinter) Line(format string, a ...any) {
	p.lines = append(p.lines, strings.TrimRight(fmt.Sprintf(format, a...), " "))
}

func TestObserveTraceRendersAWaterfall(t *testing.T) {
	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	res := observeTraceResult{
		ID: "abc",
		Spans: []obsstore.Span{
			{TraceID: "abc", SpanID: "a", Name: "GET /checkout", Service: "web", Start: base, Duration: time.Second},
			{TraceID: "abc", SpanID: "b", ParentSpanID: "a", Name: "db.query", Service: "db", Start: base.Add(200 * time.Millisecond), Duration: 300 * time.Millisecond},
		},
	}
	p := &linePrinter{}
	res.Human(p)
	out := strings.Join(p.lines, "\n")

	if !strings.Contains(out, "GET /checkout") || !strings.Contains(out, "db.query") {
		t.Fatalf("waterfall lost a span:\n%s", out)
	}
	// The child must be indented under its parent — the nesting is the whole
	// point of rendering a tree rather than a list.
	var childLine string
	for _, l := range p.lines {
		if strings.Contains(l, "db.query") {
			childLine = l
		}
	}
	if !strings.HasPrefix(childLine, "  ") {
		t.Errorf("child span is not indented under its parent: %q", childLine)
	}
	if !strings.Contains(out, "2 spans") {
		t.Errorf("header does not report the span count:\n%s", out)
	}
}

// TestObserveTraceShowsOrphans: a partially collected trace is exactly when
// someone is reading this, so a span whose parent is missing must still print.
func TestObserveTraceShowsOrphans(t *testing.T) {
	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	res := observeTraceResult{
		ID: "abc",
		Spans: []obsstore.Span{
			{SpanID: "orphan", ParentSpanID: "missing", Name: "stranded", Start: base, Duration: time.Millisecond},
		},
	}
	p := &linePrinter{}
	res.Human(p)
	if !strings.Contains(strings.Join(p.lines, "\n"), "stranded") {
		t.Errorf("orphan span was dropped from the waterfall:\n%s", strings.Join(p.lines, "\n"))
	}
}

func TestObserveTraceEmpty(t *testing.T) {
	p := &linePrinter{}
	observeTraceResult{ID: "abc"}.Human(p)
	if len(p.lines) != 1 || !strings.Contains(p.lines[0], "no recorded spans") {
		t.Errorf("empty trace rendered as %v", p.lines)
	}
}

func TestWaterfallBar(t *testing.T) {
	total := time.Second
	// A span at the start occupies the left edge.
	if got := waterfallBar(0, total, total); !strings.HasPrefix(got, "█") {
		t.Errorf("full-width span = %q, want a bar starting at the left edge", got)
	}
	// A span late in the trace is indented.
	if got := waterfallBar(900*time.Millisecond, 100*time.Millisecond, total); !strings.HasPrefix(got, " ") {
		t.Errorf("late span = %q, want leading space", got)
	}
	// A span far shorter than one cell still happened; it must not vanish.
	if got := waterfallBar(0, time.Nanosecond, total); !strings.Contains(got, "█") {
		t.Errorf("sub-cell span = %q, want at least one cell", got)
	}
	// A zero-length trace must not divide by zero or overflow the width.
	if got := waterfallBar(0, 0, 0); len(got) == 0 {
		t.Error("zero-duration trace produced an empty bar")
	}
}

// TestObserveStatusAlwaysReportsDropped is the one number that decides whether
// an empty query result means "nothing happened" or "the evidence was shed", so
// it must print even when it is zero.
func TestObserveStatusAlwaysReportsDropped(t *testing.T) {
	p := &linePrinter{}
	observeStatusResult{Status: obsstore.Status{Dir: "/data/obs"}}.Human(p)
	out := strings.Join(p.lines, "\n")
	if !strings.Contains(out, "dropped") {
		t.Errorf("status omitted the dropped counter:\n%s", out)
	}

	p = &linePrinter{}
	observeStatusResult{Status: obsstore.Status{Dir: "/data/obs", Dropped: 42}}.Human(p)
	out = strings.Join(p.lines, "\n")
	if !strings.Contains(out, "42") || !strings.Contains(out, "may be missing data") {
		t.Errorf("a non-zero dropped count must warn that results are incomplete:\n%s", out)
	}
}

// TestObserveResultsMarshalEmptyAsArray keeps `--output json` consumers from
// having to handle null.
func TestObserveResultsMarshalEmptyAsArray(t *testing.T) {
	for name, r := range map[string]json.Marshaler{
		"logs":    observeLogsResult{},
		"traces":  observeTracesResult{},
		"trace":   observeTraceResult{},
		"metrics": observeMetricsResult{},
		"query":   observeQueryResult{},
	} {
		b, err := r.MarshalJSON()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(b) != "[]" {
			t.Errorf("%s empty result marshalled as %s, want []", name, b)
		}
	}
}

func TestLabelSetString(t *testing.T) {
	got := labelSetString(map[string]string{"host": "b", "az": "a"})
	if got != `{az="a", host="b"}` {
		t.Errorf("labelSetString = %s, want key-sorted PromQL notation", got)
	}
	if got := labelSetString(nil); got != "{}" {
		t.Errorf("empty label set = %s", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate kept = %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("truncate = %q, want abcd…", got)
	}
	// Runes, not bytes: slicing a multi-byte name mid-character would emit
	// broken UTF-8 into the user's terminal.
	if got := truncate("日本語のサービス", 4); got != "日本語…" {
		t.Errorf("truncate on multi-byte = %q, want 日本語…", got)
	}
}

func TestObserveLogsRendersService(t *testing.T) {
	p := &linePrinter{}
	observeLogsResult{Entries: []obsstore.LogEntry{
		{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Service: "web", Body: "hello"},
	}}.Human(p)
	out := strings.Join(p.lines, "\n")
	if !strings.Contains(out, "web") || !strings.Contains(out, "hello") {
		t.Errorf("log line rendered as %q", out)
	}

	p = &linePrinter{}
	observeLogsResult{}.Human(p)
	if len(p.lines) != 1 || !strings.Contains(p.lines[0], "no matching records") {
		t.Errorf("empty logs rendered as %v", p.lines)
	}
}
