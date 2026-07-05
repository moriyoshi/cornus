// Package filewatch provides a small hybrid watcher over a fixed set of files.
// It is used by `cornus compose up --watch` to detect edits to the loaded
// compose files and env files and trigger a reload + re-reconcile.
//
// The strategy is event-driven-then-poll: at idle the watcher blocks on
// fsnotify events (no busy polling), and the FIRST event since the last reload
// opens a short coalescing window during which it switches to stat-polling to
// gather the full set of changes before firing a single reload.
//
// Why the hybrid rather than fsnotify alone: editors commonly save via an atomic
// write-to-temp-then-rename, which replaces the target's inode. An inode-bound
// fsnotify watch on the file itself goes silent after such a rename, so this
// watcher watches the parent DIRECTORIES and only needs the first event to wake
// up — the subsequent stat-poll reads whatever inode currently holds each
// watched name, which is immune to the rename. Why not poll alone: at idle a
// long-lived watcher (the background agent) should not stat on a timer forever;
// fsnotify keeps it quiet until something actually happens. If fsnotify is
// unavailable, the watcher degrades to a pure poll loop so `--watch` still works.
package filewatch

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Timing knobs. Window is the quiescence gap that closes a coalescing burst;
// Poll is the stat cadence within the window; maxWindow caps a burst so a stream
// of edits still fires. idlePoll is only used in the pure-poll fallback (no
// fsnotify), where there is no event to wake on.
const (
	DefaultWindow = 300 * time.Millisecond
	DefaultPoll   = 100 * time.Millisecond
	maxWindow     = 3 * time.Second
	idlePoll      = 1 * time.Second
)

// snapshot fingerprints a watched file. A file that does not exist yet is still
// watched (exists=false); its later creation is a change — matching how the
// compose loader records optional files (an absent sibling .env or a
// non-required env_file) so creating one triggers a reload.
type snapshot struct {
	exists bool
	mod    time.Time
	size   int64
}

func (a snapshot) equal(b snapshot) bool {
	return a.exists == b.exists && a.size == b.size && a.mod.Equal(b.mod)
}

func statSnapshot(path string) snapshot {
	fi, err := os.Stat(path)
	if err != nil {
		return snapshot{}
	}
	return snapshot{exists: true, mod: fi.ModTime(), size: fi.Size()}
}

// Watcher watches a fixed set of files. The path set is fixed for the life of
// the Watcher; after a reload changes it, Close this one and build a fresh
// Watcher from the new set.
type Watcher struct {
	paths   []string
	pathSet map[string]struct{}
	window  time.Duration
	poll    time.Duration
	last    map[string]snapshot

	fsw *fsnotify.Watcher // nil => pure-poll fallback
}

// New builds a Watcher over paths (made absolute + deduplicated), capturing the
// current file state as the baseline so the first change reported is one that
// happens after New returns. It watches the parent directories with fsnotify;
// if fsnotify is unavailable it silently degrades to a poll loop. A non-positive
// window or poll falls back to the package defaults.
func New(paths []string, window, poll time.Duration) *Watcher {
	if window <= 0 {
		window = DefaultWindow
	}
	if poll <= 0 {
		poll = DefaultPoll
	}
	abs := Normalize(paths)
	w := &Watcher{
		paths:   abs,
		pathSet: make(map[string]struct{}, len(abs)),
		window:  window,
		poll:    poll,
		last:    make(map[string]snapshot, len(abs)),
	}
	dirs := map[string]struct{}{}
	for _, p := range abs {
		w.pathSet[p] = struct{}{}
		w.last[p] = statSnapshot(p)
		dirs[filepath.Dir(p)] = struct{}{}
	}
	if fsw, err := fsnotify.NewWatcher(); err == nil {
		added := false
		for dir := range dirs {
			if err := fsw.Add(dir); err == nil {
				added = true
			}
		}
		if added {
			w.fsw = fsw
		} else {
			_ = fsw.Close() // could not watch any dir; fall back to polling
		}
	}
	return w
}

// EventDriven reports whether the watcher is backed by fsnotify (true) or has
// degraded to the pure-poll fallback (false). Exposed for logging/tests.
func (w *Watcher) EventDriven() bool { return w.fsw != nil }

// Close releases the underlying fsnotify watcher. Safe to call more than once.
func (w *Watcher) Close() {
	if w.fsw != nil {
		_ = w.fsw.Close()
		w.fsw = nil
	}
}

// Wait blocks until a debounced change to any watched file, or until ctx is
// done. It returns true on a real change and false on ctx cancellation. A wake
// that turns out to be spurious (e.g. a chmod with no content change) does not
// fire — Wait keeps waiting. The baseline advances as it goes, so a subsequent
// Wait reports only later changes.
func (w *Watcher) Wait(ctx context.Context) bool {
	for {
		if !w.waitFirstEvent(ctx) {
			return false
		}
		if w.coalesce(ctx) {
			w.drain()
			return true
		}
		// Spurious wake: nothing actually changed within the window. Loop back to
		// idle rather than firing a no-op reload. On ctx cancellation coalesce
		// returns false too; re-check ctx at the top via waitFirstEvent.
		if ctx.Err() != nil {
			return false
		}
	}
}

// waitFirstEvent blocks until the first relevant fsnotify event (or, in the
// poll fallback, the first detected stat change), or ctx cancellation.
func (w *Watcher) waitFirstEvent(ctx context.Context) bool {
	if w.fsw == nil {
		ticker := time.NewTicker(idlePoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return false
			case <-ticker.C:
				if w.scan() {
					return true
				}
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return false
			}
			if w.relevant(ev) {
				return true
			}
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return false
			}
			// Ignore watch errors and keep waiting; the caller's next reload will
			// rebuild the watcher over the fresh file set anyway.
		}
	}
}

// coalesce polls (and, when event-driven, keeps draining events) until the
// watched set has been quiet for the window or the max burst cap elapses,
// reporting whether any real change was observed. It runs after a wake, so it
// begins with an immediate scan to capture the change that woke it.
func (w *Watcher) coalesce(ctx context.Context) bool {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	start := time.Now()
	changed := w.scan()
	lastActivity := start
	var events <-chan fsnotify.Event
	var errs <-chan error
	if w.fsw != nil {
		events = w.fsw.Events
		errs = w.fsw.Errors
	}
	for {
		select {
		case <-ctx.Done():
			return changed
		case ev, ok := <-events:
			if ok && w.relevant(ev) {
				lastActivity = time.Now()
			}
		case _, ok := <-errs:
			_ = ok
		case now := <-ticker.C:
			if w.scan() {
				changed = true
				lastActivity = now
			}
			quiet := now.Sub(lastActivity) >= w.window
			capped := now.Sub(start) >= maxWindow
			if changed && (quiet || capped) {
				return true
			}
			if !changed && (quiet || capped) {
				return false // spurious wake; nothing really changed
			}
		}
	}
}

// drain non-blockingly discards any queued fsnotify events, so events buffered
// during a burst we already folded into the baseline do not immediately re-wake
// the next idle Wait.
func (w *Watcher) drain() {
	if w.fsw == nil {
		return
	}
	for {
		select {
		case <-w.fsw.Events:
		case <-w.fsw.Errors:
		default:
			return
		}
	}
}

// relevant reports whether an fsnotify event concerns one of the watched files.
// Pure chmod events are ignored (they carry no content change and would only
// cost a spurious coalescing window).
func (w *Watcher) relevant(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	_, ok := w.pathSet[filepath.Clean(ev.Name)]
	return ok
}

// scan re-stats every watched path, advancing the baseline, and reports whether
// any path changed since the last scan.
func (w *Watcher) scan() bool {
	changed := false
	for _, p := range w.paths {
		s := statSnapshot(p)
		if !s.equal(w.last[p]) {
			w.last[p] = s
			changed = true
		}
	}
	return changed
}

// Normalize turns a raw list of loader-reported paths into a stable watch set:
// each is made absolute (against the process working directory) and duplicates
// are dropped, preserving first-seen order. A path that cannot be absolutized is
// kept as-is rather than dropped.
func Normalize(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		abs = filepath.Clean(abs)
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	return out
}
