package cliout

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// Progress is a live, multi-task progress display. In fancy mode on a real
// terminal it renders an in-place region on stderr — one animated spinner per
// active Task, plus an optional overall progress bar — and any notice or
// streamed log line the driver emits while it is running is printed cleanly
// *above* the region (via the driver's activeProg hook). In every other mode
// (plain, json, or fancy without a TTY) it is a silent no-op overlay: callers
// keep emitting their normal append-only notices/events, which carry the output.
//
// This split is deliberate. Live progress is only ever active when fancy mode
// resolved, which requires both stdout and stderr to be TTYs — so the in-place
// region can never corrupt a pipe. Everywhere else the ordinary text path is
// the source of truth, so wrapping a flow in a Progress adds animation on a
// terminal without changing scripted/piped output at all.
type Progress struct {
	d      *Driver
	live   *liveProgress // nil unless fancy+TTY
	nextID atomic.Int64
}

// Task is a single unit of work within a Progress (e.g. one build step, or one
// service coming up). Its methods are no-ops when the Progress is not live.
type Task struct {
	p  *Progress
	id int64
}

// Progress starts a progress display bound to this driver. Call Stop (usually
// deferred) to tear it down. Task/SetFraction are safe to call concurrently.
func (d *Driver) Progress() *Progress {
	p := &Progress{d: d}
	if d.liveProgressEligible() {
		p.live = startLiveProgress(d)
	}
	return p
}

// liveProgressEligible reports whether a Progress started now would render a live
// in-place region. That needs fancy mode on a real stderr terminal AND the
// ProgressStatus style: the ProgressStream preference (a user asking for a
// scrolling append-only log via --progress / CORNUS_PROGRESS) suppresses the live
// region even on a capable terminal, so callers fall back to their notice/event
// lines. Kept as a pure predicate so the gating is unit-testable without starting
// a bubbletea program against a buffer.
func (d *Driver) liveProgressEligible() bool {
	return d.mode == ModeFancy && d.errTTY && d.progressStyle == ProgressStatus
}

// ProgressStyle selects how a live Progress renders its tasks. It is a
// user-facing preference (the compose `--progress` flag / CORNUS_PROGRESS env),
// orthogonal to the plain/fancy/json Mode: Mode decides whether ANSI and a live
// region are possible at all (a pipe is always plain), while ProgressStyle
// decides, when a live region IS possible (fancy on a TTY), whether per-task
// status collapses into one mutating line each or streams as append-only lines.
type ProgressStyle int

const (
	// ProgressStatus collapses each task into a single in-place line that mutates
	// as the task progresses (the default): one evolving "<service>  <state>" row
	// per service, the way `docker compose up` shows status.
	ProgressStatus ProgressStyle = iota
	// ProgressStream renders no live region even on a fancy TTY, so callers fall
	// back to their append-only notice/event lines — every status change is its
	// own line, preserving a full scrollback log.
	ProgressStream
)

// ParseProgressStyle resolves a --progress / CORNUS_PROGRESS value to a
// ProgressStyle. An empty value is the default (ProgressStatus). The bool is
// false for an unrecognized value, so a flag can reject it while environment
// resolution silently falls back to the default.
func ParseProgressStyle(s string) (ProgressStyle, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "status":
		return ProgressStatus, true
	case "stream":
		return ProgressStream, true
	default:
		return ProgressStatus, false
	}
}

// Task adds a task with an initial label and returns a handle to update it.
func (p *Progress) Task(label string) *Task {
	id := p.nextID.Add(1)
	if p.live != nil {
		p.live.send(addTaskMsg{id: id, label: label})
	}
	return &Task{p: p, id: id}
}

// SetFraction sets the overall progress bar to f in [0,1] and makes it visible.
// A no-op when not live.
func (p *Progress) SetFraction(f float64) {
	if p.live != nil {
		p.live.send(fractionMsg{f: clampFraction(f)})
	}
}

// Live reports whether this Progress is actually rendering a live region
// (fancy mode on a real terminal). Callers that only have a cheaper, non-live
// fallback for their live rendering (e.g. a one-line-per-task build summary)
// use this to pick between them, since Task/Update/Done/Fail are silent no-ops
// when it is false.
func (p *Progress) Live() bool { return p.live != nil }

// Stop tears down the display, flushing any remaining live region. Idempotent.
func (p *Progress) Stop() {
	if p.live != nil {
		p.live.stop()
		p.live = nil
	}
}

// Update changes the task's label (the text beside its spinner).
func (t *Task) Update(label string) {
	if t.p.live != nil {
		t.p.live.send(updateTaskMsg{id: t.id, label: label})
	}
}

// Done marks the task finished successfully; msg (if non-empty) is printed as a
// permanent ✓ line above the live region and the spinner is removed.
func (t *Task) Done(msg string) { t.finish(msg, true) }

// Fail marks the task failed; msg is printed as a permanent ✗ line.
func (t *Task) Fail(msg string) { t.finish(msg, false) }

func (t *Task) finish(msg string, ok bool) {
	if t.p.live != nil {
		t.p.live.send(finishTaskMsg{id: t.id, msg: msg, ok: ok})
	}
}

func clampFraction(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}

// --- live backend (bubbletea) ---

// liveProgress owns the running tea.Program and the goroutine driving it. It
// implements progressProgram so the driver can print notices/logs above the
// region while it is active.
type liveProgress struct {
	d        *Driver
	prog     *tea.Program
	done     chan struct{}
	stopOnce sync.Once
}

func startLiveProgress(d *Driver) *liveProgress {
	m := newProgressModel(d)
	prog := tea.NewProgram(m,
		tea.WithOutput(d.err),
		tea.WithInput(nil),         // output-only: never touch stdin (keeps Confirm working)
		tea.WithoutSignalHandler(), // the CLI owns signal handling
	)
	lp := &liveProgress{d: d, prog: prog, done: make(chan struct{})}
	d.setActiveProgram(lp)
	go func() {
		defer close(lp.done)
		prog.Run() //nolint:errcheck // a render error just ends the live display
	}()
	return lp
}

// send posts a message to the program. tea.Program.Send is ctx-aware, so after
// the program has quit this drops the message instead of blocking forever (the
// raw Program.Println would deadlock on the unbuffered channel).
func (lp *liveProgress) send(msg tea.Msg) { lp.prog.Send(msg) }

// printAbove routes an already-rendered line above the live region. It goes
// through Send (not Program.Println) for the same deadlock-safety reason.
func (lp *liveProgress) printAbove(line string) { lp.prog.Send(printAboveMsg{line: line}) }

func (lp *liveProgress) stop() {
	lp.stopOnce.Do(func() {
		lp.prog.Quit()
		<-lp.done
		lp.prog.Wait()
		// Clear only after the program has fully stopped and restored the
		// terminal, so notices/logs emitted during teardown still print above the
		// region (Send stays deadlock-safe: it drops once the program's ctx is
		// cancelled).
		lp.d.clearActiveProgram(lp)
	})
}

// Messages the model understands.
type (
	addTaskMsg struct {
		id    int64
		label string
	}
	updateTaskMsg struct {
		id    int64
		label string
	}
	finishTaskMsg struct {
		id  int64
		msg string
		ok  bool
	}
	fractionMsg   struct{ f float64 }
	printAboveMsg struct{ line string }
)

type taskState struct {
	id      int64
	label   string
	spinner spinner.Model
}

type progressModel struct {
	d       *Driver
	s       *styles
	tasks   []*taskState
	bar     progress.Model
	frac    float64
	showBar bool
}

func newProgressModel(d *Driver) *progressModel {
	return &progressModel{
		d:   d,
		s:   newStyles(d.color),
		bar: progress.New(progress.WithWidth(30), progress.WithoutPercentage()),
	}
}

func (m *progressModel) Init() tea.Cmd { return nil }

func (m *progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case addTaskMsg:
		sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(m.s.step))
		ts := &taskState{id: msg.id, label: msg.label, spinner: sp}
		m.tasks = append(m.tasks, ts)
		return m, ts.spinner.Tick

	case updateTaskMsg:
		if ts := m.task(msg.id); ts != nil {
			ts.label = msg.label
		}
		return m, nil

	case finishTaskMsg:
		m.remove(msg.id)
		if msg.msg == "" {
			return m, nil
		}
		sym, st := symSuccess, m.s.success
		if !msg.ok {
			sym, st = symFail, m.s.fail
		}
		return m, tea.Println(st.Render(sym) + " " + msg.msg)

	case fractionMsg:
		m.frac, m.showBar = msg.f, true
		return m, nil

	case printAboveMsg:
		return m, tea.Println(msg.line)

	case spinner.TickMsg:
		// Route the tick to every spinner; only the one whose id matches advances
		// and returns the next tick.
		var cmds []tea.Cmd
		for _, ts := range m.tasks {
			var cmd tea.Cmd
			ts.spinner, cmd = ts.spinner.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m *progressModel) View() string {
	if len(m.tasks) == 0 && !m.showBar {
		return ""
	}
	var b strings.Builder
	if m.showBar {
		fmt.Fprintf(&b, "%s %s\n", m.bar.ViewAs(m.frac), m.s.info.Render(fmt.Sprintf("%3.0f%%", m.frac*100)))
	}
	for i, ts := range m.tasks {
		if i > 0 || m.showBar {
			b.WriteByte('\n')
		}
		b.WriteString(ts.spinner.View())
		b.WriteString(" ")
		b.WriteString(ts.label)
	}
	return b.String()
}

func (m *progressModel) task(id int64) *taskState {
	for _, ts := range m.tasks {
		if ts.id == id {
			return ts
		}
	}
	return nil
}

func (m *progressModel) remove(id int64) {
	for i, ts := range m.tasks {
		if ts.id == id {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			return
		}
	}
}

// --- driver <-> live-program coupling ---

// progressProgram is the driver's view of a live progress program: the one
// operation the notice/log hooks need. Kept as an interface so driver.go need
// not import bubbletea.
type progressProgram interface {
	printAbove(line string)
}

func (d *Driver) setActiveProgram(p progressProgram) {
	d.progMu.Lock()
	d.activeProg = p
	d.progMu.Unlock()
}

func (d *Driver) clearActiveProgram(p progressProgram) {
	d.progMu.Lock()
	if d.activeProg == p {
		d.activeProg = nil
	}
	d.progMu.Unlock()
}

func (d *Driver) activeProgram() progressProgram {
	d.progMu.Lock()
	defer d.progMu.Unlock()
	return d.activeProg
}

// routeAbove renders one output line (via render, into a scratch builder) and,
// if a live progress program owns the terminal, prints it above the live region
// and reports true. When no program is active it reports false so the caller
// writes to its normal channel. A fresh builder per call keeps it safe under the
// concurrent notice/log writers the driver already serializes elsewhere.
func (d *Driver) routeAbove(render func(w *strings.Builder)) bool {
	p := d.activeProgram()
	if p == nil {
		return false
	}
	var b strings.Builder
	render(&b)
	p.printAbove(strings.TrimRight(b.String(), "\n"))
	return true
}
