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

	"cornus/pkg/agentdetect"
	"cornus/pkg/shells"
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
	// rules is the SUPERSEDED cornus-native rule set. Classification now runs on
	// the per-agent herdr manifests (pkg/agentdetect), which carry the region and
	// gate vocabulary these rules never had. Kept only so the user-extension path
	// under ~/.config/cornus/agent-detection/ has somewhere to land until it is
	// migrated to the manifest schema or retired — see TODO. Nothing reads it.
	rules *ruleSet

	mu sync.Mutex
	// agent is the program currently in the FOREGROUND of the session, and it is
	// what scopes the rules (see ruleSet.matches). It starts as the basename of
	// cmd[0] — right for a session launched straight into an agent — and is
	// replaced by the live foreground program as the /proc probe reports it, which
	// is the only way to see a program the user started INSIDE a shell.
	//
	// It is mutable and therefore guarded: it used to be immutable and was read
	// without the lock, which is no longer safe.
	agent string
	// manifest is the herdr manifest for `agent`, or nil when the foreground
	// program is not an agent we can classify. Nil means REPORT NOTHING: the
	// manifests are per-agent by construction, and running one agent's rules
	// against another program is exactly the mistake that made a `git log` read as
	// a prompt.
	manifest *agentdetect.Manifest
	// oscTitle / oscProgress feed the osc_title and osc_progress REGIONS. Upstream
	// treats these escape sequences as detection evidence in their own right, so
	// they are carried alongside the screen rather than stripped and discarded.
	oscTitle    string
	oscProgress string
	screen      *vt100.VT100
	lastHash    [32]byte
	state       sessionState
	timer       *time.Timer
	settle      time.Duration
	closed      bool

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
	d := &detector{
		rules:  rules,
		screen: vt100.NewVT100(int(rows), int(cols)),
		state:  stateIdle,
		settle: settle,
	}
	// Resolve the LAUNCH argv the same way a probe result is resolved, so a
	// session started directly as an agent is classifiable before any probe runs.
	d.setForeground(agentName(cmd), cmd)
	return d
}

// agentName is the best-effort identity of the program a session launched: the
// basename of its command. A program started later inside a shell is invisible
// here (screen rules still apply regardless), so this only scopes agent-tagged
// rules and labels the UI. A plain shell is not treated as an agent.
//
// "Is this a shell?" is answered by pkg/shells, the same set the
// terminal's shell discovery probes with — one definition, so a shell newly
// discoverable there cannot start being labelled an agent here. The remaining
// cases are path.Base's degenerate outputs, which are not program names at all.
func agentName(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	base := path.Base(cmd[0])
	if shells.IsShell(base) {
		return ""
	}
	switch base {
	case ".", "/", "":
		return ""
	}
	return base
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

// currentAgent returns the program the detector believes is in the foreground.
func (d *detector) currentAgent() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agent
}

// setAgent records the live foreground program, as the /proc probe reported it.
//
// A SHELL is not an agent, and reports as none: at a prompt the session is idle by
// definition, and letting "bash" scope the rules would just be the old
// everything-matches behaviour under a new name. The same rule agentName applies
// to the launch argv, so both paths answer "is this an agent?" identically.
//
// An empty name is ignored rather than clearing the agent: the probe returns
// nothing when it fails, and a failed probe is not evidence that the program
// changed. Only a positive answer moves it.
func (d *detector) setForeground(comm string, argv []string) {
	if comm == "" && len(argv) == 0 {
		return // a failed probe is not evidence that the program changed
	}
	name, manifest := "", (*agentdetect.Manifest)(nil)
	if set, err := agentdetect.Bundled(); err == nil {
		// Identification goes through the manifest set so that "is this an agent?"
		// and "which rules apply?" cannot disagree — the set answers both from the
		// same ids and aliases.
		if name = set.Identify(comm, argv); name != "" {
			manifest = set.Lookup(name)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agent, d.manifest = name, manifest
}

// setOSC records the escape-sequence evidence the osc_* regions read.
func (d *detector) setOSC(title, progress string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if title != "" {
		d.oscTitle = title
	}
	if progress != "" {
		d.oscProgress = progress
	}
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
	// No manifest means the foreground program is not an agent we can classify.
	// The session is then IDLE rather than unknown-and-left-alone: a shell sitting
	// at a prompt, or `less` showing a file, is idle, and the whole point of the
	// per-agent manifests is that we no longer guess from the text alone.
	if d.manifest == nil {
		d.state = stateIdle
		return
	}
	screen := d.renderLocked()
	res := d.manifest.Classify(agentdetect.Input{
		Screen: screen, OSCTitle: d.oscTitle, OSCProgress: d.oscProgress,
	})
	if !res.Matched || res.SkipStateUpdate {
		// Nothing fired, or the winning rule asked us not to move. Either way the
		// committed state stands — this is NOT the same as classifying idle.
		return
	}
	next := stateFromManifest(res.State)
	// A prompt the user already answered (acked, screen unchanged since) is not a
	// fresh block — treat this screen as no longer blocked.
	if next == stateBlocked && d.acked && d.lastHash == d.ackedHash {
		next = stateIdle
	}
	d.state = next
}

// stateFromManifest maps the manifest vocabulary onto the session states the UI
// knows. "unknown" and "done" have no badge of their own; done is a finished
// agent, which reads as idle, and unknown is a positive "cannot tell" that must
// not masquerade as activity.
func stateFromManifest(s agentdetect.State) sessionState {
	switch s {
	case agentdetect.StateBlocked:
		return stateBlocked
	case agentdetect.StateWorking:
		return stateWorking
	default:
		return stateIdle
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
