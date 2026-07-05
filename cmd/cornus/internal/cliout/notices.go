package cliout

import (
	"fmt"
	"strings"
)

// The notice methods narrate progress and report warnings/errors. They all go to
// stderr so stdout stays reserved for command results. In json mode each becomes
// a {"level":...,"msg":...} NDJSON object on stderr.

// Step reports an action in progress ("building web", "deploying api").
func (d *Driver) Step(format string, a ...any) { d.notice(noticeStep, format, a...) }

// Done reports a completed action ("built web", "pushed X").
func (d *Driver) Done(format string, a ...any) { d.notice(noticeDone, format, a...) }

// Success reports an explicit success.
func (d *Driver) Success(format string, a ...any) { d.notice(noticeSuccess, format, a...) }

// Info reports neutral status.
func (d *Driver) Info(format string, a ...any) { d.notice(noticeInfo, format, a...) }

// Warn reports a non-fatal warning.
func (d *Driver) Warn(format string, a ...any) { d.notice(noticeWarn, format, a...) }

// Error reports a non-fatal error notice. (Fatal errors are still returned from
// Run methods and printed by kong.)
func (d *Driver) Error(format string, a ...any) { d.notice(noticeError, format, a...) }

func (d *Driver) notice(kind noticeKind, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	// While a live progress region owns the terminal, print the notice above it
	// so the spinners aren't torn; otherwise write straight to stderr. The direct
	// path is errMu-guarded so concurrent notices (e.g. compose reconciling
	// several services at once outside fancy+TTY mode) never interleave a
	// partial line — the same discipline LineWriter already applies to streamed
	// log lines.
	if d.routeAbove(func(b *strings.Builder) { d.r.notice(b, kind, msg) }) {
		return
	}
	d.errMu.Lock()
	defer d.errMu.Unlock()
	d.r.notice(d.err, kind, msg)
}
