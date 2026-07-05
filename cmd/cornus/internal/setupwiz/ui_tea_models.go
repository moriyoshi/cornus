package setupwiz

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// teaStyles is the small style set the wizard's per-question models use. Color is
// off in tests and when the driver reports no color, so View() output is plain.
type teaStyles struct {
	color  bool
	title  lipgloss.Style
	help   lipgloss.Style
	cursor lipgloss.Style
	errSty lipgloss.Style
	key    lipgloss.Style
}

func newTeaStyles(color bool) *teaStyles {
	r := lipgloss.NewStyle
	if !color {
		return &teaStyles{title: r(), help: r(), cursor: r(), errSty: r(), key: r()}
	}
	return &teaStyles{
		color:  true,
		title:  r().Bold(true),
		help:   r().Faint(true),
		cursor: r().Foreground(lipgloss.Color("6")),
		errSty: r().Foreground(lipgloss.Color("1")),
		key:    r().Foreground(lipgloss.Color("6")).Bold(true),
	}
}

// legend renders a footer key hint. Each item is {mnemonic, symbol, description};
// the mnemonic and description are faint, and the Unicode key symbol is
// accent-colored. A symbol paired with a word mnemonic (e.g. "enter" + ⏎) is
// redundant, so it is dropped in no-color output. A symbol that is the sole key
// indicator (no word mnemonic, e.g. ↑/↓ or y/n) is always shown — accent-colored
// with color, faint without — so the hint is never lost.
// renderKey accent-colors a key symbol, but keeps any "/" separators faint, so a
// multi-key hint like "↑/↓" or "y/n" colors only the glyphs, not the slash.
func (s *teaStyles) renderKey(symbol string) string {
	parts := strings.Split(symbol, "/")
	for i, p := range parts {
		parts[i] = s.key.Render(p)
	}
	return strings.Join(parts, s.help.Render("/"))
}

func (s *teaStyles) legend(items ...[3]string) string {
	var b strings.Builder
	b.WriteString("  ")
	for i, it := range items {
		if i > 0 {
			b.WriteString(s.help.Render(" · "))
		}
		mnemonic, symbol, desc := it[0], it[1], it[2]
		wrote := false
		if mnemonic != "" {
			b.WriteString(s.help.Render(mnemonic))
			wrote = true
		}
		if symbol != "" {
			switch {
			case s.color:
				if wrote {
					b.WriteString(" ")
				}
				b.WriteString(s.renderKey(symbol))
				wrote = true
			case mnemonic == "":
				b.WriteString(s.help.Render(symbol))
				wrote = true
			}
		}
		if desc != "" {
			if wrote {
				b.WriteString(s.help.Render(" "))
			}
			b.WriteString(s.help.Render(desc))
		}
	}
	return b.String()
}

// selectModel is a hand-rolled single-choice cursor picker (kept small — the
// wizard's selects are all <= 6 items — rather than pulling in bubbles/list).
type selectModel struct {
	s       *teaStyles
	title   string
	help    string
	opts    []Option
	cursor  int
	chosen  int
	done    bool
	aborted bool
	back    bool
}

func newSelectModel(s *teaStyles, title, help string, opts []Option, def int) *selectModel {
	if def < 0 || def >= len(opts) {
		def = 0
	}
	return &selectModel{s: s, title: title, help: help, opts: opts, cursor: def}
}

func (m *selectModel) Init() tea.Cmd { return nil }

func (m *selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			m.aborted = true
			return m, tea.Quit
		case "esc", "ctrl+d":
			m.back = true
			return m, tea.Quit
		case "up", "k", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j", "ctrl+n":
			if m.cursor < len(m.opts)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = m.cursor
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *selectModel) View() string {
	var b strings.Builder
	b.WriteString(m.s.title.Render(m.title))
	b.WriteByte('\n')
	if m.help != "" {
		b.WriteString(m.s.help.Render("  " + m.help))
		b.WriteByte('\n')
	}
	for i, o := range m.opts {
		cursor := "  "
		label := o.Label
		if i == m.cursor {
			cursor = m.s.cursor.Render("> ")
			label = m.s.cursor.Render(label)
		}
		b.WriteString(cursor)
		b.WriteString(label)
		if o.Desc != "" {
			b.WriteString(m.s.help.Render(" — " + o.Desc))
		}
		b.WriteByte('\n')
	}
	b.WriteString(m.s.legend([3]string{"", "↑/↓", "move"}, [3]string{"enter", "⏎", "select"}, [3]string{"esc", "⎋", "back"}, [3]string{"ctrl+c", "⌃C", "quit"}))
	b.WriteByte('\n')
	return b.String()
}

// inputModel wraps bubbles/textinput. Secret maps to the password echo mode;
// validation runs on Enter (empty resolves to the default first), and a failing
// validation is shown inline without leaving the prompt.
type inputModel struct {
	s       *teaStyles
	ti      textinput.Model
	q       Question
	err     error
	value   string
	done    bool
	aborted bool
	back    bool
}

func newInputModel(s *teaStyles, q Question) *inputModel {
	ti := textinput.New()
	ti.Prompt = "> "
	// The placeholder ghost text is the example; a functional default falls back
	// into it when a prompt has no dedicated example.
	ti.Placeholder = q.Example
	if ti.Placeholder == "" {
		ti.Placeholder = q.Default
	}
	if q.Secret {
		ti.EchoMode = textinput.EchoPassword
	}
	ti.Focus()
	return &inputModel{s: s, ti: ti, q: q}
}

func (m *inputModel) Init() tea.Cmd { return textinput.Blink }

func (m *inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			m.aborted = true
			return m, tea.Quit
		case "esc", "ctrl+d":
			m.back = true
			return m, tea.Quit
		case "enter":
			val := strings.TrimSpace(m.ti.Value())
			if val == "" {
				val = m.q.Default
			}
			if m.q.Validate != nil {
				if err := m.q.Validate(val); err != nil {
					m.err = err
					return m, nil
				}
			}
			m.value = val
			m.done = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

func (m *inputModel) View() string {
	var b strings.Builder
	b.WriteString(m.s.title.Render(m.q.Title))
	b.WriteByte('\n')
	if m.q.Help != "" {
		b.WriteString(m.s.help.Render("  " + m.q.Help))
		b.WriteByte('\n')
	}
	b.WriteString(m.ti.View())
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString(m.s.errSty.Render(fmt.Sprintf("  invalid: %v", m.err)))
		b.WriteByte('\n')
	}
	b.WriteString(m.s.legend([3]string{"enter", "⏎", "submit"}, [3]string{"esc", "⎋", "back"}, [3]string{"ctrl+c", "⌃C", "quit"}))
	b.WriteByte('\n')
	return b.String()
}

// confirmModel is a [Y/n] prompt; Enter takes the default, y/n choose explicitly.
type confirmModel struct {
	s          *teaStyles
	question   string
	defaultYes bool
	value      bool
	done       bool
	aborted    bool
	back       bool
}

func newConfirmModel(s *teaStyles, question string, defaultYes bool) *confirmModel {
	return &confirmModel{s: s, question: question, defaultYes: defaultYes}
}

func (m *confirmModel) Init() tea.Cmd { return nil }

func (m *confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			m.aborted = true
			return m, tea.Quit
		case "esc", "ctrl+d":
			m.back = true
			return m, tea.Quit
		case "y", "Y":
			m.value = true
			m.done = true
			return m, tea.Quit
		case "n", "N":
			m.value = false
			m.done = true
			return m, tea.Quit
		case "enter":
			m.value = m.defaultYes
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *confirmModel) View() string {
	suffix := "[y/N]"
	if m.defaultYes {
		suffix = "[Y/n]"
	}
	return m.s.title.Render(m.question) + " " + suffix + "\n" +
		m.s.legend([3]string{"", "y/n", "choose"}, [3]string{"enter", "⏎", "default"}, [3]string{"esc", "⎋", "back"}, [3]string{"ctrl+c", "⌃C", "quit"}) + "\n"
}
