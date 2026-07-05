//go:build linux

package incushost

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// Logs streams the deployment's first instance's console log (an OCI app
// container's PID-1 stdout/stderr) to w. Incus's console log is a single raw
// unframed byte stream with no per-line timestamps and no stdout/stderr split,
// so — matching the framing contract — it is wrapped in stdcopy stdout framing
// (as the kubernetes backend wraps its unframed stream).
//
// opts.Since/Until are validated with deploy.ParseSince (a malformed value is an
// error, never silently ignored) but cannot be honored: the console log carries
// no timestamps to filter on. Follow/Tail/Timestamps are likewise unsupported on
// the console log; each set option is warned about per-field rather than
// silently dropped.
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	id, err := b.firstInstance(name)
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
		log.WarnContext(ctx, "backend cannot filter console logs by time; --since/--until ignored")
	}
	if opts.Follow {
		log.WarnContext(ctx, "backend does not follow console logs; returning the current snapshot")
	}
	if opts.Tail != "" && opts.Tail != "all" {
		log.WarnContext(ctx, "backend does not tail console logs; returning the full snapshot", "tail", opts.Tail)
	}

	rc, err := b.conn.ConsoleLog(id)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(stdcopy.NewStdWriter(w, stdcopy.Stdout), rc)
	return err
}
