//go:build linux

package containerdhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/containerd/containerd/cio"
	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/containerdhost/logfmt"
)

// logShimArg is the binary-log-URI query key carrying the log file path. The
// runc shim turns each query param into an argv pair "key value"
// (containerd's process.NewBinaryCmd), so the key doubles as the hidden
// cornus subcommand name and the value as its positional argument:
// `cornus containerd-log-shim <path>`. That top-level spelling is a hidden alias
// of the canonical `cornus daemon containerd-log-shim` (both registered in
// cmd/cornus); the URI must target the alias because NewBinaryCmd cannot address
// a nested `daemon` subcommand. A single key is deliberate: NewBinaryCmd iterates
// the query map and takes only each key's first value, so multiple keys would
// produce a nondeterministic argv order and extra values would be dropped.
const logShimArg = "containerd-log-shim"

// followPollInterval is how often a follow re-checks the log file for growth.
const followPollInterval = 200 * time.Millisecond

// logPath returns the instance's log file path: <dataDir>/containerd/logs/<id>.log
func (b *Backend) logPath(id string) string {
	return filepath.Join(b.dataDir, "containerd", "logs", id+".log")
}

// Log rotation. The shim appends by fd, so renaming the live file under a
// running shim would just keep the shim writing to the renamed inode —
// rotation is therefore only performed where the shim is known not to be
// running: in startTask, right before a task (and with it a fresh shim) is
// created (deploy, Start, Restart). Residual limitation: within one
// uninterrupted run — including restart-monitor resurrections, which bypass
// cornus — the live file can grow past the cap; the cap bounds growth across
// cornus-driven (re)starts only. Exactly one old generation (<name>.log.1) is
// kept; anything older is dropped at rotation time.

// defaultLogMaxBytes is the rotation threshold when logMaxBytesEnv is not set.
const defaultLogMaxBytes = 16 << 20 // 16 MiB

// logMaxBytesEnv overrides the rotation threshold in bytes; non-numeric or
// non-positive values fall back to the default.
const logMaxBytesEnv = "CORNUS_CONTAINERD_LOG_MAX_BYTES"

// logMaxBytes resolves the rotation threshold from the environment.
func logMaxBytes() int64 {
	if v := os.Getenv(logMaxBytesEnv); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultLogMaxBytes
}

// rotatedLogPath is the single retained old generation of a log file.
func rotatedLogPath(path string) string { return path + ".1" }

// rotateLogIfNeeded renames path to path+".1" (dropping any previous ".1")
// when the live file exceeds maxBytes. A missing live file is a no-op. Callers
// must ensure no shim holds the file open — see the rotation notes above.
func rotateLogIfNeeded(path string, maxBytes int64) error {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if st.Size() <= maxBytes {
		return nil
	}
	old := rotatedLogPath(path)
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(path, old)
}

// logURI returns the binary log URI string for an instance, pointing at the
// running cornus executable's `containerd-log-shim` subcommand with the log
// path as an argument. Used both for cio task IO and the restart-monitor
// loguri label.
func (b *Backend) logURI(id string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("containerd: resolve cornus executable for log shim: %w", err)
	}
	u, err := cio.LogURIGenerator("binary", exe, map[string]string{logShimArg: b.logPath(id)})
	if err != nil {
		return "", fmt.Errorf("containerd: build log URI: %w", err)
	}
	return u.String(), nil
}

// Logs streams a deployment's container logs to w in stdcopy framing, reading
// the JSON-lines log file the containerd-log-shim writes for the deployment's
// first instance.
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	c, err := b.firstInstance(ctx, name)
	if err != nil {
		return err
	}
	return streamLogFile(ctx, b.logPath(c.ID()), opts, w)
}

// streamLogFile is the testable core of Logs: it reads the logfmt file at
// path (preceded by its rotated generation, path+".1", when one exists),
// applies opts (stream selection, since, tail, timestamps, follow), and
// writes stdcopy-multiplexed frames to w. A missing file means the container
// has not logged yet: non-follow returns nil with no output; follow keeps
// polling until ctx is done. A follow ended by ctx cancellation is a normal
// end of stream and returns nil.
func streamLogFile(ctx context.Context, path string, opts api.LogOptions, w io.Writer) error {
	stdout, stderr := opts.Streams()
	// deploy.ParseSince is the shared cross-backend since grammar (Unix
	// seconds[.nanos], RFC3339, or a duration relative to now); garbage is an
	// error, and the zero time (empty input) leaves the filter unbounded.
	since, err := deploy.ParseSince(opts.Since, time.Now())
	if err != nil {
		return fmt.Errorf("containerd: %w", err)
	}
	var until time.Time
	if opts.Until != "" {
		until, err = deploy.ParseSince(opts.Until, time.Now())
		if err != nil {
			return fmt.Errorf("containerd: %w", err)
		}
	}
	filter := logfmt.Filter{Stdout: stdout, Stderr: stderr, Since: since, Until: until}
	tail := -1
	if opts.Tail != "" && opts.Tail != "all" {
		n, err := strconv.Atoi(opts.Tail)
		if err != nil || n < 0 {
			return fmt.Errorf("containerd: invalid tail value %q", opts.Tail)
		}
		tail = n
	}

	sink := newLogSink(w, opts.Timestamps)

	// Initial drain: the rotated generation (if any) then the live file, so
	// non-follow reads and the pre-follow backlog span both. Tail applies to
	// existing content only (docker semantics). The follow loop below tracks
	// the live file alone.
	recs, _, err := readLogRecords(rotatedLogPath(path), 0)
	if err != nil {
		return err
	}
	live, offset, err := readLogRecords(path, 0)
	if err != nil {
		return err
	}
	recs = append(recs, live...)
	kept := recs[:0]
	for _, r := range recs {
		if filter.Match(r) {
			kept = append(kept, r)
		}
	}
	for _, r := range logfmt.Tail(kept, tail) {
		if err := sink.write(r); err != nil {
			return err
		}
	}
	if !opts.Follow {
		return nil
	}

	ticker := time.NewTicker(followPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		recs, offset, err = readLogRecords(path, offset)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if !filter.Match(r) {
				continue
			}
			if err := sink.write(r); err != nil {
				return err
			}
		}
	}
}

// readLogRecords reads complete JSON lines from the log file at path starting
// at offset, returning the parsed records and the byte offset just past the
// last complete line. A trailing line without a newline (a write torn by
// concurrent appends or a crash) is not consumed, so a follower re-reads it
// once completed; a complete line that fails to parse is skipped, matching
// logfmt.Reader's corruption tolerance. A missing file yields no records.
func readLogRecords(path string, offset int64) ([]logfmt.Record, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer f.Close()
	if offset > 0 {
		st, err := f.Stat()
		if err != nil {
			return nil, offset, err
		}
		if st.Size() < offset {
			// The live file shrank under a follower: it was rotated away at a
			// task restart and recreated. Restart from the top of the fresh
			// file (records appended to the old generation after our last read
			// are not replayed — an accepted rotation trade-off).
			offset = 0
		} else if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, err
		}
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var recs []logfmt.Record
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// An unterminated tail (if any) stays unconsumed.
				return recs, offset, nil
			}
			return recs, offset, err
		}
		offset += int64(len(line))
		var rec logfmt.Record
		if json.Unmarshal(line, &rec) == nil {
			recs = append(recs, rec)
		}
	}
}

// logSink routes records to per-stream stdcopy writers, optionally prefixing
// each output line with the record timestamp. Partial continuation chunks are
// not re-prefixed: midLine tracks, per stream, whether the previous record
// left its line unterminated. Record bytes are written verbatim — they already
// carry their newlines.
type logSink struct {
	stdout, stderr io.Writer
	timestamps     bool
	midLine        map[string]bool
}

func newLogSink(w io.Writer, timestamps bool) *logSink {
	return &logSink{
		stdout:     stdcopy.NewStdWriter(w, stdcopy.Stdout),
		stderr:     stdcopy.NewStdWriter(w, stdcopy.Stderr),
		timestamps: timestamps,
		midLine:    map[string]bool{},
	}
}

func (s *logSink) write(rec logfmt.Record) error {
	var w io.Writer
	switch rec.Stream {
	case "stdout":
		w = s.stdout
	case "stderr":
		w = s.stderr
	default:
		return nil
	}
	payload := []byte(rec.Log)
	if s.timestamps && !s.midLine[rec.Stream] {
		payload = append([]byte(rec.Time.UTC().Format(time.RFC3339Nano)+" "), payload...)
	}
	s.midLine[rec.Stream] = rec.Partial
	_, err := w.Write(payload)
	return err
}
