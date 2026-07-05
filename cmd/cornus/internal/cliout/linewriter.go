package cliout

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// LineWriter returns a concurrency-safe, line-buffered writer over base. Writes
// are buffered until a newline, then the whole line is emitted under a lock so
// concurrent streams sharing a channel never interleave a partial line — the
// driver-owned replacement for compose's syncWriter/prefixWriter.
//
// In plain and fancy modes each line is emitted as prefix+line (fancy dims the
// prefix). In json mode each line becomes {"type":"log","tag":tag,"line":line}
// NDJSON, where tag is prefix with trailing separators/spaces trimmed. Close
// flushes any trailing partial line.
//
// When base is the driver's own stdout or stderr, all LineWriters over that
// channel share one mutex, so writers to the same stream serialize against each
// other; over any other base each writer gets its own mutex.
func (d *Driver) LineWriter(base io.Writer, prefix string) io.WriteCloser {
	return &lineWriter{
		d:      d,
		base:   base,
		mu:     d.muFor(base),
		prefix: prefix,
		tag:    strings.TrimRight(prefix, " |\t"),
	}
}

// muFor returns the mutex guarding writes to base: the shared per-channel mutex
// for stdout/stderr, or a fresh one otherwise.
func (d *Driver) muFor(base io.Writer) *sync.Mutex {
	switch base {
	case d.out:
		return &d.outMu
	case d.err:
		return &d.errMu
	default:
		return &sync.Mutex{}
	}
}

// LineGroup creates line writers that all share one mutex, so lines written
// through any of them never interleave — even across different bases (stdout and
// stderr). This is the compose multi-service log case: every service's stdout
// and stderr writer serializes against every other, matching the single shared
// mutex compose used before the driver owned line-writing.
type LineGroup struct {
	d  *Driver
	mu sync.Mutex
}

// LineGroup returns a fresh group whose Writers share one mutex.
func (d *Driver) LineGroup() *LineGroup { return &LineGroup{d: d} }

// Writer returns a concurrency-safe, line-buffered writer over base with the
// group's shared mutex. Formatting follows the driver mode exactly like
// Driver.LineWriter (prefix in human modes, NDJSON in json mode).
func (g *LineGroup) Writer(base io.Writer, prefix string) io.WriteCloser {
	return &lineWriter{
		d:      g.d,
		base:   base,
		mu:     &g.mu,
		prefix: prefix,
		tag:    strings.TrimRight(prefix, " |\t"),
	}
}

type lineWriter struct {
	d      *Driver
	base   io.Writer
	mu     *sync.Mutex
	prefix string
	tag    string

	bufMu sync.Mutex // guards buf against a caller's own concurrent Writes
	buf   bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.bufMu.Lock()
	defer w.bufMu.Unlock()
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.buf.Write(p)
			break
		}
		w.buf.Write(p[:i+1])
		w.emit(w.buf.String())
		w.buf.Reset()
		p = p[i+1:]
	}
	return n, nil
}

// Close flushes a trailing partial line (one without a newline).
func (w *lineWriter) Close() error {
	w.bufMu.Lock()
	defer w.bufMu.Unlock()
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
	return nil
}

func (w *lineWriter) emit(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Route streamed log lines above any live progress region, so a spinner and a
	// stream of service logs coexist without the log tearing the spinner.
	if w.d.routeAbove(func(b *strings.Builder) { w.d.r.logLine(b, w.prefix, w.tag, line) }) {
		return
	}
	w.d.r.logLine(w.base, w.prefix, w.tag, line)
}
