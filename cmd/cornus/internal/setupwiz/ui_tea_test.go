package setupwiz

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }
func runes(s string) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func testStyles() *teaStyles       { return newTeaStyles(false) }

func TestSelectModelCursorAndChoose(t *testing.T) {
	m := newSelectModel(testStyles(), "pick", "", threeOpts, 0)
	m.Update(key(tea.KeyDown))
	m.Update(key(tea.KeyDown))
	m.Update(key(tea.KeyUp)) // 0 -> 1 -> 2 -> 1
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	_, cmd := m.Update(key(tea.KeyEnter))
	if !m.done || m.chosen != 1 || cmd == nil {
		t.Fatalf("after enter: done=%v chosen=%d cmd=%v", m.done, m.chosen, cmd)
	}
	if v := m.View(); !strings.Contains(v, "pick") || !strings.Contains(v, "> ") {
		t.Errorf("View missing title or cursor: %q", v)
	}
}

func TestSelectModelClampAndAbort(t *testing.T) {
	m := newSelectModel(testStyles(), "pick", "", threeOpts, 0)
	m.Update(key(tea.KeyUp)) // clamp at 0
	if m.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.cursor)
	}
	m.Update(key(tea.KeyCtrlC))
	if !m.aborted {
		t.Error("ctrl+c should abort")
	}
}

func TestInputModelTypeAndSubmit(t *testing.T) {
	m := newInputModel(testStyles(), Question{Title: "name"})
	m.Update(runes("hi"))
	m.Update(key(tea.KeyEnter))
	if !m.done || m.value != "hi" {
		t.Fatalf("value=%q done=%v", m.value, m.done)
	}
}

func TestInputModelDefaultAndValidate(t *testing.T) {
	// Empty submit resolves to the default.
	m := newInputModel(testStyles(), Question{Title: "n", Default: "dflt"})
	m.Update(key(tea.KeyEnter))
	if m.value != "dflt" {
		t.Errorf("empty submit = %q, want default", m.value)
	}

	// A failing validation blocks submit and shows the error, then a valid value
	// goes through.
	calls := 0
	m2 := newInputModel(testStyles(), Question{Title: "n", Validate: func(s string) error {
		calls++
		if s != "ok" {
			return errStub
		}
		return nil
	}})
	m2.Update(key(tea.KeyEnter)) // empty -> invalid
	if m2.done || m2.err == nil {
		t.Fatalf("invalid submit should not finish: done=%v err=%v", m2.done, m2.err)
	}
	if !strings.Contains(m2.View(), "invalid") {
		t.Errorf("View should show the validation error: %q", m2.View())
	}
	m2.Update(runes("ok"))
	m2.Update(key(tea.KeyEnter))
	if !m2.done || m2.value != "ok" {
		t.Fatalf("valid submit: done=%v value=%q", m2.done, m2.value)
	}
}

func TestInputModelSecretBackAndAbort(t *testing.T) {
	m := newInputModel(testStyles(), Question{Title: "token", Secret: true})
	if m.ti.EchoMode != textinput.EchoPassword {
		t.Error("secret question should use the password echo mode")
	}
	// Esc goes back one step; Ctrl-C aborts the wizard. They are distinct.
	m.Update(key(tea.KeyEsc))
	if !m.back || m.aborted {
		t.Errorf("esc should go back, not abort: back=%v aborted=%v", m.back, m.aborted)
	}
	m2 := newInputModel(testStyles(), Question{Title: "token", Secret: true})
	m2.Update(key(tea.KeyCtrlC))
	if !m2.aborted || m2.back {
		t.Errorf("ctrl+c should abort, not go back: aborted=%v back=%v", m2.aborted, m2.back)
	}
}

func TestModelsEscGoesBack(t *testing.T) {
	sm := newSelectModel(testStyles(), "pick", "", threeOpts, 0)
	sm.Update(key(tea.KeyEsc))
	if !sm.back || sm.aborted {
		t.Errorf("select esc: back=%v aborted=%v", sm.back, sm.aborted)
	}
	cm := newConfirmModel(testStyles(), "ok?", false)
	cm.Update(key(tea.KeyEsc))
	if !cm.back || cm.aborted {
		t.Errorf("confirm esc: back=%v aborted=%v", cm.back, cm.aborted)
	}
}

func TestConfirmModel(t *testing.T) {
	y := newConfirmModel(testStyles(), "ok?", false)
	y.Update(runes("y"))
	if !y.done || !y.value {
		t.Errorf("y: done=%v value=%v", y.done, y.value)
	}

	n := newConfirmModel(testStyles(), "ok?", true)
	n.Update(runes("n"))
	if !n.done || n.value {
		t.Errorf("n: done=%v value=%v", n.done, n.value)
	}

	d := newConfirmModel(testStyles(), "ok?", true)
	d.Update(key(tea.KeyEnter))
	if !d.done || !d.value {
		t.Errorf("enter should take the default (true): done=%v value=%v", d.done, d.value)
	}
	if !strings.Contains(d.View(), "[Y/n]") {
		t.Errorf("View should show the default marker: %q", d.View())
	}

	a := newConfirmModel(testStyles(), "ok?", false)
	a.Update(key(tea.KeyCtrlC))
	if !a.aborted {
		t.Error("ctrl+c should abort")
	}
}

func TestLegendSymbolColorGating(t *testing.T) {
	// No-color mode omits the Unicode key symbol but keeps mnemonic + description.
	plain := newTeaStyles(false).legend([3]string{"esc", "⎋", "back"})
	if strings.Contains(plain, "⎋") {
		t.Errorf("plain legend should omit the symbol: %q", plain)
	}
	if !strings.Contains(plain, "esc") || !strings.Contains(plain, "back") {
		t.Errorf("plain legend should keep mnemonic and description: %q", plain)
	}
	// Color mode includes the symbol (it is accent-colored via the key style).
	if colored := newTeaStyles(true).legend([3]string{"esc", "⎋", "back"}); !strings.Contains(colored, "⎋") {
		t.Errorf("color legend should include the symbol: %q", colored)
	}

	// A sole key indicator (empty mnemonic, e.g. arrows) is shown in both modes,
	// so the hint is never lost — only redundant paired symbols are dropped.
	for _, color := range []bool{false, true} {
		got := newTeaStyles(color).legend([3]string{"", "↑/↓", "move"})
		if !strings.Contains(got, "↑/↓") || !strings.Contains(got, "move") {
			t.Errorf("sole-indicator legend (color=%v) should keep the keys and description: %q", color, got)
		}
	}
}

func TestSelectModelEmacsCursor(t *testing.T) {
	m := newSelectModel(testStyles(), "pick", "", threeOpts, 0)
	m.Update(key(tea.KeyCtrlN)) // down: 0 -> 1
	m.Update(key(tea.KeyCtrlN)) // down: 1 -> 2
	if m.cursor != 2 {
		t.Fatalf("ctrl+n cursor = %d, want 2", m.cursor)
	}
	m.Update(key(tea.KeyCtrlP)) // up: 2 -> 1
	if m.cursor != 1 {
		t.Fatalf("ctrl+p cursor = %d, want 1", m.cursor)
	}
}

func TestModelsCtrlDGoesBack(t *testing.T) {
	im := newInputModel(testStyles(), Question{Title: "x"})
	im.Update(key(tea.KeyCtrlD))
	if !im.back || im.aborted {
		t.Errorf("input ctrl+d: back=%v aborted=%v", im.back, im.aborted)
	}
	sm := newSelectModel(testStyles(), "pick", "", threeOpts, 0)
	sm.Update(key(tea.KeyCtrlD))
	if !sm.back || sm.aborted {
		t.Errorf("select ctrl+d: back=%v aborted=%v", sm.back, sm.aborted)
	}
	cm := newConfirmModel(testStyles(), "ok?", false)
	cm.Update(key(tea.KeyCtrlD))
	if !cm.back || cm.aborted {
		t.Errorf("confirm ctrl+d: back=%v aborted=%v", cm.back, cm.aborted)
	}
}

func TestInputModelExamplePlaceholder(t *testing.T) {
	m := newInputModel(testStyles(), Question{Title: "Server URL", Example: "https://cornus.example.com"})
	if m.ti.Placeholder != "https://cornus.example.com" {
		t.Errorf("placeholder = %q, want the example", m.ti.Placeholder)
	}
	// A functional default fills the placeholder when there is no example.
	m2 := newInputModel(testStyles(), Question{Title: "Namespace", Default: "default"})
	if m2.ti.Placeholder != "default" {
		t.Errorf("placeholder = %q, want the default fallback", m2.ti.Placeholder)
	}
}
