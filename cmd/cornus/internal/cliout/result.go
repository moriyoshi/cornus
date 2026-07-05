package cliout

import (
	"fmt"
	"strings"
)

// Result is a structured command result. Commands whose output scripts should
// parse implement it: the value carries JSON tags for ModeJSON, and Human
// renders the plain/fancy line(s). It is the seam that keeps json mode
// meaningful without every call site formatting strings twice.
type Result interface {
	// Human renders the human-readable form to p. ModeJSON ignores this and
	// marshals the value directly.
	Human(p Printer)
}

// Printer writes human-readable result lines to the results stream. Result.Human
// receives one; a line is emitted per Line call.
type Printer interface {
	Line(format string, a ...any)
}

// Emit renders a structured result: NDJSON to stdout in json mode, otherwise the
// value's Human form to stdout. Results go to stdout so they stay pipe-clean.
// While a live progress region owns the terminal, the rendered line is printed
// above it (fancy always resolves with both streams on the same TTY, so this
// keeps channel semantics intact for the piped case, which is never live).
func (d *Driver) Emit(r Result) error {
	if d.routeAbove(func(b *strings.Builder) { d.r.emit(b, r) }) {
		return nil
	}
	return d.r.emit(d.out, r)
}

// Event renders a structured progress event to stderr: NDJSON in json mode,
// otherwise the value's Human form. Use it for progress that carries data (e.g.
// a compose service coming up) — Emit is for a command's final result on stdout;
// Event keeps progress on stderr while staying machine-readable, and avoids
// baking layout into a free-form notice string.
func (d *Driver) Event(r Result) error {
	if d.routeAbove(func(b *strings.Builder) { d.r.emit(b, r) }) {
		return nil
	}
	// errMu-guarded like notice(): compose reconciling several services
	// concurrently emits events from multiple goroutines, and outside
	// fancy+TTY mode (where routeAbove above handles serialization) those
	// writes would otherwise race directly on d.err.
	d.errMu.Lock()
	defer d.errMu.Unlock()
	return d.r.emit(d.err, r)
}

// Item prints a single value to stdout. In plain and fancy modes the value is
// bare (so `cornus version` / `cornus token` pipe cleanly); in json mode it
// becomes {"value":"..."}.
func (d *Driver) Item(format string, a ...any) {
	d.r.item(d.out, fmt.Sprintf(format, a...))
}
