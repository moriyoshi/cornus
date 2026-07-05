package dockerproxy

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"

	"cornus/pkg/api"
)

// containerRecord is the proxy's view of one Docker container: the buffered
// create request, the derived cornus deployment, and (once started) its live
// deploy-attach session plus the local port-forward listeners publishing its
// PortBindings.
//
// sess/fwd/state/startedC are guarded by mu: `docker run` drives attach, wait
// and start on concurrent connections, and attach + wait?condition=next-exit
// both arrive BEFORE start and must block until the session goes live.
type containerRecord struct {
	id         string
	name       string // docker name, with leading "/"
	deployment string // cornus deployment name (== spec.Name)
	created    string // RFC3339
	req        createRequest
	spec       api.DeploySpec
	networks   []string // network names this container is attached to

	mu      sync.Mutex
	sess    *session // nil until started
	cleanup func()   // withdraws the container's port exposure (portfwd.Group.Close
	// in the standalone path, or a conduit withdraw); nil until started
	state    string        // created|running|exited
	startedC chan struct{} // closed when a session goes live; replaced on stop

	// exitingSess/exitDone track the in-flight (or just-completed) exit
	// transition so a caller that loses the setExited race blocks until the
	// winner's cleanup has actually run. Guarded by mu (exitDone is captured
	// under the lock, then waited on outside it).
	exitingSess *session
	exitDone    chan struct{}
}

// session returns the live deploy-attach session, or nil.
func (rec *containerRecord) session() *session {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.sess
}

// stateNow returns the current lifecycle state.
func (rec *containerRecord) stateNow() string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.state
}

// started returns a channel that is closed once a session is live. Callers
// arriving before start (docker run's attach and wait) select on it.
func (rec *containerRecord) started() <-chan struct{} {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.startedC
}

// setRunning installs the live session (and its port-exposure cleanup, if any)
// and unblocks started() waiters. It reports false when a session is already
// installed: two concurrent /start requests can both observe session()==nil and
// race here, and only the first may install and close startedC (a second
// close(startedC) panics). The loser must stop its own session and run its
// cleanup. The check-and-install is atomic under mu.
func (rec *containerRecord) setRunning(sess *session, cleanup func()) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.sess != nil {
		return false
	}
	rec.sess = sess
	rec.cleanup = cleanup
	rec.state = "running"
	close(rec.startedC)
	return true
}

// setExited clears sess and marks the record exited, iff sess is still the
// current session (concurrent wait waiters and the self-exit watcher make this
// idempotent). It reports whether it performed the transition, so exactly one
// caller publishes the "die" event. The session's port exposure is withdrawn
// with it — exactly once, outside the lock (a portfwd.Group.Close drains
// in-flight tunnels). A fresh startedC is armed so a later start/wait cycle
// works.
//
// setExited never returns before this session's cleanup has completed: the
// caller that loses the transition race (Session.Stop wakes the self-exit
// watcher on the same channel it returns on, so a /stop or /rm handler and the
// watcher both call setExited for the same session) blocks on the winner's
// cleanup. That guarantees a /stop or /rm response is not written until the
// port listener is actually closed — otherwise a client that reconnects the
// moment stop returns can still reach the withdrawn port.
func (rec *containerRecord) setExited(sess *session) bool {
	rec.mu.Lock()
	if sess == nil {
		rec.mu.Unlock()
		return false
	}
	if rec.sess != sess {
		// Someone else already began (or finished) this session's exit. If its
		// cleanup is still in flight, wait for it so every caller observes the
		// port exposure as withdrawn on return.
		var wait chan struct{}
		if rec.exitingSess == sess {
			wait = rec.exitDone
		}
		rec.mu.Unlock()
		if wait != nil {
			<-wait
		}
		return false
	}
	cleanup := rec.cleanup
	done := make(chan struct{})
	rec.exitingSess = sess
	rec.exitDone = done
	rec.sess = nil
	rec.cleanup = nil
	rec.state = "exited"
	rec.startedC = make(chan struct{})
	rec.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
	close(done)
	return true
}

// all snapshots every record (for Proxy.Close).
func (r *registry) all() []*containerRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*containerRecord, 0, len(r.byID))
	for _, rec := range r.byID {
		out = append(out, rec)
	}
	return out
}

// registry holds the proxy's containers, keyed by full id.
type registry struct {
	mu   sync.Mutex
	byID map[string]*containerRecord
}

func newRegistry() *registry { return &registry{byID: map[string]*containerRecord{}} }

func (r *registry) put(rec *containerRecord) {
	r.mu.Lock()
	r.byID[rec.id] = rec
	r.mu.Unlock()
}

func (r *registry) del(id string) {
	r.mu.Lock()
	delete(r.byID, id)
	r.mu.Unlock()
}

// get resolves a record by full id, 12-char short id, or name (with/without the
// leading "/").
func (r *registry) get(ref string) *containerRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.byID[ref]; ok {
		return rec
	}
	want := "/" + strings.TrimPrefix(ref, "/")
	for _, rec := range r.byID {
		if strings.HasPrefix(rec.id, ref) && len(ref) >= 12 {
			return rec
		}
		if rec.name == want {
			return rec
		}
	}
	return nil
}

// list returns all records whose labels satisfy the given label filters and,
// unless all, are running.
func (r *registry) list(all bool, labelFilters map[string]string) []*containerRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*containerRecord
	for _, rec := range r.byID {
		if !all && rec.stateNow() != "running" {
			continue
		}
		if !matchesLabels(rec.req.Labels, labelFilters) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// matchesLabels reports whether the record's labels satisfy every filter
// ("key" requires presence; "key=value" requires equality).
func matchesLabels(labels map[string]string, filters map[string]string) bool {
	for k, v := range filters {
		got, ok := labels[k]
		if !ok {
			return false
		}
		if v != "" && got != v {
			return false
		}
	}
	return true
}

// newID returns a random 64-hex container id.
func newID() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// deploymentName derives a cornus-valid deployment name from a docker name (or
// the short id when unnamed): lowercased, non-alnum collapsed to '-'.
func deploymentName(dockerName, id string) string {
	n := strings.TrimPrefix(dockerName, "/")
	if n == "" {
		return "cornus-" + id[:12]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(n) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "cornus-" + id[:12]
	}
	return s
}
