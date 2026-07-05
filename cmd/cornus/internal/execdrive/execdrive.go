// Package execdrive drives an already-created server-side exec from the local
// terminal: it starts the exec stream, optionally puts the local terminal in raw
// mode and forwards window resizes, bridges local stdio, and maps the remote
// command's exit status to a process exit code. Both `cornus exec` and `cornus
// compose exec` share it so the interactive-terminal handling lives in one place.
package execdrive

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"golang.org/x/term"

	"cornus/pkg/api"
)

// Client is the subset of *client.Client an interactive drive needs: starting
// the exec's stdio stream, resizing its TTY, and inspecting its final state.
// Creating the exec is the caller's job — it knows the deployment and the exec
// config (command, env, user, ...).
type Client interface {
	ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error)
	ExecResize(ctx context.Context, execID string, height, width uint) error
	ExecInspect(ctx context.Context, execID string) (api.ExecState, error)
}

// Options controls one interactive drive. Tty puts the local terminal in raw mode
// and forwards resizes; the caller must have already downgraded it to false when
// stdin is not a terminal (a server PTY the client cannot drive in raw mode would
// garble the output). Interactive bridges local stdin into the exec. ResizeNotify,
// when non-nil and Tty is set, registers an onResize callback fired on every
// terminal size change until its returned stop func is called — package main and
// the compose CLI inject their own SIGWINCH watcher, so execdrive itself carries
// no platform-specific files.
type Options struct {
	Tty          bool
	Interactive  bool
	ResizeNotify func(onResize func()) (stop func())
	// Stdout and Stderr receive the exec's output. Nil means os.Stdout /
	// os.Stderr, which is what both production callers want; tests set them to
	// observe the demultiplexed streams separately.
	Stdout io.Writer
	Stderr io.Writer
}

func (o Options) stdout() io.Writer {
	if o.Stdout != nil {
		return o.Stdout
	}
	return os.Stdout
}

func (o Options) stderr() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

// Run starts the exec, drives local stdio until the remote command finishes, and
// returns its mapped exit code. A non-nil error means the stream could not be
// started or raw mode could not be set (the returned code is meaningless then);
// the caller surfaces it. On success the code already folds in the InspectFailCode
// fallback, so the caller can pass it straight to os.Exit. The local terminal is
// restored before Run returns (its deferred cleanup runs on the normal return, so
// a subsequent os.Exit in the caller does not skip it).
func Run(ctx context.Context, cl Client, execID string, opts Options) (int, error) {
	conn, err := cl.ExecStart(ctx, execID, api.ExecStartConfig{Tty: opts.Tty})
	if err != nil {
		return 0, fmt.Errorf("starting exec: %w", err)
	}
	defer conn.Close()

	if opts.Tty {
		fd := int(os.Stdin.Fd())
		old, err := term.MakeRaw(fd)
		if err != nil {
			return 0, fmt.Errorf("setting raw mode: %w", err)
		}
		defer func() { _ = term.Restore(fd, old) }()

		resize := func() error {
			w, h, err := term.GetSize(fd)
			if err != nil {
				return err
			}
			return cl.ExecResize(ctx, execID, uint(h), uint(w))
		}
		// Seed the remote PTY with the current window size, RETRYING briefly.
		//
		// ExecStart returns once the stream's response headers arrive, which on
		// libpod is before the runtime has created the PTY: a resize sent in that
		// window is answered 500 and the PTY keeps no size at all, so `stty size`
		// in the session fails outright rather than reporting something stale.
		// Measured on podman 4.3.1 — resize at +0ms -> 500 and no size ever;
		// at +50ms -> 201 and "24 100". Docker wins the race and returns on the
		// first attempt, so this costs it nothing.
		//
		// The retry is on the SEED only. A later SIGWINCH resize that fails is
		// self-correcting — the next window change re-sends it, and the session
		// meanwhile has a usable size — whereas the seed is the one whose failure
		// leaves the PTY permanently unsized, and its error was previously
		// discarded, so nothing anywhere reported it.
		seedRemotePTY(ctx, resize)
		if opts.ResizeNotify != nil {
			stop := opts.ResizeNotify(func() { _ = resize() })
			defer stop()
		}
	}

	if opts.Interactive {
		go func() {
			_, _ = io.Copy(conn, os.Stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}

	// Foreground copy blocks until the exec's stream closes (process exit).
	//
	// A non-TTY exec's output is stdcopy-MULTIPLEXED — every backend guarantees it
	// (see deploy.Backend.Logs/ExecStart), because without a PTY there is no other
	// way to keep stdout and stderr apart on one connection. It must therefore be
	// demultiplexed here rather than copied through: a raw copy writes the 8-byte
	// frame headers to the terminal and folds stderr into stdout. That is what this
	// code did, on every host backend, and it was invisible because the header
	// bytes are mostly NULs and the payload still reads correctly:
	//
	//	$ cornus exec app sh -c 'echo OUT; echo ERR >&2' | cat -A
	//	^A^@^@^@^@^@^@^DOUT$      <- 0x01 stdout, length 4
	//	^B^@^@^@^@^@^@^DERR$      <- 0x02 stderr, length 4
	//
	// With a TTY the daemon allocates a PTY and sends the raw byte stream instead
	// (measured on both Docker and libpod: framed when Tty is false, raw when
	// true), so demultiplexing there would corrupt it — hence the branch.
	if opts.Tty {
		_, _ = io.Copy(opts.stdout(), conn)
	} else {
		_, _ = stdcopy.StdCopy(opts.stdout(), opts.stderr(), conn)
	}

	st, inspErr := cl.ExecInspect(ctx, execID)
	if inspErr != nil {
		// A failed inspect (transport error, non-200, decode failure) returns a
		// zero-value ExecState; trusting its ExitCode==0 would report success for a
		// command whose real status we never learned. Surface it to stderr so CI does
		// not mistake the resulting non-zero exit for its own diagnostic.
		fmt.Fprintf(os.Stderr, "cornus: could not determine exec exit status: %v\n", inspErr)
	}
	return ExitCode(st, inspErr), nil
}

// InspectFailCode is the exit code used when the remote command finished but its
// exit status could not be retrieved. It matches docker's convention of 125 for
// "the command itself ran but the tooling could not complete".
const InspectFailCode = 125

// ExitCode maps an ExecInspect result to a process exit code. A non-nil error
// means the exit status is unknown, so it yields InspectFailCode rather than the
// misleading zero-value ExitCode; otherwise it returns the reported code.
func ExitCode(st api.ExecState, err error) int {
	if err != nil {
		return InspectFailCode
	}
	return st.ExitCode
}

// seedResizeAttempts and seedResizeDelay bound the seed retry. The measured race
// window is under 50ms; this allows ~10x that and then gives up, because a
// terminal that never sizes is a degraded session, not a reason to refuse to open
// one.
const (
	seedResizeAttempts = 6
	seedResizeDelay    = 80 * time.Millisecond
)

// seedRemotePTY sets the remote PTY's initial size, retrying past the runtime's
// start race. Returns once a resize succeeds, the attempts are exhausted, or the
// context ends.
func seedRemotePTY(ctx context.Context, resize func() error) {
	for i := 0; i < seedResizeAttempts; i++ {
		if err := resize(); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(seedResizeDelay):
		}
	}
}
