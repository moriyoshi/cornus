package server

import (
	"context"
	"fmt"
	"log/slog"

	"cornus/pkg/activity"
	"cornus/pkg/caretaker"
	"cornus/pkg/config"
	"cornus/pkg/deploywire"
	"cornus/pkg/logging"
)

// ActivityDir is where a server keeps its flight recorder, under the data dir so
// it is as persistent as the deployment makes that — a StatefulSet volume, a
// containerized server's host bind, a host install's storage dir. That is what
// lets the records outlive the process AND the container, so the NEXT server can
// serve its predecessor's flight.
func ActivityDir(cfg config.Config) string { return cfg.DataDir + "/activity" }

// openActivity opens this server's stream. A failure is not fatal: a flight
// recorder that could refuse to let the aircraft take off would be worse than no
// recorder, so it degrades to a nil (no-op) recorder and says so once.
func openActivity(ctx context.Context, cfg config.Config) *activity.Recorder {
	rec, err := activity.Open(ActivityDir(cfg), "server")
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "activity log unavailable; this run will not be recorded", "error", err)
		return nil
	}
	return rec
}

// recoverActivities is the startup pass over what the previous incarnations left
// unfinished.
//
// Two questions are answered here, and they are different:
//
//   - did the last run end cleanly? An unfinished lifetime means it did not —
//     SIGKILL, OOM, `docker rm -f`, a panic, a host reboot — which is the first
//     thing worth knowing after an incident.
//   - is anything still lying around? An unfinished 9P mount is a mountpoint that
//     may still exist with no process owning it. Those are undone here, which is
//     the one case where the record drives an action rather than just a report.
//
// Every entry it deals with is Resolved rather than deleted or left open: the
// incident stays legible (status "recovered", attributed to this run), while the
// unfinished set converges instead of accumulating every historical crash.
func (s *Server) recoverActivities(ctx context.Context) {
	log := logging.FromContext(ctx, slog.String("component", "activity"))
	dir := ActivityDir(s.cfg)

	events, err := activity.Read(dir)
	if err != nil {
		log.WarnContext(ctx, "could not read the activity log", "error", err)
		return
	}
	open := activity.UnfinishedFrom(events)
	if len(open) == 0 {
		return
	}

	for _, e := range open {
		// Skip this run's own records: Run begins the server lifetime before this
		// pass, and closing it here would declare the live process crashed.
		if e.Instance == s.activity.Instance() {
			continue
		}
		switch e.Kind {
		case activity.KindServer, activity.KindCaretaker:
			log.WarnContext(ctx, "previous run did not shut down cleanly",
				"proc", e.Proc, "instance", e.Instance, "started", e.TS, "pid", e.PID)
			s.activity.Resolve(e, activity.StatusRecovered, "did not shut down cleanly")
		case activity.KindMount9P:
			s.recoverMount(ctx, log, events, e)
		default:
			// A kind this build has no recovery for — including one written by a
			// newer binary. Report it and leave the record alone rather than
			// closing an activity nothing has actually dealt with.
			log.WarnContext(ctx, "unfinished activity with no recovery for its kind",
				"kind", string(e.Kind), "target", e.Target, "instance", e.Instance)
		}
	}
}

// recoverMount undoes one stranded 9P mount.
//
// The clean/unclean distinction is not cosmetic. A mount left by a process that
// was killed is expected collateral. The SAME mount left by a process that shut
// down cleanly means the unwind path itself is broken — a bug that would
// otherwise be silently tidied away on every restart, which is exactly how it
// would stay unnoticed.
func (s *Server) recoverMount(ctx context.Context, log *slog.Logger, events []activity.Event, e activity.Event) {
	known, clean := activity.CleanExit(events, e.Instance)
	switch {
	case known && clean:
		log.WarnContext(ctx, "mount left behind by a process that exited CLEANLY; its unwind path is broken",
			"target", e.Target, "instance", e.Instance, "deployment", e.Attrs["deployment"])
	default:
		log.InfoContext(ctx, "recovering a mount stranded by an unclean exit",
			"target", e.Target, "instance", e.Instance, "deployment", e.Attrs["deployment"])
	}

	// Tolerate an absent target. A crash between the write-ahead record and the
	// mount syscall leaves a record for an effect that never existed, and there
	// is no way to tell that from a completed one — so "already gone" is success.
	if !caretaker.IsMountpoint(e.Target) {
		s.activity.Resolve(e, activity.StatusRecovered, "mountpoint was already gone")
		return
	}
	deploywire.Unmount9P(e.Target)
	if caretaker.IsMountpoint(e.Target) {
		// Keep the record open: the effect is still there, so the next run must
		// try again. Losing it would be worse than reporting it every startup.
		log.WarnContext(ctx, "could not unmount a stranded mountpoint; leaving it recorded for the next run",
			"target", e.Target)
		return
	}
	s.activity.Resolve(e, activity.StatusRecovered, fmt.Sprintf("unmounted %s at startup", e.Target))
	log.InfoContext(ctx, "unmounted a stranded mountpoint", "target", e.Target)
}
