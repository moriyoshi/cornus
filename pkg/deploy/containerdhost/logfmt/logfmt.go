// Package logfmt implements the on-disk container log record format shared by
// the log-writing shim process and the containerdhost backend's Logs read
// path. The format is JSON lines, one record per line, mirroring Docker's
// json-file logging driver:
//
//	{"time":"2026-01-02T15:04:05.999999999Z","stream":"stdout","log":"hello\n","partial":true}
package logfmt

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

// MaxChunk is the maximum number of log bytes carried by a single record.
// Writers split longer lines into Partial chunks, matching Docker's json-file
// driver behavior (16 KiB).
const MaxChunk = 16 * 1024

// maxLine bounds a single encoded JSON line when reading. A MaxChunk payload
// can expand up to 6x under JSON escaping (\u00XX per byte) plus fixed field
// overhead, so size generously.
const maxLine = 128 * 1024

// Record is one log record. Log carries the raw chunk bytes, usually ending
// in "\n". Partial marks a chunk of a longer line that was split at MaxChunk;
// the terminating chunk of the line has Partial set to false.
type Record struct {
	Time    time.Time `json:"time"`
	Stream  string    `json:"stream"`
	Log     string    `json:"log"`
	Partial bool      `json:"partial,omitempty"`
}

// Writer encodes records as JSON lines. It is safe for concurrent use by
// multiple goroutines (e.g. one stdout and one stderr copier).
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns a Writer emitting JSON lines to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteRecord encodes r as a single JSON line. The line is written with one
// Write call under a mutex so records from concurrent goroutines never
// interleave.
func (w *Writer) WriteRecord(r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(b)
	return err
}

// Copy reads r until EOF and emits one record per line on the given stream
// ("stdout" or "stderr"). Lines longer than MaxChunk are split into Partial
// chunks; the chunk carrying the terminating newline has Partial=false. A
// final unterminated line is emitted as-is without a trailing newline. now
// supplies record timestamps (parameterized for tests). Copy returns nil when
// r reaches EOF.
func (w *Writer) Copy(stream string, r io.Reader, now func() time.Time) error {
	br := bufio.NewReaderSize(r, MaxChunk)
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			rec := Record{
				Time:    now(),
				Stream:  stream,
				Log:     string(chunk),
				Partial: errors.Is(err, bufio.ErrBufferFull),
			}
			if werr := w.WriteRecord(rec); werr != nil {
				return werr
			}
		}
		switch {
		case err == nil || errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil
		default:
			return err
		}
	}
}

// Reader decodes records from a JSON-lines stream produced by Writer.
type Reader struct {
	sc *bufio.Scanner
}

// NewReader returns a Reader over r.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	return &Reader{sc: sc}
}

// Next returns the next record, or io.EOF at end of stream. A line that fails
// to parse and is the last line of the stream (a crash mid-write left it
// truncated) yields io.EOF; a corrupt line followed by valid lines is skipped.
func (r *Reader) Next() (Record, error) {
	for {
		if !r.sc.Scan() {
			if err := r.sc.Err(); err != nil {
				return Record{}, err
			}
			return Record{}, io.EOF
		}
		line := r.sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			// Truncated or corrupt line. If it is the last line, the next
			// Scan returns false and we report io.EOF; otherwise skip it.
			continue
		}
		return rec, nil
	}
}

// Filter selects records by stream and time.
type Filter struct {
	Stdout bool
	Stderr bool
	Since  time.Time
	Until  time.Time
}

// Match reports whether r passes the filter: its stream must be selected, and
// when Since is non-zero the record time must not be before Since (records at
// exactly Since match), and when Until is non-zero the record time must be
// before Until (records at exactly Until are excluded, matching docker --until).
func (f Filter) Match(r Record) bool {
	switch r.Stream {
	case "stdout":
		if !f.Stdout {
			return false
		}
	case "stderr":
		if !f.Stderr {
			return false
		}
	default:
		return false
	}
	if !f.Since.IsZero() && r.Time.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !r.Time.Before(f.Until) {
		return false
	}
	return true
}

// Tail returns the records making up the last n lines, the way docker tail
// counts them: only records that terminate a line (Partial=false) count, and
// a run of Partial chunks belongs to the line its terminating record ends, so
// the whole run is included. n < 0 means all records; n == 0 means none.
func Tail(recs []Record, n int) []Record {
	if n < 0 {
		return recs
	}
	if n == 0 {
		return recs[len(recs):]
	}
	count := 0
	i := len(recs)
	for i > 0 {
		i--
		if !recs[i].Partial {
			count++
			if count == n {
				// Include the partial run preceding this terminating record.
				for i > 0 && recs[i-1].Partial {
					i--
				}
				return recs[i:]
			}
		}
	}
	return recs
}
