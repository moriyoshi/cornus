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

		resize := func() {
			if w, h, err := term.GetSize(fd); err == nil {
				_ = cl.ExecResize(ctx, execID, uint(h), uint(w))
			}
		}
		resize() // seed the remote PTY with the current window size
		if opts.ResizeNotify != nil {
			stop := opts.ResizeNotify(resize)
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
	_, _ = io.Copy(os.Stdout, conn)

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
