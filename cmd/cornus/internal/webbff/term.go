package webbff

// Persistent terminal sessions for the tiled web workspace. Unlike handleExecWS
// (which ties one exec to one browser WebSocket), these sessions live in the BFF
// process independently of any browser: the "cornus web" process is the tmux
// server, a browser tab is a client. A session holds its exec stream open, buffers
// recent output in a ring, and lets browser sockets attach/detach — so a page
// reload reattaches by id and replays scrollback instead of killing the shell.
// Sessions live until explicitly killed or the process exits; they do not survive
// a BFF restart.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"cornus/pkg/api"
	"cornus/pkg/shells"
)

const (
	// termRingCap bounds the per-session replay buffer: the most recent output
	// bytes a (re)attaching browser receives before live forwarding starts.
	termRingCap = 128 << 10
	// termLinger is how long a dead session (its shell exited) stays listable and
	// attachable — so a reattaching browser can still see the final scrollback —
	// before it is reaped.
	termLinger = 30 * time.Second
	// termMaxSessions caps live sessions to bound leaks from abandoned shells.
	termMaxSessions = 64
)

// execClient is the slice of *client.Client the terminal manager needs. Declaring
// it as an interface keeps the manager unit-testable with an in-memory fake.
type execClient interface {
	ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error)
	ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error)
	ExecResize(ctx context.Context, execID string, height, width uint) error
}

// ringBuffer keeps the most recent up-to-cap output bytes for replay on attach.
type ringBuffer struct {
	buf []byte
	cap int
}

func newRingBuffer(capacity int) *ringBuffer { return &ringBuffer{cap: capacity} }

// write appends p, keeping only the last cap bytes. copy handles the overlapping
// forward move (dst index 0 < src index) correctly.
func (r *ringBuffer) write(p []byte) {
	if len(p) >= r.cap {
		r.buf = append(r.buf[:0], p[len(p)-r.cap:]...)
		return
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.cap:]...)
	}
}

func (r *ringBuffer) snapshot() []byte {
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// subCloseReason explains why a subscriber was closed. handleTermAttach turns it
// into a WebSocket close code so the browser can tell a genuinely ended session
// (where reconnect means a fresh shell) from a takeover by another tab, or from a
// transient drop it should silently reattach through.
type subCloseReason int

const (
	subEnded      subCloseReason = iota // the session's process exited or it was killed
	subSuperseded                       // a newer browser attach took the subscriber slot
	subDetached                         // this browser's own socket went away
)

// subscriber is one attached browser socket. The session reader goroutine fans
// live output into ch and closes done when the subscriber is superseded, detached,
// or the session ends. reason records which; it is set once under the close-once
// guard, so it is safe to read after done fires (the close happens-before the recv).
type subscriber struct {
	ch     chan []byte
	done   chan struct{}
	once   sync.Once
	reason subCloseReason
}

func newSubscriber() *subscriber {
	return &subscriber{ch: make(chan []byte, 64), done: make(chan struct{})}
}

func (s *subscriber) close(reason subCloseReason) {
	s.once.Do(func() {
		s.reason = reason
		close(s.done)
	})
}

// termSession is one persistent exec: its stream, replay ring, and at most one
// attached subscriber. A single readLoop goroutine copies stream output into the
// ring and (if attached) the subscriber.
type termSession struct {
	id       string
	workload string
	cmd      []string
	execID   string
	created  time.Time

	ec     execClient
	stream net.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mgr    *termManager

	mu    sync.Mutex
	ring  *ringBuffer
	rows  uint
	cols  uint
	alive bool
	sub   *subscriber
	// title is the window title the session's output last set, via the OSC
	// sequence every terminal reads (see osc.go). It names what is actually
	// running — "vim README.md" rather than the shell's argv — and is empty until
	// something in the session sets one, which plenty of shells never do.
	title string
	// cwd is the working directory the session last reported via OSC 7, absolute
	// inside the container. Unlike the launch `dir` it tracks the user's cd's, so
	// it is what a split should inherit. Empty far more often than title is: the
	// hook that emits OSC 7 is not present in a stock container image.
	cwd string
	// pid is the session process's id inside the CONTAINER's PID namespace,
	// announced once by the launch wrapper (pkg/shells.WrapAnnouncePID) before the
	// shell produced any output. Zero when the launch could not be wrapped, which
	// is the normal state for a non-shell command and for fish.
	pid int
	// procCwd/procComm are the last successful /proc probe's answers — the
	// fallback for a session whose shell reports nothing itself. probeAt bounds how
	// often the probe runs and probing keeps two polls from racing into two execs.
	procCwd  string
	procComm string
	probeAt  time.Time
	probing  bool
	// probeMisses counts consecutive probes that learned nothing, so a container
	// with no readable /proc stops being asked instead of paying an exec forever.
	probeMisses int

	// det passively classifies this session's activity (working/idle/blocked)
	// from its output. It is fed by readLoop, never occupies the subscriber slot,
	// and is nil only in the degenerate case of a session created without one.
	det *detector
}

// readLoop pumps stream output into the ring and the current subscriber until the
// stream ends, then marks the session dead. Sending to the subscriber applies
// backpressure to the shell (like a TTY) rather than dropping bytes; a detach
// unblocks it via sub.done.
func (ts *termSession) readLoop() {
	buf := make([]byte, 32<<10)
	// The scanner carries escape-sequence state across reads, since a sequence can
	// straddle a chunk boundary. It is touched only on this goroutine, so it needs
	// no lock; only the committed values cross to info().
	var oscs oscScanner
	for {
		n, err := ts.stream.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			u := oscs.scan(chunk)
			ts.mu.Lock()
			ts.ring.write(chunk)
			if u.hasTitle {
				ts.title = u.title
			}
			if u.hasCwd {
				ts.cwd = u.cwd
			}
			if u.hasPID {
				ts.pid = u.pid
			}
			sub := ts.sub
			ts.mu.Unlock()
			if ts.det != nil {
				// OSC is EVIDENCE, not only noise. The osc_title / osc_progress
				// regions of a manifest read these directly, which is why the
				// sequences are handed over rather than merely stripped.
				if u.hasTitle {
					ts.det.setOSC(u.title, "")
				}
				// The VISIBLE bytes, not the raw chunk: the detector models a screen
				// and classifies what is on it, and the vt100 library it uses paints
				// OSC payloads as text (see oscUpdate.visible). The browser still gets
				// the raw chunk — xterm.js understands OSC perfectly well, and the
				// replay ring has to hold exactly what the session sent.
				ts.det.feed(u.visible) // passive activity tap; has its own lock
			}
			if sub != nil {
				select {
				case sub.ch <- chunk:
				case <-sub.done:
				}
			}
		}
		if err != nil {
			ts.markDead()
			return
		}
	}
}

// attachment binds a session to one browser socket: the replay snapshot plus the
// live subscriber. Taking the snapshot and installing the subscriber under the
// same lock guarantees every output chunk is delivered exactly once — via replay
// or via the live channel, never both.
type attachment struct {
	ts     *termSession
	sub    *subscriber
	replay []byte
	alive  bool
}

func (ts *termSession) attach() *attachment {
	sub := newSubscriber()
	ts.mu.Lock()
	replay := ts.ring.snapshot()
	old := ts.sub
	ts.sub = sub
	alive := ts.alive
	ts.mu.Unlock()
	if old != nil {
		old.close(subSuperseded) // a newer browser took over this session
	}
	if !alive {
		// The shell already exited (linger window): deliver scrollback, then end.
		sub.close(subEnded)
	}
	return &attachment{ts: ts, sub: sub, replay: replay, alive: alive}
}

func (a *attachment) detach() { a.ts.detach(a.sub) }

func (ts *termSession) detach(sub *subscriber) {
	ts.mu.Lock()
	if ts.sub == sub {
		ts.sub = nil
	}
	ts.mu.Unlock()
	sub.close(subDetached)
}

func (ts *termSession) input(p []byte) {
	if ts.det != nil {
		ts.det.onInput() // a keystroke answers a blocked prompt
	}
	_, _ = ts.stream.Write(p)
}

func (ts *termSession) resize(h, w uint) {
	ts.mu.Lock()
	ts.rows, ts.cols = h, w
	ts.mu.Unlock()
	if ts.det != nil {
		ts.det.resize(h, w) // keep the headless screen the same size as the browser's
	}
	_ = ts.ec.ExecResize(ts.ctx, ts.execID, h, w)
}

// markDead flips the session dead, signals any attached browser, and schedules the
// reap after the linger window.
func (ts *termSession) markDead() {
	ts.mu.Lock()
	ts.alive = false
	sub := ts.sub
	ts.mu.Unlock()
	if sub != nil {
		sub.close(subEnded)
	}
	if ts.det != nil {
		ts.det.stop()
	}
	if ts.mgr != nil {
		time.AfterFunc(ts.mgr.linger, func() { ts.mgr.remove(ts.id) })
	}
}

// shutdown tears the session down immediately (explicit kill).
func (ts *termSession) shutdown() {
	ts.cancel()
	_ = ts.stream.Close() // unblocks readLoop, which then markDead()s
	ts.mu.Lock()
	sub := ts.sub
	ts.sub = nil
	ts.alive = false
	ts.mu.Unlock()
	if sub != nil {
		sub.close(subEnded)
	}
	if ts.det != nil {
		ts.det.stop()
	}
}

func (ts *termSession) info() termInfo {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	// State is reported only while the session is alive: a dead session's last
	// screen is stale, so the UI shows "ended", not a frozen activity badge. Agent
	// is immutable, so it is safe to read outside the detector's own lock.
	state := ""
	agent := ""
	if ts.det != nil {
		agent = ts.det.agent
		if ts.alive {
			state = string(ts.det.current())
		}
	}
	// Title is reported even for a dead session, unlike State. State is a claim
	// about what the session is doing NOW, which a frozen screen cannot support;
	// the title is a name, and the last name a session went by stays the truthful
	// answer to "which tab was this?" through the linger window. Reverting it to
	// the argv at death would rename the tab out from under the user at the exact
	// moment they are looking for it.
	// OSC wins over the probe wherever both have an answer: it came from the
	// session's own output at the moment it changed, where the probe is at best
	// procProbeTTL stale. The probe is the floor, not a correction.
	title, cwd := ts.title, ts.cwd
	if title == "" && ts.procComm != "" {
		title = ts.procComm
	}
	if cwd == "" {
		cwd = ts.procCwd
	}
	return termInfo{
		ID: ts.id, Workload: ts.workload, Cmd: ts.cmd,
		Alive: ts.alive, Rows: ts.rows, Cols: ts.cols, Created: ts.created,
		State: state, Agent: agent, Title: title, Cwd: cwd,
	}
}

// procProbeMisses is how many consecutive empty probes retire a session's probing.
// Some images have no readable /proc at all, and a session there would otherwise
// pay an exec every poll forever to learn nothing. Three is enough to ride out a
// shell that exited between the poll and the probe.
const procProbeMisses = 3

// maybeProbe starts a /proc probe if this session needs one and has not had one
// recently. It NEVER blocks the caller: the answer lands in time for the next
// poll, which is what keeps the session list a cheap request.
//
// A session that OSC already answers for is not probed. That is the whole ordering
// of this feature — the stream is free, the exec is not.
func (ts *termSession) maybeProbe() {
	if ts.mgr == nil || ts.mgr.cap == nil {
		return
	}
	ts.mu.Lock()
	stale := time.Since(ts.probeAt) > procProbeTTL
	// No longer gated on title/cwd being missing. The probe's `comm` is what scopes
	// the detection rules to the program actually in the foreground, and that
	// changes whenever the user runs something — so a session whose shell reports a
	// title and a directory still has to be asked WHAT IS RUNNING. Cost is bounded
	// the same way as before: only while the list is being polled, at most once per
	// procProbeTTL, and retired after procProbeMisses fruitless attempts.
	wants := ts.alive && ts.pid > 0 && !ts.probing && stale &&
		ts.probeMisses < procProbeMisses
	if !wants {
		ts.mu.Unlock()
		return
	}
	// The script runs under the session's OWN shell, the one interpreter this
	// image is known to have. A session whose argv is not a shell was never
	// wrapped either, so it has no pid and never reaches here.
	interp, ok := shells.FromArgv(ts.cmd)
	if !ok {
		ts.probeMisses = procProbeMisses // nothing to run it with; stop asking
		ts.mu.Unlock()
		return
	}
	ts.probing = true
	ts.probeAt = time.Now()
	workload, pid := ts.workload, ts.pid
	cap := ts.mgr.cap
	ts.mu.Unlock()

	go func() {
		info := probeProc(ts.ctx, cap, workload, interp, pid)
		ts.mu.Lock()
		ts.probing = false
		if info.cwd == "" && info.comm == "" {
			ts.probeMisses++
			ts.mu.Unlock()
			return
		}
		ts.probeMisses = 0
		// Each field is kept only when the probe actually produced it: a process
		// can have a readable comm and an unresolvable cwd, and dropping the old
		// cwd in that case would lose a good answer to a partial one.
		if info.cwd != "" {
			ts.procCwd = info.cwd
		}
		if info.comm != "" {
			ts.procComm = info.comm
		}
		// Unlocked BEFORE telling the detector, deliberately. The detector has a
		// lock of its own and readLoop already calls into it without holding this
		// one; taking them nested here would be the only place in the file that
		// orders the two, which is how a deadlock gets introduced later.
		ts.mu.Unlock()

		// The foreground program SCOPES the detection rules — it is the
		// classifier's input, not merely a label. This is the only way a program
		// the user started INSIDE a shell becomes visible to detection.
		if ts.det != nil {
			ts.det.setForeground(info.comm, info.argv)
		}
	}()
}

// termManager owns the live sessions.
type termManager struct {
	mu          sync.Mutex
	sessions    map[string]*termSession
	ec          execClient
	maxSessions int
	linger      time.Duration

	// rules is the compiled agent-detection rule set, loaded once and shared by
	// every session's detector. detSettle is the detectors' quiet-debounce window;
	// tests shorten it (like linger and maxSessions) before creating a session.
	rules     *ruleSet
	detSettle time.Duration

	// cap runs the /proc probe (procprobe.go). Nil disables probing entirely,
	// which is what a manager built without one gets — the OSC path still works,
	// so a session simply keeps whatever its own output reported.
	cap procCapturer
}

func newTermManager(ec execClient) *termManager {
	return &termManager{
		sessions:    map[string]*termSession{},
		ec:          ec,
		maxSessions: termMaxSessions,
		linger:      termLinger,
		rules:       loadRules(),
		detSettle:   defaultDetSettle,
	}
}

// Create opens a new persistent exec. The session runs on a background context so
// it outlives the HTTP request that created it.
// dir is the shell's starting directory, absolute inside the container, or "" for the
// image's own default. It is passed through as ExecConfig.WorkingDir, which the docker,
// containerd, bare-host and incus backends honour and the kubernetes one cannot express
// (it warns and ignores) — so a caller gets the directory it asked for on most targets
// and the image default on kube.
func (m *termManager) Create(ctx context.Context, workload string, cmd []string, dir string) (*termSession, error) {
	if workload == "" {
		return nil, fmt.Errorf("workload is required")
	}
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	m.mu.Lock()
	if len(m.sessions) >= m.maxSessions {
		m.mu.Unlock()
		return nil, fmt.Errorf("too many terminal sessions (max %d)", m.maxSessions)
	}
	m.mu.Unlock()

	sctx, cancel := context.WithCancel(context.Background())
	execID, err := m.ec.ExecCreate(sctx, workload, api.ExecConfig{
		Cmd: cmd, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		WorkingDir: dir,
		// Ask the server to wrap the launch so the session announces its pid. It is
		// a request the server silently declines for anything it cannot wrap, and
		// `cmd` below stays the argv the USER asked for — the wrapper is a spelling
		// of the same command, and reporting it would put "sh -c printf …" in the
		// UI's Command column and in the close dialog.
		AnnouncePID: true,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	stream, err := m.ec.ExecStart(sctx, execID, api.ExecStartConfig{Tty: true})
	if err != nil {
		cancel()
		return nil, err
	}
	ts := &termSession{
		id: newTermID(), workload: workload, cmd: cmd, execID: execID,
		created: time.Now(), ec: m.ec, stream: stream, ctx: sctx, cancel: cancel, mgr: m,
		ring: newRingBuffer(termRingCap), alive: true,
		det: newDetector(m.rules, cmd, 0, 0, m.detSettle),
	}
	m.mu.Lock()
	m.sessions[ts.id] = ts
	m.mu.Unlock()
	go ts.readLoop()
	return ts, nil
}

func (m *termManager) Get(id string) *termSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *termManager) List() []termInfo {
	m.mu.Lock()
	sessions := make([]*termSession, 0, len(m.sessions))
	for _, ts := range m.sessions {
		sessions = append(sessions, ts)
	}
	m.mu.Unlock()
	out := make([]termInfo, 0, len(sessions))
	for _, ts := range sessions {
		// Probing is driven from HERE, off the request the browser already makes
		// every 2s, rather than from a background ticker. That is what bounds the
		// cost: no browser polling means no probes at all, and the work scales with
		// what is actually being displayed instead of with how many shells happen
		// to be alive. It never blocks — this call returns the cached answer and
		// the fresh one lands in time for the next poll.
		ts.maybeProbe()
		out = append(out, ts.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (m *termManager) Kill(id string) bool {
	m.mu.Lock()
	ts, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	ts.shutdown()
	return true
}

// closeAll kills every live session — the deterministic teardown a host runs when
// it stops serving this BFF.
//
// Sessions deliberately outlive their HTTP requests (that is the whole point: a
// page reload reattaches instead of killing the shell), so nothing else ever reaps
// them. In the foreground CLI the process exit did that implicitly; inside the
// long-lived client agent it would not, and every session's exec stream would leak
// for the agent's lifetime. Idempotent.
func (m *termManager) closeAll() {
	m.mu.Lock()
	sessions := make([]*termSession, 0, len(m.sessions))
	for _, ts := range m.sessions {
		sessions = append(sessions, ts)
	}
	m.sessions = map[string]*termSession{}
	m.mu.Unlock()
	for _, ts := range sessions {
		ts.shutdown()
	}
}

// remove drops a session from the map (used by the linger reaper). Idempotent.
func (m *termManager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func newTermID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- HTTP surface -----------------------------------------------------------

type termInfo struct {
	ID       string    `json:"id"`
	Workload string    `json:"workload"`
	Cmd      []string  `json:"cmd"`
	Alive    bool      `json:"alive"`
	Rows     uint      `json:"rows"`
	Cols     uint      `json:"cols"`
	Created  time.Time `json:"created"`
	// State is the detected activity of the session's foreground program:
	// "working", "idle", or "blocked" (waiting on a human). Empty for a dead
	// session. Agent is the program currently in the FOREGROUND, learned from the
	// /proc probe and falling back to the basename of cmd[0]; it is what scopes the
	// detection rules, and is empty when the foreground program is a shell or could
	// not be identified.
	State string `json:"state,omitempty"`
	Agent string `json:"agent,omitempty"`
	// Title is the window title the session's output last set via OSC 0/2 — the
	// name of what is actually running in it, which the fixed Cmd cannot track.
	// Absent when nothing in the session ever set one, which is normal: a UI must
	// treat it as a nicety and fall back to Cmd.
	Title string `json:"title,omitempty"`
	// Cwd is the working directory the session last reported via OSC 7, absolute
	// inside the container. It is the live answer that the creation-time Dir can
	// only approximate. Absent unless the session's shell runs a hook that emits
	// it, which a stock container image does not — so a consumer must fall back to
	// the pane's own dir rather than treating "no cwd" as "the root".
	Cwd string `json:"cwd,omitempty"`
}

type createTermRequest struct {
	Workload string   `json:"workload"`
	Cmd      []string `json:"cmd,omitempty"`
	// Cmdline is the same command as a single STRING, split server-side with the
	// shell-words parser (see pkg/shells.Split). It exists so a hand-typed command and a
	// discovered candidate travel the same way: a browser that wrapped a typed
	// "/bin/busybox sh" into a one-element Cmd would be asking to execute a file
	// whose name contains a space, which is not what the user typed and cannot run.
	// Ignored when Cmd is set.
	Cmdline string `json:"cmdline,omitempty"`
	// Dir is where the shell starts, absolute inside the container. The web UI sends it
	// when a terminal is opened from the file browser, so the session lands in the
	// folder the user was looking at rather than at the image's default.
	//
	// Not every backend can honour it: kubernetes' PodExecOptions has no
	// working-directory field, and that backend already warns and ignores (see
	// pkg/deploy/kubernetes). It is therefore a PREFERENCE, and a caller must not
	// assume the shell came up where it asked.
	Dir string `json:"dir,omitempty"`
}

// cmd resolves the request's command: the explicit argv wins, else the split
// command line, else nothing (the session falls back to /bin/sh).
func (r createTermRequest) cmd() []string {
	if len(r.Cmd) > 0 {
		return r.Cmd
	}
	return shells.Split(r.Cmdline)
}

// dir resolves the request's working directory, or "" for none. Only a clean absolute
// path is accepted and anything else is dropped rather than rejected: a bad Dir should
// cost the user the cwd, not the terminal.
//
// This is HYGIENE, NOT A BOUNDARY. It confines nothing — the session it opens is an
// interactive shell that can cd anywhere the container allows the moment it starts, and
// that is the whole point of the feature. What it buys is that a relative or unclean
// value cannot reach a backend and be resolved against some directory neither side
// chose.
func (r createTermRequest) dir() string {
	if r.Dir == "" {
		return ""
	}
	clean := path.Clean(r.Dir)
	if !path.IsAbs(clean) {
		return ""
	}
	return clean
}

func (s *Server) handleTermList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.terms.List())
}

func (s *Server) handleTermCreate(w http.ResponseWriter, r *http.Request) {
	var req createTermRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Workload == "" {
		http.Error(w, "workload is required", http.StatusBadRequest)
		return
	}
	ts, err := s.terms.Create(r.Context(), req.Workload, req.cmd(), req.dir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, ts.info())
}

func (s *Server) handleTermKill(w http.ResponseWriter, r *http.Request) {
	if s.terms.Kill(r.PathValue("id")) {
		writeJSON(w, map[string]string{"result": "killed"})
		return
	}
	http.Error(w, "no such terminal session", http.StatusNotFound)
}

// WebSocket close codes the attach handler sends so the browser can distinguish an
// ended session from a transient drop. 4000-4999 is the RFC 6455 application range.
const (
	wsCloseEnded      = websocket.StatusCode(4000) // the session's process exited or it was killed
	wsCloseSuperseded = websocket.StatusCode(4001) // superseded by a newer attach (another tab)
)

// closeFrame maps a subscriber close reason to the WS close code/text the browser
// receives. A detached subscriber means this socket is already gone, so the frame is
// a best-effort normal closure that the browser will likely never read.
func closeFrame(r subCloseReason) (websocket.StatusCode, string) {
	switch r {
	case subSuperseded:
		return wsCloseSuperseded, "superseded"
	case subDetached:
		return websocket.StatusNormalClosure, "detached"
	default: // subEnded
		return wsCloseEnded, "ended"
	}
}

// handleTermAttach bridges a browser WebSocket to a persistent session: first the
// replay buffer, then live output; binary browser frames are stdin, text frames
// carry {"resize":{h,w}} control (same protocol as handleExecWS). Closing the
// socket detaches without killing the session.
func (s *Server) handleTermAttach(w http.ResponseWriter, r *http.Request) {
	sess := s.terms.Get(r.PathValue("id"))
	if sess == nil {
		http.Error(w, "no such terminal session", http.StatusNotFound)
		return
	}
	conn, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()

	q := r.URL.Query()
	if h, errH := strconv.Atoi(q.Get("h")); errH == nil {
		if wd, errW := strconv.Atoi(q.Get("w")); errW == nil {
			sess.resize(uint(h), uint(wd))
		}
	}

	att := sess.attach()
	defer att.detach()

	if len(att.replay) > 0 {
		if err := conn.Write(ctx, websocket.MessageBinary, att.replay); err != nil {
			return
		}
	}

	// Browser -> session: binary frames are stdin, text frames are control.
	go func() {
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				att.detach()
				return
			}
			switch typ {
			case websocket.MessageBinary:
				sess.input(data)
			case websocket.MessageText:
				var ctl execControl
				if json.Unmarshal(data, &ctl) == nil && ctl.Resize != nil {
					sess.resize(ctl.Resize.H, ctl.Resize.W)
				}
			}
		}
	}()

	// Session -> browser, until the socket, session, or subscriber ends.
	for {
		select {
		case <-ctx.Done():
			return
		case <-att.sub.done:
			// The subscriber ended because the shell exited/was killed (reconnect
			// means a fresh shell), a newer attach superseded us (another tab has it),
			// or our own socket detached. The close code lets the browser tell a real
			// end from a transient drop it should silently reattach through.
			code, text := closeFrame(att.sub.reason)
			conn.Close(code, text)
			return
		case chunk := <-att.sub.ch:
			if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		}
	}
}
