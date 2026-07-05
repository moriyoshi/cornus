// Package cliout is the cornus CLI's unified output driver. Every human-facing
// line the CLI prints flows through one *Driver, which decides how to render it
// for the resolved mode: plain (deterministic, ANSI-free, for pipes and CI),
// fancy (color and aligned layout for an interactive terminal), or json
// (machine-readable NDJSON). Call sites describe *what* to say — a status, a
// table, a result, a stream of logs — and the driver decides *how*.
//
// It lives in an internal package (not package main) so both the top-level
// commands and the `cornus compose` subpackage can share one driver, bound via
// kong — the same pattern as cmd/cornus/internal/clientconn. It must not import
// the server, pkg/logging, or package main. The fancy mode is built on the Charm
// stack — lipgloss for width-correct styling/layout and bubbletea+bubbles for the
// live progress region (see progress.go); plain and json modes stay dependency-
// light (stdlib + golang.org/x/term) and are what pipes, CI, and scripts see.
//
// Channel discipline: results the user might pipe (tables, items, structured
// Emit values, streamed logs that are the point of a command) go to stdout;
// progress narration and notices go to stderr, so stdout stays clean for
// downstream consumers. slog remains a separate sink for structured diagnostics.
package cliout

import (
	"io"
	"os"
	"runtime"
	"sync"

	"golang.org/x/term"
)

// Mode is the resolved rendering mode.
type Mode int

const (
	// ModePlain emits deterministic, ANSI-free text. The default off a terminal.
	ModePlain Mode = iota
	// ModeFancy adds color and richer layout for an interactive terminal.
	ModeFancy
	// ModeJSON emits machine-readable NDJSON (one JSON object per line).
	ModeJSON
)

// Options configures New. Stdout/Stderr/Stdin default to the process streams.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// Output is the raw --output flag value: "", "auto", "plain", "fancy", or
	// "json". Empty is treated as "auto".
	Output string
	// NoColor is the --no-color flag; it forces color off but keeps fancy layout.
	NoColor bool

	// Env looks up environment variables; nil means os.Getenv. Injectable for tests.
	Env func(string) string
	// GOOS overrides runtime.GOOS for mode resolution; "" means runtime.GOOS.
	GOOS string
}

// Driver is the single sink every CLI-facing human output flows through.
type Driver struct {
	out, err io.Writer
	in       io.Reader
	r        renderer
	mode     Mode
	color    bool
	inTTY    bool
	outTTY   bool
	errTTY   bool

	// outMu and errMu serialize concurrent LineWriters that share a channel, so
	// interleaved streams (e.g. compose's per-service logs) never split a line.
	outMu sync.Mutex
	errMu sync.Mutex

	// progMu guards activeProg, the live progress program that currently owns the
	// terminal (fancy+TTY only). While set, notices and streamed log lines route
	// through it (printed above the live region) instead of writing to err/out
	// directly, so a spinner is never torn by interleaved output. See progress.go.
	progMu     sync.Mutex
	activeProg progressProgram

	// Prompt asks the user a yes/no question and reports the answer. It is a
	// field so tests can inject an answer without a PTY, mirroring the former
	// confirmSetDefaultContext package var. New installs a terminal-reading
	// default; Confirm only calls it when stdin is a terminal.
	Prompt func(question string, defaultYes bool) bool
}

// New builds a Driver, resolving the mode from the flag, environment, and TTY
// state once so every later call renders deterministically.
func New(o Options) *Driver {
	out := orWriter(o.Stdout, os.Stdout)
	errw := orWriter(o.Stderr, os.Stderr)
	in := o.Stdin
	if in == nil {
		in = os.Stdin
	}
	env := o.Env
	if env == nil {
		env = os.Getenv
	}
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	outTTY := isTerminal(out)
	errTTY := isTerminal(errw)
	inTTY := isTerminalReader(in)

	mode, color := resolveMode(o.Output, o.NoColor, env, outTTY, errTTY, goos)

	d := &Driver{
		out:    out,
		err:    errw,
		in:     in,
		mode:   mode,
		color:  color,
		inTTY:  inTTY,
		outTTY: outTTY,
		errTTY: errTTY,
		r:      rendererFor(mode, color),
	}
	d.Prompt = d.terminalPrompt
	return d
}

// resolveMode is the pure precedence rule: flag > env > TTY autodetect. It is
// separated out so every combination is table-testable without touching the
// process environment or a real terminal.
func resolveMode(flag string, noColorFlag bool, env func(string) string, stdoutTTY, stderrTTY bool, goos string) (Mode, bool) {
	// 1. Explicit mode from flag, else CORNUS_OUTPUT, else "auto".
	sel := flag
	if sel == "" {
		sel = env("CORNUS_OUTPUT")
	}
	if sel == "" {
		sel = "auto"
	}

	// Color preference (only meaningful for fancy). NO_COLOR (any non-empty
	// value, per no-color.org) and CLICOLOR=0 disable; CLICOLOR_FORCE forces on.
	colorForced := env("CLICOLOR_FORCE") != "" && env("CLICOLOR_FORCE") != "0"
	colorDisabled := noColorFlag || env("NO_COLOR") != "" || env("CLICOLOR") == "0"

	var mode Mode
	switch sel {
	case "json":
		return ModeJSON, false
	case "plain":
		mode = ModePlain
	case "fancy":
		mode = ModeFancy
	default: // "auto"
		// Fancy only when both result and notice streams are terminals, so a
		// piped stdout (cornus ps | cat) never receives ANSI. On Windows, stay
		// plain unless color is explicitly forced, to avoid garbling consoles
		// without VT support.
		if stdoutTTY && stderrTTY && (goos != "windows" || colorForced) {
			mode = ModeFancy
		} else {
			mode = ModePlain
		}
	}

	color := mode == ModeFancy && !colorDisabled
	if colorForced {
		color = mode == ModeFancy
	}
	return mode, color
}

// Mode reports the resolved rendering mode.
func (d *Driver) Mode() Mode { return d.mode }

// IsTTY reports whether the results stream (stdout) is a terminal.
func (d *Driver) IsTTY() bool { return d.outTTY }

// InTTY reports whether the input stream (stdin) is a terminal. The setup wizard
// gates its rich key-reading dialogs on this so it never puts a pipe into raw
// mode (unlike the output-only progress region, which only needs errTTY).
func (d *Driver) InTTY() bool { return d.inTTY }

// ErrTTY reports whether the diagnostics stream (stderr) is a terminal.
func (d *Driver) ErrTTY() bool { return d.errTTY }

// Stdin returns the raw input reader. The setup wizard's per-question bubbletea
// programs and its plain line prompts read from it.
func (d *Driver) Stdin() io.Reader { return d.in }

// Color reports whether color is enabled (fancy mode with color not disabled).
func (d *Driver) Color() bool { return d.color }

// Out returns the raw results writer (stdout). Bytes pass through unstyled; use
// it for streaming output that is itself the command's result.
func (d *Driver) Out() io.Writer { return d.out }

// Err returns the raw diagnostics writer (stderr). Use it for already-rendered
// bytes (e.g. a build progress display) that must not be re-wrapped.
func (d *Driver) Err() io.Writer { return d.err }

func orWriter(w, def io.Writer) io.Writer {
	if w == nil {
		return def
	}
	return w
}

// isTerminal reports whether w is a real terminal (an *os.File whose fd is a
// tty). A bytes.Buffer or pipe is not, which is exactly what keeps tests plain.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func isTerminalReader(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
