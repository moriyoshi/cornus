package activity

// Following the record as it is written.
//
// Read answers "what happened"; this answers "what is happening". They are the
// same question at different distances, so they read the same files — a follower
// is just a Read that remembers where it stopped.
//
// Polling, not inotify: the streams are appended to by processes that may be in
// another mount namespace or another container, the directory can gain a file
// when a caretaker starts, and a flight recorder must not grow a watcher
// dependency to be readable. A poll of a few stat calls per interval is
// cheaper than the failure modes of the alternative.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultPollInterval is how often a follower looks for new records. It is a
// diagnostic stream watched by a human or an agent, not a data path, so this is
// tuned for "feels live" rather than for latency.
const DefaultPollInterval = 500 * time.Millisecond

// Tailer reads a directory's records incrementally: each Next returns only what
// has been appended since the last one.
//
// The FIRST Next returns the whole existing stream. That is not a convenience —
// it is what makes following safe. A follower that read the history separately
// and then began tailing would silently drop anything written in between, and
// the records most worth following are written exactly when the system is busy.
//
// A Tailer is not safe for concurrent use.
type Tailer struct {
	dir string

	// files tracks how far into each stream we have read. The key is the full
	// path; a file that disappears is dropped, and one that is REPLACED (log
	// rotation renames the live file aside and starts a new one) is detected by
	// identity rather than by name.
	files map[string]*tailFile

	started bool
}

type tailFile struct {
	info os.FileInfo
	off  int64
}

// NewTailer returns a Tailer over dir. It does no I/O; a directory that does not
// exist yet is the normal first-boot case and simply yields nothing until it
// appears.
func NewTailer(dir string) *Tailer {
	return &Tailer{dir: dir, files: map[string]*tailFile{}}
}

// Next returns the events appended since the previous call, oldest first.
//
// It never blocks and never waits for new records: an empty result means
// "nothing new yet", which is what lets a caller decide its own pacing.
func (t *Tailer) Next() ([]Event, error) {
	first := !t.started
	t.started = true

	paths, err := t.scan(first)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(paths))
	var out []Event
	for _, p := range paths {
		seen[p] = true
		evs, err := t.readNew(p)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	// Drop vanished files so a directory churning through streams cannot grow
	// this map without bound.
	for p := range t.files {
		if !seen[p] {
			delete(t.files, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// scan lists the streams to read this round.
//
// The retained generation (.ndjson.1) is read on the FIRST round only. After
// that its appearance means a rotation just happened, and its contents are
// precisely the records we already emitted from the live file before the rename
// — reading it again would replay a whole generation of history into the middle
// of a live stream.
func (t *Tailer) scan(first bool) ([]string, error) {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("activity: read %s: %w", t.dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		switch {
		case strings.HasSuffix(n, ".ndjson"):
		case strings.HasSuffix(n, ".ndjson.1") && first:
		default:
			continue
		}
		out = append(out, filepath.Join(t.dir, n))
	}
	sort.Strings(out)
	return out, nil
}

// readNew consumes whatever has been appended to path since the last round.
func (t *Tailer) readNew(path string) ([]Event, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // rotated or removed between the scan and here
		}
		return nil, fmt.Errorf("activity: stat %s: %w", path, err)
	}
	tf := t.files[path]
	switch {
	case tf == nil:
		tf = &tailFile{}
		t.files[path] = tf
	case !os.SameFile(tf.info, st), st.Size() < tf.off:
		// The name now points at a different file (rotation), or the file shrank.
		// Either way our offset describes a stream that no longer exists, so start
		// over — a fresh live file only holds records we have not seen.
		tf.off = 0
	}
	tf.info = st
	if st.Size() == tf.off {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("activity: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(tf.off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("activity: seek %s: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("activity: read %s: %w", path, err)
	}

	// Stop at the last newline and leave the remainder for the next round. A
	// writer is appending concurrently, so the tail of what we just read may be
	// half a record — consuming it would corrupt one event and, worse, skip it
	// permanently once the offset moved past.
	nl := bytes.LastIndexByte(b, '\n')
	if nl < 0 {
		return nil, nil
	}
	complete := b[:nl+1]
	tf.off += int64(len(complete))

	var out []Event
	for _, line := range strings.Split(string(complete), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // a torn line from an earlier crash; the rest is still readable
		}
		out = append(out, e)
	}
	return out, nil
}

// Follow calls fn for every record in dir, then for every record appended to it,
// until ctx is done. It returns ctx.Err() on cancellation — the normal end of a
// follow — or the first error from fn or from reading.
//
// interval defaults to DefaultPollInterval when not positive.
func Follow(ctx context.Context, dir string, interval time.Duration, fn func(Event) error) error {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	t := NewTailer(dir)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		events, err := t.Next()
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := fn(e); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// ParseSince accepts an RFC3339 instant or a duration back from now, because
// "the last two hours" is what an investigator actually reaches for and an
// absolute timestamp is what a machine has. An empty string is the zero time,
// meaning no lower bound.
func ParseSince(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("want RFC3339 (2026-07-26T10:00:00Z) or a duration (2h), got %q", s)
	}
	return t, nil
}
