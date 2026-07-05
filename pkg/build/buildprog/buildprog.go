// Package buildprog is the neutral, BuildKit-free boundary for build progress.
// The build engine projects BuildKit's status stream into buildprog.Events and
// hands them to a Sink; the CLI turns a Sink into rendered output (plain text,
// colored text, or NDJSON) through NewSink. Because this package imports no
// BuildKit, both endpoints of a remote build — the caller (any OS) and the
// server — can forward and render events without linking the solver, preserving
// the module's "remote build client links zero BuildKit" invariant.
package buildprog

import (
	"encoding/json"
	"fmt"
	"io"
)

// Event is one build-progress update. A vertex event reports a build step's
// state transition; a log event carries verbatim output (a RUN command's
// stdout/stderr, or a cornus marker line). The two are distinguished by which
// fields are set: Vertex/Status for a step, Log for output.
type Event struct {
	Vertex string `json:"vertex,omitempty"` // build-step name
	Status string `json:"status,omitempty"` // "start" | "done" | "cached" | "error"
	Error  string `json:"error,omitempty"`  // step error message, if Status=="error"
	Log    string `json:"log,omitempty"`    // verbatim log/marker text (may include newlines)
}

// Sink consumes build-progress events. A nil Sink is a valid no-op (see Call).
type Sink func(Event)

// Call invokes s with e unless s is nil, so callers need not nil-check.
func (s Sink) Call(e Event) {
	if s != nil {
		s(e)
	}
}

// Log is a convenience for emitting a verbatim log/marker line.
func (s Sink) Log(format string, a ...any) {
	s.Call(Event{Log: fmt.Sprintf(format, a...)})
}

// Mode selects how NewSink renders events.
type Mode int

const (
	// Plain writes deterministic, ANSI-free text.
	Plain Mode = iota
	// Fancy writes the same text with color on the step lines.
	Fancy
	// JSON writes one NDJSON object per event.
	JSON
)

// NewSink returns a Sink that renders each event to w for the given mode. A nil
// w discards. In Plain/Fancy mode log text is written verbatim (so a RUN
// command's output survives as a substring, which the E2E harness relies on) and
// each step transition is one line; Fancy colors the step lines. In JSON mode
// every event becomes one NDJSON object.
func NewSink(w io.Writer, mode Mode, color bool) Sink {
	if w == nil {
		return func(Event) {}
	}
	if mode == JSON {
		return func(e Event) {
			b, err := json.Marshal(e)
			if err != nil {
				return
			}
			w.Write(append(b, '\n'))
		}
	}
	return func(e Event) {
		if e.Log != "" {
			io.WriteString(w, e.Log)
		}
		if e.Vertex == "" {
			return
		}
		line := stepLine(e)
		if color && mode == Fancy {
			line = colorFor(e.Status) + line + reset
		}
		fmt.Fprintln(w, line)
	}
}

func stepLine(e Event) string {
	switch e.Status {
	case "done":
		return "=> " + e.Vertex + " done"
	case "cached":
		return "=> " + e.Vertex + " cached"
	case "error":
		if e.Error != "" {
			return "=> " + e.Vertex + " ERROR: " + e.Error
		}
		return "=> " + e.Vertex + " ERROR"
	default: // "start"
		return "=> " + e.Vertex
	}
}

const reset = "\x1b[0m"

func colorFor(status string) string {
	switch status {
	case "done", "cached":
		return "\x1b[32m" // green
	case "error":
		return "\x1b[31m" // red
	default:
		return "\x1b[36m" // cyan
	}
}
