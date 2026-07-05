package setupwiz

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"cornus/cmd/cornus/internal/cliout"
)

// teaUI is the rich UI: one short-lived tea.Program per question (not a
// whole-flow model, so the imperative flow in wizard.go stays the single source
// of truth shared with plainUI). Each program renders to stderr, reads keys from
// the driver's stdin, and disables the built-in signal handler so Ctrl-C arrives
// as a KeyMsg the model turns into ErrAborted. After each answer a permanent
// transcript line is printed to stderr so the live region only ever shows the
// current question; secret answers are masked in the transcript.
type teaUI struct {
	d *cliout.Driver
	s *teaStyles
}

func newTeaUI(d *cliout.Driver) *teaUI {
	return &teaUI{d: d, s: newTeaStyles(d.Color())}
}

// run drives one model to completion. It mirrors cliout.startLiveProgress's
// program construction (WithOutput to stderr, WithoutSignalHandler) but, unlike
// the output-only progress program, wires stdin so the model can read keys.
func (u *teaUI) run(m tea.Model) (tea.Model, error) {
	prog := tea.NewProgram(m,
		tea.WithOutput(u.d.Err()),
		tea.WithInput(u.d.Stdin()),
		tea.WithoutSignalHandler(),
	)
	return prog.Run()
}

// transcript prints the permanent record of an answered question to stderr.
func (u *teaUI) transcript(label, value string) {
	fmt.Fprintf(u.d.Err(), "%s %s: %s\n", "✓", label, value)
}

func (u *teaUI) Note(format string, a ...any) {
	fmt.Fprintf(u.d.Err(), format+"\n", a...)
}

func (u *teaUI) Select(title, help string, opts []Option, def int) (int, error) {
	res, err := u.run(newSelectModel(u.s, title, help, opts, def))
	if err != nil {
		return 0, err
	}
	m := res.(*selectModel)
	if m.aborted {
		return 0, ErrAborted
	}
	if m.back {
		return 0, ErrBack
	}
	u.transcript(title, opts[m.chosen].Label)
	return m.chosen, nil
}

func (u *teaUI) Input(q Question) (string, error) {
	res, err := u.run(newInputModel(u.s, q))
	if err != nil {
		return "", err
	}
	m := res.(*inputModel)
	if m.aborted {
		return "", ErrAborted
	}
	if m.back {
		return "", ErrBack
	}
	shown := m.value
	if q.Secret {
		shown = "********"
	}
	u.transcript(q.Title, shown)
	return m.value, nil
}

func (u *teaUI) Confirm(question string, defaultYes bool) (bool, error) {
	res, err := u.run(newConfirmModel(u.s, question, defaultYes))
	if err != nil {
		return false, err
	}
	m := res.(*confirmModel)
	if m.aborted {
		return false, ErrAborted
	}
	if m.back {
		return false, ErrBack
	}
	ans := "no"
	if m.value {
		ans = "yes"
	}
	u.transcript(question, ans)
	return m.value, nil
}
