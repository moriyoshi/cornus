// Package activity is cornus's flight recorder: a durable, machine-readable
// record of what the server and its caretakers were doing, kept on disk so it
// can be read after the process that wrote it is gone.
//
// It exists because nothing else survives an incident. Live introspection
// (backend.List/Status) answers only "what is true right now", and only for what
// the runtime still remembers. Structured logs are ephemeral and go with the
// container. OTel spans leave the box entirely and are off by default. So an
// operator arriving after a failure can see the wreckage but not the flight.
//
// # The model
//
// Every record is one line of NDJSON. Work is recorded as a begin/end PAIR
// sharing an id, which makes the interesting question answerable by absence: an
// activity with a begin and no end did not finish. That covers both halves of
// what a recorder is for —
//
//   - a process lifetime is itself an activity (KindServer, KindCaretaker), so a
//     lifetime with no end is a process that did not shut down cleanly: SIGKILL,
//     OOM, `docker rm -f`, a panic, a host reboot;
//   - an effect-bearing activity with no end (KindMount9P) is something that may
//     still exist with nobody looking after it.
//
// Every record carries the writing process's instance id, so a stream groups
// into incarnations: this run started at 10:02, mounted these, built that, and
// never ended. That grouping is what lets a reader tell a mount stranded by a
// crash (its incarnation also never ended) from one stranded despite a clean
// shutdown (a broken unwind path — a different bug, and a louder one).
//
// # Durability
//
// Split by whether anything must be undone, because paying for the strongest
// guarantee everywhere would put an fsync on the build hot path for no benefit:
//
//   - KindMount9P is strictly write-ahead — its begin is fsynced BEFORE the mount
//     syscall, its end written after the unmount. That ordering is the whole
//     reason an open record can be trusted to mean "an effect may exist".
//   - Everything else is a best-effort append. Losing the last line of a build
//     record to a power cut costs a little forensic detail; it strands nothing.
//
// The log is bounded like any recorder: size-capped with one retained
// generation.
package activity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind names a class of recorded work.
type Kind string

const (
	// KindServer is a cornus server's whole lifetime. Unfinished means the
	// server did not complete its shutdown unwind.
	KindServer Kind = "server"
	// KindCaretaker is a caretaker sidecar's whole lifetime, same reading.
	KindCaretaker Kind = "caretaker"
	// KindMount9P is a kernel 9P mount. Unfinished means the mountpoint may
	// still exist with no process owning it — the one kind recovery acts on.
	KindMount9P Kind = "9p-mount"
	// KindBuild is one image build.
	KindBuild Kind = "build"
	// KindDeploy is one deploy (or deploy-attach session).
	KindDeploy Kind = "deploy"
	// KindService is one RUN of a supervised child (pkg/supervisor) — one
	// Serve attempt, so a crash-looping child appears as repeated pairs rather
	// than one long silence. Unfinished means that service was still running
	// when its process died, which is the closest thing to a snapshot of what
	// the process was doing at the moment it stopped.
	KindService Kind = "service"
)

// writeAhead reports whether this kind's begin must be durable before the effect
// it describes is created. Only kinds whose effect can OUTLIVE the process need
// it: for the others the record is forensic, and an fsync would buy nothing.
func (k Kind) writeAhead() bool { return k == KindMount9P }

// Phase distinguishes the two halves of an activity.
type Phase string

const (
	PhaseBegin Phase = "begin"
	PhaseEnd   Phase = "end"
)

// Status is an ended activity's outcome.
const (
	StatusOK    = "ok"
	StatusError = "error"
	// StatusRecovered marks an activity closed by a LATER incarnation rather
	// than by the one that began it — a crashed lifetime, or an effect the next
	// process had to undo. It is deliberately distinct from StatusOK: the
	// activity did not complete, and reading it back as if it had would erase
	// the incident. It is equally deliberately not left open, so the unfinished
	// set converges instead of growing a permanent backlog of old crashes.
	StatusRecovered = "recovered"
)

// Event is one line of the log.
type Event struct {
	// TS is RFC3339Nano, so the text sorts in time order and parses machine-side.
	TS string `json:"ts"`
	// Proc is the writing program ("server", "caretaker").
	Proc string `json:"proc"`
	// Instance identifies THIS run of that program, so records group into
	// incarnations across restarts.
	Instance string `json:"instance"`
	PID      int    `json:"pid,omitempty"`

	Kind  Kind   `json:"kind"`
	Phase Phase  `json:"phase"`
	ID    string `json:"id"`
	// Target is the thing acted on, when there is one: a mountpoint, an image
	// ref. It is what recovery needs, so it is a field rather than an attr.
	Target string `json:"target,omitempty"`
	// Attrs is free-form context for a human or a machine reading back.
	Attrs map[string]string `json:"attrs,omitempty"`

	// Status and Err are set on PhaseEnd only.
	Status string `json:"status,omitempty"`
	Err    string `json:"err,omitempty"`
}

// Unix returns the event's timestamp, or the zero time if unparseable.
func (e Event) Unix() time.Time {
	t, err := time.Parse(time.RFC3339Nano, e.TS)
	if err != nil {
		return time.Time{}
	}
	return t
}

// MaxBytesEnv caps the live log file; the previous generation is retained
// alongside it, so the on-disk ceiling is about twice this.
const MaxBytesEnv = "CORNUS_ACTIVITY_MAX_BYTES"

const defaultMaxBytes = 8 << 20 // 8 MiB

// FileName is the recorder's file within its directory. One file per process
// kind keeps the server's and a caretaker's streams separable when they share a
// directory, which on the host backends they do.
func FileName(proc string) string { return proc + ".ndjson" }

// Recorder appends to one process's stream.
//
// A nil *Recorder is a fully working no-op: recording is best-effort
// instrumentation, and a caller with no recorder configured must not have to
// branch at every call site.
type Recorder struct {
	dir      string
	proc     string
	instance string
	pid      int

	mu   sync.Mutex
	f    *os.File
	seq  uint64
	fail error // first write failure, for Err
}

// Activity is one in-flight begin/end pair.
type Activity struct {
	rec    *Recorder
	id     string
	kind   Kind
	target string
	ended  bool
}

// Open prepares dir and opens proc's stream for appending, rotating first if the
// previous run left it over the size cap.
func Open(dir, proc string) (*Recorder, error) {
	if dir == "" || proc == "" {
		return nil, fmt.Errorf("activity: dir and proc are required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("activity: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName(proc))
	if err := rotateIfNeeded(path, maxBytes()); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("activity: open %s: %w", path, err)
	}
	return &Recorder{dir: dir, proc: proc, instance: newID(), pid: os.Getpid(), f: f}, nil
}

// Dir returns the directory this recorder writes into.
func (r *Recorder) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// Instance returns this run's identity, for callers that want to correlate their
// own logs with the recorder's stream.
func (r *Recorder) Instance() string {
	if r == nil {
		return ""
	}
	return r.instance
}

// Err reports the first write failure, if any. Recording never fails a caller's
// operation — a flight recorder that could abort the flight would be worse than
// none — so failures surface here instead.
func (r *Recorder) Err() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fail
}

// Close closes the stream. It does NOT write an end record: whether a lifetime
// ended cleanly is the caller's statement to make, and inferring it here would
// make every crash look clean.
func (r *Recorder) Close() error {
	if r == nil || r.f == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.f.Close()
	r.f = nil
	return err
}

// Begin records the start of an activity and returns the handle that ends it.
// For a write-ahead kind the record is durable before Begin returns, so the
// effect it describes can never exist without a record of it.
func (r *Recorder) Begin(k Kind, target string, attrs map[string]string) *Activity {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("%s-%d", r.instance, r.seq)
	r.mu.Unlock()

	a := &Activity{rec: r, id: id, kind: k, target: target}
	r.write(Event{
		Kind: k, Phase: PhaseBegin, ID: id, Target: target, Attrs: attrs,
	}, k.writeAhead())
	return a
}

// End records the activity's outcome. Safe on a nil Activity and idempotent.
func (a *Activity) End(err error) {
	if a == nil || a.ended {
		return
	}
	a.ended = true
	e := Event{Kind: a.kind, Phase: PhaseEnd, ID: a.id, Target: a.target, Status: StatusOK}
	if err != nil {
		e.Status, e.Err = StatusError, err.Error()
	}
	a.rec.write(e, false)
}

// Resolve closes an activity begun by ANOTHER incarnation — one this process
// found unfinished and has since dealt with.
//
// The end record keeps the original id, so the pair still matches, but carries
// THIS process's provenance: reading back, it is visible that instance B closed
// instance A's activity, and when. status is normally StatusRecovered.
func (r *Recorder) Resolve(begin Event, status, msg string) {
	if r == nil {
		return
	}
	r.write(Event{
		Kind: begin.Kind, Phase: PhaseEnd, ID: begin.ID, Target: begin.Target,
		Status: status, Err: msg,
		Attrs: map[string]string{"recoveredFrom": begin.Instance},
	}, false)
}

// ID returns the activity's id, so a caller can correlate it elsewhere.
func (a *Activity) ID() string {
	if a == nil {
		return ""
	}
	return a.id
}

// write appends one event. sync forces it to disk before returning.
func (r *Recorder) write(e Event, sync bool) {
	if r == nil {
		return
	}
	e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	e.Proc, e.Instance, e.PID = r.proc, r.instance, r.pid
	line, err := json.Marshal(e)
	if err != nil {
		r.setErr(err)
		return
	}
	line = append(line, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return
	}
	// One Write of one line to an O_APPEND file: the kernel appends it whole, so
	// concurrent writers (this process and a caretaker sharing the directory)
	// interleave by line and never mid-record. The lock covers this process's own
	// goroutines.
	if _, err := r.f.Write(line); err != nil {
		r.setErrLocked(err)
		return
	}
	if sync {
		if err := r.f.Sync(); err != nil {
			r.setErrLocked(err)
		}
	}
}

func (r *Recorder) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrLocked(err)
}

func (r *Recorder) setErrLocked(err error) {
	if r.fail == nil {
		r.fail = err
	}
}

// newID returns a short random identity for one process run.
func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A recorder that cannot get randomness still has to work; the pid and
		// start time are enough to tell incarnations apart in practice.
		return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func maxBytes() int64 {
	if v := os.Getenv(MaxBytesEnv); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxBytes
}

// rotatedPath is the single retained previous generation, mirroring the
// containerd log shim's scheme.
func rotatedPath(path string) string { return path + ".1" }

func rotateIfNeeded(path string, max int64) error {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Size() <= max {
		return nil
	}
	old := rotatedPath(path)
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(path, old)
}

// Read returns every event in dir, oldest first, across every process stream and
// both retained generations.
//
// Reading the retained generation matters more than it looks: an activity that
// began before a rotation and never ended would otherwise vanish from Unfinished
// exactly when the log was busiest.
func Read(dir string) ([]Event, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("activity: read %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".ndjson") || strings.HasSuffix(n, ".ndjson.1") {
			files = append(files, filepath.Join(dir, n))
		}
	}
	sort.Strings(files)
	var out []Event
	for _, f := range files {
		evs, err := readFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

func readFile(path string) ([]Event, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("activity: read %s: %w", path, err)
	}
	var out []Event
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// A torn final line is what a crash mid-write looks like. Skipping it
			// is right: the rest of the flight is still readable, which is the
			// entire point of a line-oriented recorder.
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Unfinished returns the begin events in dir with no matching end, oldest first.
//
// This is the recorder's primary question. For a lifetime kind the answer means
// "that process did not shut down cleanly"; for an effect-bearing kind it means
// "this may still exist and nobody owns it".
func Unfinished(dir string) ([]Event, error) {
	events, err := Read(dir)
	if err != nil {
		return nil, err
	}
	return UnfinishedFrom(events), nil
}

// UnfinishedFrom is Unfinished over an already-read stream, for a caller that
// needs the whole stream anyway — recovery does, because deciding what a
// stranded effect MEANS requires looking up its incarnation's lifetime.
func UnfinishedFrom(events []Event) []Event { return unfinishedFrom(events) }

func unfinishedFrom(events []Event) []Event {
	ended := make(map[string]bool)
	for _, e := range events {
		if e.Phase == PhaseEnd {
			ended[e.ID] = true
		}
	}
	var out []Event
	for _, e := range events {
		if e.Phase == PhaseBegin && !ended[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// CleanExit reports whether the given instance's lifetime activity was closed,
// i.e. whether that process finished its shutdown unwind.
//
// The distinction it enables is the point: an unfinished mount under an instance
// that exited cleanly is a broken unwind path, which is a different and louder
// bug than the same mount under an instance that was killed.
// It pairs by activity ID, not by instance, for a reason worth stating: a
// recovery record closes the CRASHED run's lifetime but is written by the
// RECOVERING run, so it carries the recoverer's instance. Matching on instance
// alone would credit the recoverer with an end it never had — reporting a
// running server as already exited — and would also mark the crashed run clean
// the moment it was recovered, erasing the incident.
//
// StatusRecovered is therefore explicitly not clean: the lifetime was closed by
// somebody else, which is exactly what "it did not shut down cleanly" means.
func CleanExit(events []Event, instance string) (known, clean bool) {
	var lifetimeID string
	for _, e := range events {
		if e.Phase == PhaseBegin && e.Instance == instance &&
			(e.Kind == KindServer || e.Kind == KindCaretaker) {
			lifetimeID, known = e.ID, true
			break
		}
	}
	if !known {
		return false, false
	}
	for _, e := range events {
		if e.Phase == PhaseEnd && e.ID == lifetimeID {
			return true, e.Status != StatusRecovered
		}
	}
	return true, false
}
