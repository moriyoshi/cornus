package webbff

// Per-session activity detection, à la a terminal multiplexer's "agent awareness".
// Each persistent terminal session (see term.go) carries a detector that watches
// the session's output and classifies what its foreground program is doing:
// working, idle, or blocked waiting for a human (a permission/approval prompt).
//
// The detector is a passive tap on the output stream, NOT a second subscriber: a
// session allows at most one attached browser, and monitoring must keep working
// when no browser is attached at all — that is the whole point of reporting which
// sessions need you. So readLoop feeds the detector the same bytes it writes to the
// replay ring, and a per-detector settle timer re-classifies once output goes quiet.
//
// This is a clean-room implementation of the documented concept; the detection
// patterns (see rules.toml) are our own.

import (
	"crypto/sha256"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/tonistiigi/vt100"
)

// sessionState is the coarse activity of a session's foreground program.
type sessionState string

const (
	stateIdle    sessionState = "idle"
	stateWorking sessionState = "working"
	stateBlocked sessionState = "blocked"
)

const (
	// detScreenRows/Cols size the headless screen before a browser reports the real
	// geometry: a session can produce output while no one is attached, so we always
	// have a screen to render.
	detScreenRows = 24
	detScreenCols = 80
	// detBottomLines is how many lines of the rendered screen we match rules
	// against — the "bottom buffer", where prompts and status lines live. Matching
	// only the bottom avoids false positives from stale text higher up the screen.
	detBottomLines = 12
	// defaultDetSettle is how long output must be quiet before we re-classify. A
	// burst of output reads as working; only once it stops do we settle to idle or
	// confirm blocked. This is the debounce that keeps the state from flapping.
	defaultDetSettle = 600 * time.Millisecond
)

// detector is one session's passive state classifier. It owns a headless VT100
// screen fed every output chunk, a committed state, and a settle timer that
// re-classifies the static screen after output stops.
type detector struct {
	rules *ruleSet
	// agent is the best-effort program identity (basename of cmd[0]); immutable
	// after construction, so it is read without the lock.
	agent string

	mu       sync.Mutex
	screen   *vt100.VT100
	lastHash [32]byte
	state    sessionState
	timer    *time.Timer
	settle   time.Duration
	closed   bool

	// acked marks that the user answered the prompt currently on screen, so the
	// still-visible prompt text should not re-trigger blocked. ackedHash pins which
	// screen was acknowledged; new output (a different hash) clears the ack, so a
	// fresh prompt blocks again. Without this, the settle timer would revert to
	// blocked the moment after the user answered.
	acked     bool
	ackedHash [32]byte
}

func newDetector(rules *ruleSet, cmd []string, rows, cols uint, settle time.Duration) *detector {
	if rows == 0 {
		rows = detScreenRows
	}
	if cols == 0 {
		cols = detScreenCols
	}
	if settle <= 0 {
		settle = defaultDetSettle
	}
	return &detector{
		rules:  rules,
		agent:  agentName(cmd),
		screen: vt100.NewVT100(int(rows), int(cols)),
		state:  stateIdle,
		settle: settle,
	}
}

// agentName is the best-effort identity of the program a session launched: the
// basename of its command. A program started later inside a shell is invisible
// here (screen rules still apply regardless), so this only scopes agent-tagged
// rules and labels the UI. A plain shell is not treated as an agent.
func agentName(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	switch base := path.Base(cmd[0]); base {
	case "sh", "bash", "zsh", "ash", "dash", "fish", ".", "/", "":
		return ""
	default:
		return base
	}
}

// feed advances the headless screen with a chunk of session output. If the
// rendered bottom buffer changed, the session is producing output, so it is marked
// working and the settle timer is (re)armed to fire once output stops.
func (d *detector) feed(chunk []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	// vt100.Write never returns a hard error: it skips sequences it can't decode
	// and buffers partial tails, which is exactly the tolerance a passive tap needs.
	_, _ = d.screen.Write(chunk)
	h := sha256.Sum256([]byte(d.renderLocked()))
	if h != d.lastHash {
		d.lastHash = h
		d.acked = false // new output: any prior prompt acknowledgement is stale
		d.state = stateWorking
		d.armLocked()
	}
}

// onInput records stdin from the browser: if the session was blocked on a prompt,
// the user just answered it, so it is working again until output settles.
func (d *detector) onInput() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if d.state == stateBlocked {
		d.state = stateWorking
		d.acked = true // this prompt was answered; don't re-block on it
		d.ackedHash = d.lastHash
		d.armLocked()
	}
}

// resize keeps the headless screen the same size as the browser's, so wrapping and
// therefore the rendered text match what the user sees.
func (d *detector) resize(rows, cols uint) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || rows == 0 || cols == 0 {
		return
	}
	d.screen.Resize(int(rows), int(cols))
}

// current returns the committed state.
func (d *detector) current() sessionState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// stop halts the settle timer and freezes the detector. Idempotent.
func (d *detector) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.timer != nil {
		d.timer.Stop()
	}
}

// armLocked (re)starts the settle timer. Every output chunk pushes it out, so it
// fires only after `settle` of quiet. Caller holds d.mu.
func (d *detector) armLocked() {
	if d.timer == nil {
		d.timer = time.AfterFunc(d.settle, d.onSettle)
		return
	}
	d.timer.Reset(d.settle)
}

// onSettle runs after output has been quiet for `settle`: classify the now-static
// screen. Blocked is deliberately strict — it requires a prompt to still be
// visible on the quiet screen — which is why we only commit it here, not mid-burst.
func (d *detector) onSettle() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	screen := d.renderLocked()
	blocked := d.rules.matches(stateBlocked, d.agent, screen)
	// A prompt the user already answered (acked, screen unchanged since) is not a
	// fresh block — treat this screen as no longer blocked.
	if blocked && d.acked && d.lastHash == d.ackedHash {
		blocked = false
	}
	switch {
	case blocked:
		d.state = stateBlocked
	case d.rules.matches(stateWorking, d.agent, screen):
		d.state = stateWorking
	default:
		d.state = stateIdle
	}
}

// renderLocked flattens the bottom of the visible screen to plain text: one line
// per row, right-trimmed, trailing blank lines dropped, keeping the last
// detBottomLines. Caller holds d.mu.
func (d *detector) renderLocked() string {
	rows := d.screen.Content
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, strings.TrimRight(string(row), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > detBottomLines {
		lines = lines[len(lines)-detBottomLines:]
	}
	return strings.Join(lines, "\n")
}
