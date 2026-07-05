package composecli

import (
	"fmt"

	"cornus/cmd/cornus/internal/cliout"
)

// applyProgressFlag applies a compose --progress flag value to the driver,
// overriding the CORNUS_PROGRESS-resolved style. An empty value leaves the
// env-resolved style in place; an unrecognized one is a usage error.
func applyProgressFlag(d *cliout.Driver, flag string) error {
	if flag == "" {
		return nil
	}
	style, ok := cliout.ParseProgressStyle(flag)
	if !ok {
		return fmt.Errorf("invalid --progress %q (want status or stream)", flag)
	}
	d.SetProgressStyle(style)
	return nil
}

// serviceStatus is one compose service's live status surface during `up`. When
// the driver renders a collapsed live region (fancy TTY with --progress=status,
// the default), it owns a single in-place progress Task keyed by the service name
// and mutates its label as the service progresses (starting -> pulling ->
// running), so the terminal shows one evolving line per service the way
// `docker compose up` does: the transient deploy diagnostics and per-instance
// state transitions all collapse onto that one line instead of scrolling.
//
// In every other case — plain, json, non-TTY, or --progress=stream — prog.Live()
// is false, the Task is an inert no-op, and each update falls back to the
// append-only serviceEvent / Warn / Error line the CLI emitted before. So piped,
// machine-readable, and explicit-scrollback output is byte-for-byte the streamed
// behavior; only fancy+TTY+status collapses.
//
// The Task's lifetime spans the whole bring-up of its service (created up front,
// finished once via done/fail by the owning goroutine), so a mounted service's
// attach-phase diagnostics and its later cluster-reconcile transitions share one
// continuous line.
type serviceStatus struct {
	d       *cliout.Driver
	prog    *cliout.Progress
	task    *cliout.Task
	service string
}

// newServiceStatus creates the service's status line as a "starting" task on the
// shared Progress. In non-live modes prog.Task is a no-op, so this emits nothing.
func newServiceStatus(d *cliout.Driver, prog *cliout.Progress, service string) *serviceStatus {
	return &serviceStatus{d: d, prog: prog, task: prog.Task(service + ": starting"), service: service}
}

// transition reports a per-instance state change (pending, running, ...). Live:
// the service's line becomes "<service>: <state>". Otherwise: the append-only
// transition event, unchanged from the pre-collapse behavior.
func (s *serviceStatus) transition(instance, state string) {
	if s.prog.Live() {
		s.task.Update(s.service + ": " + state)
		return
	}
	s.d.Event(serviceEvent{Service: s.service, Event: "transition", Instance: instance, State: state})
}

// diagnostic reports a deploy diagnostic streamed from the attach session: a
// transient one (a workload still starting — image pull, brief CrashLoopBackOff)
// or a terminal failure. Live: it folds onto the service's line. Otherwise: a
// Warn (transient) or Error (terminal), matching the prior severity routing.
func (s *serviceStatus) diagnostic(msg string, terminal bool) {
	if s.prog.Live() {
		s.task.Update(s.service + ": " + msg)
		return
	}
	if terminal {
		s.d.Error("%s: %s", s.service, msg)
	} else {
		s.d.Warn("%s: %s", s.service, msg)
	}
}

// waiting reports that the service is blocked on a dependency. Live: its line
// shows "<service>: waiting <detail>". Otherwise: the append-only "waiting" event.
func (s *serviceStatus) waiting(detail string) {
	if s.prog.Live() {
		s.task.Update(s.service + ": waiting " + detail)
		return
	}
	s.d.Event(serviceEvent{Service: s.service, Event: "waiting", Detail: detail})
}

// done finishes the live line silently on success; the owning goroutine calls it
// after the service is up and its hooks have run. The caller separately emits the
// authoritative svcUp / svcEvent summary as a permanent line, so this only
// removes the spinner. A no-op in non-live modes.
func (s *serviceStatus) done() { s.task.Done("") }

// fail finishes the live line as failed (a ✗). The owning goroutine calls it when
// its work returned an error or was cancelled; the failure itself is surfaced by
// the returned error or an append-only notice. A no-op in non-live modes.
func (s *serviceStatus) fail() { s.task.Fail("") }

// transitionSink returns the per-instance transition callback pollTransitions
// invokes. With a shared serviceStatus (the concurrent up loop) it routes onto
// that one line; without one (tests, and the `down` teardown watch) it emits the
// standalone append-only transition event exactly as before.
func transitionSink(d *cliout.Driver, status *serviceStatus, service string) func(instance, state string) {
	if status != nil {
		return status.transition
	}
	return func(instance, state string) {
		d.Event(serviceEvent{Service: service, Event: "transition", Instance: instance, State: state})
	}
}
