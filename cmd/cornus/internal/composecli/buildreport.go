package composecli

import (
	"io"
	"strings"
	"sync"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/build/buildprog"
)

// buildReporter renders the progress of however many build groups
// runBuildGroups is running concurrently. One is created per `compose
// build`/`up --build` invocation and shared by every group's goroutine, so its
// state (the live Progress, and the serialization below) is common across
// them.
//
//   - fancy+TTY (prog.Live()): each group gets its own live one-line status
//     (see sinkFor), continuously updated with its most recent step/log line
//     instead of streaming the full log — matching `docker compose build`'s
//     default. The full log is still captured; on failure it is flushed,
//     labeled, through lines so the failure stays diagnosable.
//   - json mode: unchanged NDJSON of buildprog.Event, verbatim — just behind a
//     mutex so concurrent groups' events never split a Write call across each
//     other. Preserves the existing wire schema exactly (no consumer of
//     `--output json` build events should observe any difference).
//   - plain mode (and fancy without a TTY): the same full-stream rendering
//     compose always used, but through a cliout.LineGroup so concurrent
//     groups' output is line-buffered, "label | "-prefixed, and serialized
//     against each other instead of writing raw, unsynchronized bytes to the
//     shared stderr — the SAME concurrency-safety mechanism `compose logs`
//     already uses for multiple services' streamed output.
type buildReporter struct {
	d     *cliout.Driver
	prog  *cliout.Progress
	lines *cliout.LineGroup // plain/fancy-non-live full-stream target
	jsonW io.Writer         // json mode's shared, mutex-serialized target
}

// newBuildReporter starts prog (the caller must Stop it once every group is
// done) and returns a reporter ready for concurrent sinkFor calls.
func newBuildReporter(d *cliout.Driver) *buildReporter {
	return &buildReporter{
		d:     d,
		prog:  d.Progress(),
		lines: d.LineGroup(),
		jsonW: &syncWriter{base: d.Err()},
	}
}

// sinkFor returns a build-progress sink for one group (label identifies it,
// e.g. "web" or "web, worker" for a deduplicated build) plus a finish func the
// caller must invoke exactly once with the build's outcome.
func (br *buildReporter) sinkFor(label string) (buildprog.Sink, func(err error)) {
	switch {
	case br.d.Mode() == cliout.ModeJSON:
		sink := buildprog.NewSink(br.jsonW, buildprog.JSON, false)
		return sink, func(error) {}
	case br.prog.Live():
		return br.quietSink(label)
	default:
		w := br.lines.Writer(br.d.Err(), label+" | ")
		sink := buildprog.NewSink(w, buildprog.Plain, false)
		return sink, func(error) { w.Close() }
	}
}

// quietSink is the fancy+TTY path: a live Task updated with the group's most
// recent step/log line, finishing as a ✓/✗ summary. The full log is captured
// alongside so a failure can still be diagnosed (the live line only ever shows
// the latest one), flushed through br.lines on error so it stays serialized
// against any other group's concurrent output.
func (br *buildReporter) quietSink(label string) (buildprog.Sink, func(err error)) {
	task := br.prog.Task(label + ": building")
	var log strings.Builder
	sink := buildprog.Sink(func(e buildprog.Event) {
		if e.Log != "" {
			log.WriteString(e.Log)
			if line := lastNonEmptyLine(e.Log); line != "" {
				task.Update(label + ": " + line)
			}
		}
		if e.Vertex != "" {
			task.Update(label + ": " + buildStepSummary(e))
		}
	})
	return sink, func(err error) {
		if err != nil {
			task.Fail(label + ": build failed")
			if log.Len() > 0 {
				w := br.lines.Writer(br.d.Err(), label+" | ")
				io.WriteString(w, strings.TrimRight(log.String(), "\n")+"\n")
				w.Close()
			}
			return
		}
		task.Done(label + ": built")
	}
}

// buildStepSummary renders a build-progress vertex event as a short label
// suffix, mirroring buildprog's own step-line text without the leading "=> ".
func buildStepSummary(e buildprog.Event) string {
	switch e.Status {
	case "done":
		return e.Vertex + " done"
	case "cached":
		return e.Vertex + " cached"
	case "error":
		if e.Error != "" {
			return e.Vertex + " ERROR: " + e.Error
		}
		return e.Vertex + " ERROR"
	default: // "start"
		return e.Vertex
	}
}

// lastNonEmptyLine returns the last non-blank line of s (which may contain
// embedded newlines and a trailing partial line), or "" if s has none.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// syncWriter serializes concurrent Write calls to base under one mutex,
// without any line-buffering/framing — used for json mode, where each write is
// already one complete NDJSON object and must reach base as exactly the bytes
// given (any reinterpretation, e.g. through a LineWriter, would change the
// wire schema).
type syncWriter struct {
	mu   sync.Mutex
	base io.Writer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.base.Write(p)
}
