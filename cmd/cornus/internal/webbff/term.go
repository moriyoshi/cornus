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
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"cornus/pkg/api"
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
	for {
		n, err := ts.stream.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			ts.mu.Lock()
			ts.ring.write(chunk)
			sub := ts.sub
			ts.mu.Unlock()
			if ts.det != nil {
				ts.det.feed(chunk) // passive activity tap; has its own lock
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
	return termInfo{
		ID: ts.id, Workload: ts.workload, Cmd: ts.cmd,
		Alive: ts.alive, Rows: ts.rows, Cols: ts.cols, Created: ts.created,
		State: state, Agent: agent,
	}
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
func (m *termManager) Create(ctx context.Context, workload string, cmd []string) (*termSession, error) {
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
	// session. Agent is the best-effort program identity (basename of cmd[0]).
	State string `json:"state,omitempty"`
	Agent string `json:"agent,omitempty"`
}

type createTermRequest struct {
	Workload string   `json:"workload"`
	Cmd      []string `json:"cmd,omitempty"`
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
	ts, err := s.terms.Create(r.Context(), req.Workload, req.Cmd)
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
