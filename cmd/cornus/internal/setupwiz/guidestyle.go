package setupwiz

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// guideStyles renders the setup guide and the closing checklist. It is a
// deliberately tiny subset of Markdown — inline code spans and nothing else —
// rather than a real renderer: the guide is a dozen lines of prose and commands,
// and a full Markdown engine would cost nine modules and ~10 MB of syntax
// lexers to typeset them. Every style collapses to the identity when color is
// off, so plain, piped, and NO_COLOR output stays exactly what it was before the
// styling existed.
type guideStyles struct {
	color bool
	title lipgloss.Style
	rule  lipgloss.Style
	num   lipgloss.Style
	code  lipgloss.Style
	dim   lipgloss.Style
}

// newGuideStyles binds the styles to the DRIVER's color decision rather than
// letting lipgloss make its own. That is not a detail: lipgloss's default
// renderer probes stdout, and the guide is written to stderr — so a run whose
// stdout is piped but whose stderr is a terminal would lose its color for a
// reason that has nothing to do with where the text is going. The driver already
// weighed the right stream, NO_COLOR, and --output; this honors that answer.
//
// The forced profile is plain ANSI (16 colors), which is what the color indexes
// below are anyway and what the narrowest terminal still understands.
func newGuideStyles(color bool) *guideStyles {
	if !color {
		r := lipgloss.NewStyle
		return &guideStyles{title: r(), rule: r(), num: r(), code: r(), dim: r()}
	}
	// io.Discard: the writer is only consulted for detection, and detection is
	// exactly what we are overriding. It must be SetColorProfile and not the
	// termenv.WithProfile option — the option is measurably overridden by the
	// renderer's own probe of the writer, which for io.Discard lands on Ascii and
	// silently strips every style.
	rend := lipgloss.NewRenderer(io.Discard)
	rend.SetColorProfile(termenv.ANSI)
	r := rend.NewStyle
	return &guideStyles{
		color: true,
		title: r().Bold(true),
		rule:  r().Faint(true),
		num:   r().Foreground(lipgloss.Color("6")).Bold(true),
		code:  r().Foreground(lipgloss.Color("3")),
		dim:   r().Faint(true),
	}
}

// styles builds the guide's styles from the driver's color decision, tolerating
// a Wizard assembled without a driver (the narrow unit tests do that).
func (w *Wizard) styles() *guideStyles {
	return newGuideStyles(w.d != nil && w.d.Color())
}

// inline renders the inline code spans in s: a `backticked` run is highlighted,
// and the backticks themselves always come off — with color they are replaced by
// the highlight, without color they simply vanish, so no-color output reads as
// the plain sentence it was written as. An unpaired backtick is left alone
// rather than swallowing the rest of the line.
func (g *guideStyles) inline(s string) string {
	if !strings.Contains(s, "`") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "`")
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.Index(s[i+1:], "`")
		if j < 0 {
			b.WriteString(s) // unpaired: emit the remainder verbatim
			break
		}
		b.WriteString(s[:i])
		b.WriteString(g.code.Render(s[i+1 : i+1+j]))
		s = s[i+1+j+1:]
	}
	return b.String()
}

// heading renders a title followed by a rule the width of the title. The rule is
// decoration, so it appears only when we are already decorating; a piped or
// NO_COLOR run gets the bare title it always got.
//
// It deliberately does NOT append a separator: the caller owns the layout, and
// the documentation pointer belongs between this and the blank line that opens
// the body.
func (g *guideStyles) heading(format string, a ...any) []string {
	text := fmt.Sprintf(format, a...)
	lines := []string{g.title.Render(text)}
	if g.color {
		lines = append(lines, g.rule.Render(strings.Repeat("─", lipgloss.Width(text))))
	}
	return lines
}

// steps renders a numbered list: the ordinal accent-colored and right-aligned so
// the text starts at one column however many steps there are, and each step's
// code spans highlighted.
func (g *guideStyles) steps(items []string) []string {
	width := len(fmt.Sprintf("%d", len(items)))
	lines := make([]string, 0, len(items))
	for i, s := range items {
		n := g.num.Render(fmt.Sprintf("%*d", width, i+1))
		lines = append(lines, "  "+n+"  "+g.inline(s))
	}
	return lines
}

// underline marks a URL as followable. Underline is the attribute terminals
// agree means that, and the emulators that linkify output tend to underline what
// they linkified, so it reads the same whether or not the terminal made it
// clickable.
//
// It emits the SGR directly instead of going through lipgloss because lipgloss,
// under the forced ANSI profile, renders underline PER CHARACTER — measured:
// "\x1b[4;4mh\x1b[0m\x1b[4;4mt\x1b[0m…", fifty escape pairs for one URL, and
// UnderlineSpaces(false) does not change it. Besides the bloat, a terminal that
// linkifies by scanning text sees a string interrupted every character. 4 turns
// underline on; 24 turns underline alone off, leaving any surrounding colour be.
func (g *guideStyles) underline(s string) string {
	if !g.color {
		return s
	}
	return "\x1b[4m" + s + "\x1b[24m"
}

// docLine renders the documentation pointer: a dim label and an underlined URL.
// The underline is on the URL alone — the label is not followable, and marking
// it would blur where the thing you can click begins and ends.
func (g *guideStyles) docLine(doc string) string {
	return g.dim.Render("docs: ") + g.underline(doc)
}

// synopsis renders the one-line headline command above the steps. The "$ " is a
// dim prompt marker rather than part of the command, so a reader copying the
// line takes the command and leaves the sigil — the convention every manpage
// and README already uses. Returns nothing when there is no command to run.
func (g *guideStyles) synopsis(cmd string) []string {
	if cmd == "" {
		return nil
	}
	return []string{"  " + g.dim.Render("$ ") + g.code.Render(cmd), ""}
}
