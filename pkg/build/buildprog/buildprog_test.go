package buildprog

import (
	"bytes"
	"encoding/json"
	"go/build"
	"strings"
	"testing"
)

// events is a representative sequence: a step, its verbatim log output (which the
// E2E harness greps for), a completion, a cached step, and an error.
func events(sink Sink) {
	sink.Call(Event{Vertex: "[1/2] FROM alpine", Status: "start"})
	sink.Call(Event{Log: "LAZY-COPY-OK\n"})
	sink.Call(Event{Vertex: "[1/2] FROM alpine", Status: "done"})
	sink.Call(Event{Vertex: "[2/2] RUN build", Status: "cached"})
	sink.Call(Event{Vertex: "[bad] RUN false", Status: "error", Error: "exit code 1"})
}

func TestPlainSink(t *testing.T) {
	var b bytes.Buffer
	events(NewSink(&b, Plain, false))
	got := b.String()
	// Log data must appear verbatim (the E2E build scenarios grep for it).
	if !strings.Contains(got, "LAZY-COPY-OK\n") {
		t.Errorf("plain output dropped verbatim log data:\n%s", got)
	}
	for _, want := range []string{
		"=> [1/2] FROM alpine\n",
		"=> [1/2] FROM alpine done\n",
		"=> [2/2] RUN build cached\n",
		"=> [bad] RUN false ERROR: exit code 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plain output missing %q; got:\n%s", want, got)
		}
	}
	if bytes.IndexByte(b.Bytes(), 0x1b) >= 0 {
		t.Errorf("plain output contains an ANSI escape: %q", got)
	}
}

func TestJSONSink(t *testing.T) {
	var b bytes.Buffer
	events(NewSink(&b, JSON, false))
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 NDJSON lines, got %d:\n%s", len(lines), b.String())
	}
	var first Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 0 not valid JSON: %v", err)
	}
	if first.Vertex != "[1/2] FROM alpine" || first.Status != "start" {
		t.Errorf("unexpected first event: %+v", first)
	}
}

func TestFancyIsPlainPlusColor(t *testing.T) {
	var plain, fancy bytes.Buffer
	events(NewSink(&plain, Plain, false))
	events(NewSink(&fancy, Fancy, true))
	if bytes.IndexByte(fancy.Bytes(), 0x1b) < 0 {
		t.Errorf("fancy output should contain ANSI color")
	}
	// Stripping color from fancy yields the plain content.
	stripped := stripSGR(fancy.String())
	if stripped != plain.String() {
		t.Errorf("fancy (color-stripped) != plain:\n fancy %q\n plain %q", stripped, plain.String())
	}
}

func TestNilSinkSafe(t *testing.T) {
	var s Sink // nil
	s.Call(Event{Log: "x"})
	s.Log("y %d", 1)
	if got := NewSink(nil, Plain, false); got == nil {
		t.Fatal("NewSink(nil) must return a usable no-op sink")
	} else {
		got(Event{Log: "z"}) // must not panic
	}
}

// TestNoBuildKitImport guards the invariant that buildprog links zero BuildKit,
// so the remote-build client (and every non-Linux binary) can render progress
// without pulling the solver. It walks the package's transitive import set.
func TestNoBuildKitImport(t *testing.T) {
	seen := map[string]bool{}
	var walk func(path string)
	walk = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		pkg, err := build.Import(path, "", 0)
		if err != nil {
			return // stdlib pseudo-packages etc.
		}
		for _, imp := range pkg.Imports {
			if strings.Contains(imp, "moby/buildkit") {
				t.Errorf("buildprog transitively imports BuildKit via %s -> %s", path, imp)
			}
			if strings.HasPrefix(imp, "cornus/") {
				walk(imp)
			}
		}
	}
	walk("cornus/pkg/build/buildprog")
}

// stripSGR removes CSI escape sequences for the fancy-equals-plain assertion.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
