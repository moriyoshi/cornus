package cliout

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// styles is the fancy renderer's palette, built once from the resolved color
// flag. It owns a lipgloss.Renderer whose color profile is set explicitly
// (ANSI256 when color is on, Ascii when off) rather than auto-detected from a
// stream, so rendering is deterministic even when the driver writes to a
// bytes.Buffer in tests. When color is off the styles still carry their glyphs
// and layout — only the SGR color is dropped — so fancy stays "fancy" under
// NO_COLOR (borders, symbols, alignment) without any ANSI.
type styles struct {
	// noticeStyle/noticeSym give each notice kind its color and leading glyph.
	success lipgloss.Style
	step    lipgloss.Style
	info    lipgloss.Style
	warn    lipgloss.Style
	fail    lipgloss.Style

	// header/rule style a table's header row and the thin underline beneath it;
	// key styles a KV block's key.
	header lipgloss.Style
	rule   lipgloss.Style
	key    lipgloss.Style

	// logTags is a small stable palette; a streamed-log prefix picks a color by
	// hashing its tag, so each service in a multi-service stream stays visually
	// distinct (docker-compose style).
	logTags []lipgloss.Style
}

// notice glyphs. Kept as plain runes so they render with or without color.
const (
	symSuccess = "✓"
	symStep    = "▸"
	symInfo    = "•"
	symWarn    = "⚠"
	symFail    = "✗"
	// ruleRune draws the per-column header underline.
	ruleRune = "─"
)

// Standard ANSI color indices (stable across terminals; lipgloss maps them to
// the resolved profile). Kept as names so the intent is legible.
const (
	colGreen   = "2"
	colYellow  = "3"
	colBlue    = "4"
	colMagenta = "5"
	colCyan    = "6"
	colRed     = "1"
	colGray    = "8" // bright black — used for the dim header rule
)

func newStyles(color bool) *styles {
	r := lipgloss.NewRenderer(io.Discard)
	if color {
		r.SetColorProfile(termenv.ANSI256)
	} else {
		r.SetColorProfile(termenv.Ascii)
	}

	c := func(code string) lipgloss.Style { return r.NewStyle().Foreground(lipgloss.Color(code)) }

	s := &styles{
		success: c(colGreen),
		step:    c(colCyan),
		info:    r.NewStyle().Faint(true),
		warn:    c(colYellow),
		fail:    c(colRed),
		header:  c(colCyan).Bold(true),
		rule:    r.NewStyle().Faint(true),
		key:     r.NewStyle().Bold(true),
	}
	// A distinguishable rotation for per-service log prefixes.
	for _, code := range []string{colCyan, colGreen, colYellow, colMagenta, colBlue, colRed} {
		s.logTags = append(s.logTags, c(code).Faint(true))
	}
	return s
}

// logTagStyle picks a stable color for tag by hashing it, so the same service
// keeps the same color for the life of the process.
func (s *styles) logTagStyle(tag string) lipgloss.Style {
	if len(s.logTags) == 0 {
		return lipgloss.NewStyle()
	}
	var h uint32 = 2166136261
	for i := 0; i < len(tag); i++ { // FNV-1a
		h ^= uint32(tag[i])
		h *= 16777619
	}
	return s.logTags[int(h)%len(s.logTags)]
}
