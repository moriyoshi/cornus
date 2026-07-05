package logfmt

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func readAll(t *testing.T, buf *bytes.Buffer) []Record {
	t.Helper()
	r := NewReader(bytes.NewReader(buf.Bytes()))
	var recs []Record
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			return recs
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		recs = append(recs, rec)
	}
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ts := time.Date(2026, 1, 2, 15, 4, 5, 999999999, time.UTC)
	in := []Record{
		{Time: ts, Stream: "stdout", Log: "hello\n"},
		{Time: ts.Add(time.Second), Stream: "stderr", Log: "oops\n"},
		{Time: ts.Add(2 * time.Second), Stream: "stdout", Log: "chunk", Partial: true},
		{Time: ts.Add(3 * time.Second), Stream: "stdout", Log: "end\n"},
	}
	for _, r := range in {
		if err := w.WriteRecord(r); err != nil {
			t.Fatalf("WriteRecord: %v", err)
		}
	}
	got := readAll(t, &buf)
	if len(got) != len(in) {
		t.Fatalf("got %d records, want %d", len(got), len(in))
	}
	for i := range in {
		if !got[i].Time.Equal(in[i].Time) || got[i].Stream != in[i].Stream ||
			got[i].Log != in[i].Log || got[i].Partial != in[i].Partial {
			t.Errorf("record %d: got %+v, want %+v", i, got[i], in[i])
		}
	}
}

func TestConcurrentWriters(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	const per = 200
	ts := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for _, stream := range []string{"stdout", "stderr"} {
		wg.Add(1)
		go func(stream string) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				err := w.WriteRecord(Record{Time: ts, Stream: stream, Log: "line\n"})
				if err != nil {
					t.Errorf("WriteRecord: %v", err)
					return
				}
			}
		}(stream)
	}
	wg.Wait()
	recs := readAll(t, &buf)
	if len(recs) != 2*per {
		t.Fatalf("got %d records, want %d", len(recs), 2*per)
	}
	counts := map[string]int{}
	for _, r := range recs {
		if r.Log != "line\n" {
			t.Fatalf("corrupt record: %+v", r)
		}
		counts[r.Stream]++
	}
	if counts["stdout"] != per || counts["stderr"] != per {
		t.Fatalf("stream counts %v, want %d each", counts, per)
	}
}

func TestCopySplitsLongLines(t *testing.T) {
	long := strings.Repeat("a", MaxChunk+100) + "\n"
	input := "short\n" + long + "tail-no-newline"
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ts := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	if err := w.Copy("stdout", strings.NewReader(input), func() time.Time { return ts }); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	recs := readAll(t, &buf)
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4: %+v", len(recs), recs)
	}
	if recs[0].Log != "short\n" || recs[0].Partial {
		t.Errorf("record 0: %+v", recs[0])
	}
	if !recs[1].Partial || len(recs[1].Log) != MaxChunk {
		t.Errorf("record 1: partial=%v len=%d, want partial MaxChunk", recs[1].Partial, len(recs[1].Log))
	}
	if recs[2].Partial || recs[1].Log+recs[2].Log != long {
		t.Errorf("long line did not reassemble exactly")
	}
	if recs[3].Log != "tail-no-newline" || recs[3].Partial {
		t.Errorf("record 3: %+v", recs[3])
	}
	var all strings.Builder
	for _, r := range recs {
		all.WriteString(r.Log)
	}
	if all.String() != input {
		t.Errorf("concatenated logs differ from input")
	}
}

func TestReaderToleratesTruncatedFinalLine(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ts := time.Now().UTC()
	if err := w.WriteRecord(Record{Time: ts, Stream: "stdout", Log: "ok\n"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: a truncated JSON line at the end.
	buf.WriteString(`{"time":"2026-01-02T15:04:05Z","stream":"std`)
	r := NewReader(bytes.NewReader(buf.Bytes()))
	rec, err := r.Next()
	if err != nil || rec.Log != "ok\n" {
		t.Fatalf("first record: %+v, %v", rec, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("truncated final line: got err %v, want io.EOF", err)
	}
}

func TestReaderSkipsCorruptMiddleLine(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ts := time.Now().UTC()
	if err := w.WriteRecord(Record{Time: ts, Stream: "stdout", Log: "a\n"}); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("not json at all\n")
	if err := w.WriteRecord(Record{Time: ts, Stream: "stdout", Log: "b\n"}); err != nil {
		t.Fatal(err)
	}
	recs := readAll(t, &buf)
	if len(recs) != 2 || recs[0].Log != "a\n" || recs[1].Log != "b\n" {
		t.Fatalf("got %+v, want the two valid records", recs)
	}
}

func TestFilter(t *testing.T) {
	since := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	out := Record{Time: since, Stream: "stdout", Log: "o\n"}
	errRec := Record{Time: since.Add(time.Second), Stream: "stderr", Log: "e\n"}
	old := Record{Time: since.Add(-time.Nanosecond), Stream: "stdout", Log: "old\n"}

	f := Filter{Stdout: true, Stderr: false}
	if !f.Match(out) || f.Match(errRec) {
		t.Errorf("stream selection failed")
	}
	f = Filter{Stdout: false, Stderr: true}
	if f.Match(out) || !f.Match(errRec) {
		t.Errorf("stream selection failed")
	}

	f = Filter{Stdout: true, Stderr: true, Since: since}
	if !f.Match(out) {
		t.Errorf("record at exactly Since must match")
	}
	if f.Match(old) {
		t.Errorf("record before Since must not match")
	}
	if !f.Match(errRec) {
		t.Errorf("record after Since must match")
	}

	// Zero Since matches everything stream-wise selected.
	f = Filter{Stdout: true, Stderr: true}
	if !f.Match(old) {
		t.Errorf("zero Since must not filter by time")
	}
}

func TestTail(t *testing.T) {
	recs := []Record{
		{Log: "1\n"},               // line 1
		{Log: "2a", Partial: true}, // line 2 ...
		{Log: "2b", Partial: true}, // ...
		{Log: "2c\n"},              // line 2 end
		{Log: "3\n"},               // line 3
	}
	if got := Tail(recs, -1); len(got) != len(recs) {
		t.Errorf("n<0: got %d, want all %d", len(got), len(recs))
	}
	if got := Tail(recs, 0); len(got) != 0 {
		t.Errorf("n=0: got %d, want 0", len(got))
	}
	if got := Tail(recs, 1); len(got) != 1 || got[0].Log != "3\n" {
		t.Errorf("n=1: got %+v", got)
	}
	// n=2 must include the entire partial run of line 2.
	if got := Tail(recs, 2); len(got) != 4 || got[0].Log != "2a" {
		t.Errorf("n=2: got %+v, want 4 records starting at 2a", got)
	}
	if got := Tail(recs, 3); len(got) != 5 {
		t.Errorf("n=3: got %d, want all 5", len(got))
	}
	if got := Tail(recs, 10); len(got) != 5 {
		t.Errorf("n>lines: got %d, want all 5", len(got))
	}
}

// TestFilterUntil checks the --until bound: records before Until pass, records
// at or after Until are excluded (docker --until semantics).
func TestFilterUntil(t *testing.T) {
	base := time.Unix(1000, 0)
	f := Filter{Stdout: true, Until: base}
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before until", base.Add(-time.Second), true},
		{"at until", base, false},
		{"after until", base.Add(time.Second), false},
	}
	for _, c := range cases {
		if got := f.Match(Record{Stream: "stdout", Time: c.at}); got != c.want {
			t.Errorf("%s: Match = %v, want %v", c.name, got, c.want)
		}
	}
	// Since + Until together bound a window [Since, Until).
	win := Filter{Stdout: true, Since: base, Until: base.Add(10 * time.Second)}
	if !win.Match(Record{Stream: "stdout", Time: base.Add(5 * time.Second)}) {
		t.Error("record inside [Since,Until) window should pass")
	}
	if win.Match(Record{Stream: "stdout", Time: base.Add(-time.Second)}) {
		t.Error("record before Since should be excluded")
	}
}
