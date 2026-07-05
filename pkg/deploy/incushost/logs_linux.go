//go:build linux

package incushost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// Logs streams the deployment's instance console (an OCI app container's PID-1
// stdout/stderr) to w. Incus offers the console in two forms and this uses both:
// GetInstanceConsoleLog is a one-shot snapshot of the ring buffer (the history),
// and ConsoleInstanceDynamic attaches to the live console device (the tail as it
// grows). Both are a single raw unframed byte stream with no stdout/stderr
// split, so — matching the framing contract — the output is wrapped in stdcopy
// STDOUT framing, as the kubernetes backend wraps its unframed stream.
//
// What this backend can and cannot honor, and why:
//
//   - Tail — SUPPORTED. The snapshot is a plain byte stream, so the last N lines
//     are a local slice of it. Tail "0" replays nothing, matching the docker
//     semantics the other backends implement.
//   - Follow — SUPPORTED, via the live console attach. The attach happens BEFORE
//     the snapshot is read, deliberately: the two are separate reads of the same
//     device, so ordering them the other way would drop whatever the workload
//     wrote in between. The cost is that a line written in that window can appear
//     twice; duplicated output is a far smaller lie than missing output.
//   - Since / Until / Timestamps — NOT SUPPORTABLE, and not a deferred item. A
//     console is a serial byte stream: Incus records no per-line timestamp
//     anywhere, in either the ring buffer or the live attach, so there is nothing
//     to filter on and nothing to print. Cornus will not synthesize a receive
//     time and pass it off as the workload's own. The values are still validated
//     with deploy.ParseSince (a malformed value is an error, never silently
//     ignored) and each set option is warned about per-field.
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	id, err := b.instanceAt(name, opts.Instance)
	if err != nil {
		return err
	}
	tail, err := parseTail(opts.Tail)
	if err != nil {
		return err
	}
	now := time.Now()
	if opts.Since != "" {
		if _, err := deploy.ParseSince(opts.Since, now); err != nil {
			return err
		}
	}
	if opts.Until != "" {
		if _, err := deploy.ParseSince(opts.Until, now); err != nil {
			return err
		}
	}
	log := logging.FromContext(ctx, slog.Group("incus", "deployment", name))
	if opts.Since != "" || opts.Until != "" {
		log.WarnContext(ctx, "backend cannot filter console logs by time; --since/--until ignored (an Incus console carries no per-line timestamps)")
	}
	if opts.Timestamps {
		log.WarnContext(ctx, "backend cannot prefix console logs with timestamps; --timestamps ignored (an Incus console carries no per-line timestamps)")
	}

	out := stdcopy.NewStdWriter(w, stdcopy.Stdout)

	// Attach first, replay second — see the "Follow" note above.
	var stream func(io.ReadWriteCloser) error
	if opts.Follow {
		s, stop, err := b.conn.ConsoleAttach(id)
		if err != nil {
			return fmt.Errorf("incus: attaching to the console of %s: %w", id, err)
		}
		stream = s
		defer stop()
		// The stream ends when its reader side reports EOF, which consoleSink does
		// on ctx cancellation; detaching as well releases the instance's single
		// console slot promptly rather than at the daemon's own timeout. The done
		// channel retires the watcher when the console ends on its own (the
		// instance stopped) while the caller's context is still live, so a
		// long-lived caller does not accumulate a goroutine per finished follow.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				stop()
			case <-done:
			}
		}()
	}

	rc, err := b.conn.ConsoleLog(id)
	if err != nil {
		return err
	}
	snapshot, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return err
	}
	if _, err := out.Write(lastLines(snapshot, tail)); err != nil {
		return err
	}
	if stream == nil {
		return nil
	}
	return stream(&consoleSink{ctx: ctx, w: out})
}

// consoleSink adapts the log writer to the io.ReadWriteCloser Incus mirrors an
// attached console onto. The console is BIDIRECTIONAL — whatever the sink reads
// is written into the workload's console, i.e. PID 1's stdin — so Read must
// never produce a byte. It blocks until ctx is done and then reports EOF, which
// is also what tears the mirror down: Incus's read mirror closes the websocket's
// write side on EOF, ending the session and returning from the stream function.
type consoleSink struct {
	ctx context.Context
	w   io.Writer
}

func (s *consoleSink) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *consoleSink) Read([]byte) (int, error) {
	<-s.ctx.Done()
	return 0, io.EOF
}

func (s *consoleSink) Close() error { return nil }

// parseTail maps api.LogOptions.Tail to a line count: -1 for "everything"
// ("" / "all"), 0 for "no history at all" (docker's `--tail 0`), N for the last
// N lines. A non-numeric or negative value is an error rather than a silent
// "all".
func parseTail(tail string) (int, error) {
	if tail == "" || tail == "all" {
		return -1, nil
	}
	n, err := strconv.Atoi(tail)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("incus: invalid tail value %q", tail)
	}
	return n, nil
}

// lastLines returns the last n newline-delimited lines of data (n < 0 keeps all
// of it, n == 0 keeps none). A single trailing newline is not counted as an
// empty final line, so `--tail 1` on "a\nb\n" yields "b\n" rather than "".
func lastLines(data []byte, n int) []byte {
	if n < 0 {
		return data
	}
	if n == 0 {
		return nil
	}
	trimmed := bytes.TrimSuffix(data, []byte("\n"))
	start := 0
	seen := 0
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i] != '\n' {
			continue
		}
		seen++
		if seen == n {
			start = i + 1
			break
		}
	}
	return data[start:]
}
