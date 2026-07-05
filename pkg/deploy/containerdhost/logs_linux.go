//go:build linux

package containerdhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/containerd/containerd/cio"
	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/containerdhost/logfmt"
	"cornus/pkg/logging"
)

// logShimArg is the binary-log-URI query key carrying the log file path. It is
// an alias of the exported LogShimArg in containerdhost.go, which is where the
// literal lives so that cmd/cornus can NAME its subcommand from it instead of
// spelling it again — see that declaration for why.
const logShimArg = LogShimArg

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
// created (deploy, Start, Restart). Exactly one old generation (<name>.log.1) is
// kept; anything older is dropped at rotation time.
//
// The bound this actually gives you, stated exactly:
//
//   - <name>.log.1 is at most one over-cap generation, frozen at the moment of
//     the rotation that produced it.
//   - <name>.log — the live file — is bounded only at cornus-driven (re)starts.
//     Within one uninterrupted run it is UNBOUNDED, and "one run" includes
//     containerd restart-monitor resurrections, which recreate the task (and a
//     fresh shim on the same path, appending) without cornus in the loop.
//
// So the per-instance worst case is "cap, plus however much a long-lived task
// logs", not the 2x cap the two-generation scheme suggests. A chatty workload
// that is never redeployed still fills the disk. That is a real limitation and
// naming it is the point of this comment.
//
// The options for closing it were weighed and none is a fix at this size:
//
//   - Truncate the live file in place. Mechanically this WOULD work — the shim
//     opens with O_APPEND, so its next write lands at 0 rather than leaving a
//     sparse hole, and readLogRecords already detects a shrink and restarts.
//     But it discards the entire log instead of retiring the oldest part of it,
//     which is strictly worse than the rename scheme it would replace, and it
//     needs a periodic driver that does not exist. Copy-then-truncate does not
//     rescue it: whatever the shim appends between the copy and the truncate is
//     lost, with no bound on how much.
//   - Check the size on the read path. Logs/Attach may not be called for days,
//     so it bounds nothing; and having a read delete data is a surprise nobody
//     asked for.
//   - Have the shim rotate itself. This is the correct answer — the writer is
//     the only party that can rotate atomically with respect to its own writes,
//     at a record boundary it alone knows. It is also a change to the log-shim
//     contract (the cap has to travel in the log URI, cmd/cornus's shim needs
//     byte accounting, and existing containers carry URIs without it), i.e. a
//     redesign rather than a fix. Deliberately not attempted here.
//
// Until that redesign happens the bound above is the contract, and
// TestLogRotationIsNeverPerformedOutsideTaskStart pins it so it cannot quietly
// get worse — in particular so nobody "fixes" the residual by rotating from a
// read or reconcile path, which would not shrink anything and WOULD silently
// send every subsequent line of a running workload to an unlinked inode.
//
// Separately: Delete removes an instance's <id>.log[.1] once its container is
// gone (removeInstanceLogs below), so deleted deployments no longer accumulate
// under <dataDir>/containerd/logs. That reaping is retention hygiene, not part
// of this cap — it bounds nothing for a live instance.

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

// removeInstanceLogs deletes both generations of an instance's log file
// (<id>.log and <id>.log.1). Delete is the only caller: an instance's logs are
// reachable only through its container, so once the container is gone the files
// are unreachable garbage that would otherwise accumulate under
// <dataDir>/containerd/logs forever.
//
// Best-effort by construction — it returns nothing, so a delete can never fail
// on a log file. A half-removed deployment (container gone, delete reported as
// failed) is strictly worse than a leaked file. An already-absent file is not an
// error and is not even logged; anything else is warned about and dropped.
//
// Ordering and the shim: callers must remove the CONTAINER first. Delete stops
// the task (which reaps the shim, and with it the only writer holding the fd)
// before deleting the container, so nothing is writing by the time this runs. It
// is safe even if something were: unlink of an open file leaves the writer
// appending to an unlinked inode that is freed when it exits, and the follow
// path (readLogRecords) opens per poll and treats ENOENT as "no records", so a
// live `logs -f` sees an idle stream rather than an error.
//
// Rotation cannot race this either: rotateLogIfNeeded runs only from startTask,
// stats the live file first, and treats a missing file as a no-op — so a
// concurrent removal turns rotation into a no-op instead of an error, and a
// rotation that just renamed the live file leaves a .1 this call also removes.
func (b *Backend) removeInstanceLogs(ctx context.Context, id string) {
	path := b.logPath(id)
	for _, p := range []string{path, rotatedLogPath(path)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			logging.FromContext(ctx, slog.Group("containerd", "instance", id)).
				WarnContext(ctx, "log file removal failed", "path", p, "error", err)
		}
	}
}

// logURI returns the binary log URI string for an instance, pointing at the
// running cornus executable's `containerd-log-shim` subcommand with the log
// path as an argument. Used both for cio task IO and the restart-monitor
// loguri label.
// Both halves of the URI are HOST paths: a containerd shim execs the binary and
// opens the log file, and it does so in the host's mount namespace. On a
// non-containerized server that is exactly what logPath and the running
// executable already are, so this is the identity; containerize the server and
// both must be translated or every task fails its IO setup against a binary
// that does not exist there. See hostpaths_linux.go.
func (b *Backend) logURI(id string) (string, error) {
	exe, err := b.logShimBinary()
	if err != nil {
		return "", err
	}
	logPath, err := b.hostPath("log file", b.logPath(id))
	if err != nil {
		return "", err
	}
	u, err := cio.LogURIGenerator("binary", exe, map[string]string{logShimArg: logPath})
	if err != nil {
		return "", fmt.Errorf("containerd: build log URI: %w", err)
	}
	return u.String(), nil
}

// Logs streams a deployment's container logs to w in stdcopy framing, reading
// the JSON-lines log file the containerd-log-shim writes for the deployment's
// first instance.
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	c, err := b.instanceAt(ctx, name, opts.Instance)
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
