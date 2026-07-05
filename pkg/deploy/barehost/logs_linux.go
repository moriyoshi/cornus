//go:build linux

package barehost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// logFollowInterval is how often a following read polls the log file for newly
// appended bytes.
const logFollowInterval = 200 * time.Millisecond

// Logs streams a deployment's first instance's logs to w. Per the Backend
// contract the output is stdcopy-multiplexed; M1's file-backed stdio does not
// separate stdout from stderr (both are the raw log file), so every byte is
// framed as a single stdout stream — contract-compliant (clients demux
// unconditionally) and the same shape the kubernetes backend uses for its
// unframed source. opts.Since is parsed for a legible error, but M1's raw log
// carries no per-line timestamps to filter on (timestamps + stream separation
// arrive with the logfmt-writing shim in M4).
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	if opts.Since != "" {
		if _, err := deploy.ParseSince(opts.Since, time.Now()); err != nil {
			return fmt.Errorf("bare: %w", err)
		}
	}
	// First instance only (a documented limitation shared with the other backends).
	return streamRawLog(ctx, recs[0].LogPath, opts, w)
}

// streamRawLog writes the log file at path to w as a stdcopy stdout stream,
// honoring Tail (last N lines) and Follow (poll for appended bytes until ctx is
// done). A missing file means "no logs yet": nothing is written and it returns
// nil (or, when following, waits for the file to appear).
func streamRawLog(ctx context.Context, path string, opts api.LogOptions, w io.Writer) error {
	out := stdcopy.NewStdWriter(w, stdcopy.Stdout)

	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("bare: open log: %w", err)
		}
		if !opts.Follow {
			return nil
		}
		// Following a not-yet-created log: wait for it to appear.
		f, err = waitForLog(ctx, path)
		if err != nil {
			return err
		}
	}
	defer f.Close()

	if opts.Tail != "" && opts.Tail != "all" {
		n, err := strconv.Atoi(opts.Tail)
		if err != nil {
			return fmt.Errorf("bare: invalid tail value %q", opts.Tail)
		}
		if err := seekToLastLines(f, n); err != nil {
			return err
		}
	}

	if _, err := io.Copy(out, f); err != nil {
		return err
	}
	if !opts.Follow {
		return nil
	}
	// Follow: poll for appended bytes. The file offset is already at EOF after
	// the copy above, so each tick resumes from where the last read stopped.
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(logFollowInterval):
		}
		if _, err := io.Copy(out, f); err != nil {
			return err
		}
	}
}

// waitForLog blocks until path exists (or ctx is done), then opens it.
func waitForLog(ctx context.Context, path string) (*os.File, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(logFollowInterval):
		}
		if f, err := os.Open(path); err == nil {
			return f, nil
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("bare: open log: %w", err)
		}
	}
}

// seekToLastLines positions f at the start of the last n newline-delimited lines
// (n <= 0 keeps the whole file). It reads the file to find the boundary; M1 logs
// are small, so a full read is acceptable (the logfmt reader in M4 tails by
// record).
func seekToLastLines(f *os.File, n int) error {
	if n <= 0 {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("bare: read log for tail: %w", err)
	}
	// Trim a single trailing newline so the last line is counted once.
	trimmed := bytes.TrimSuffix(data, []byte("\n"))
	start := 0
	if idx := lastNthNewline(trimmed, n); idx >= 0 {
		start = idx + 1
	}
	if _, err := f.Seek(int64(start), io.SeekStart); err != nil {
		return fmt.Errorf("bare: seek log: %w", err)
	}
	return nil
}

// lastNthNewline returns the byte offset of the newline that begins the last n
// lines of data, or -1 if there are fewer than n lines (keep everything).
func lastNthNewline(data []byte, n int) int {
	count := 0
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			count++
			if count == n {
				return i
			}
		}
	}
	return -1
}
