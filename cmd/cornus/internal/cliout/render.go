package cliout

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
)

// noticeKind classifies a one-line notice so each renderer can style it.
type noticeKind int

const (
	noticeStep    noticeKind = iota // an action in progress
	noticeDone                      // an action completed
	noticeSuccess                   // an explicit success
	noticeInfo                      // neutral status
	noticeWarn                      // a warning
	noticeError                     // an error notice (non-fatal)
)

func (k noticeKind) level() string {
	switch k {
	case noticeWarn:
		return "warning"
	case noticeError:
		return "error"
	case noticeSuccess:
		return "success"
	case noticeStep:
		return "step"
	case noticeDone:
		return "done"
	default:
		return "info"
	}
}

// renderer formats each output category to a writer. The Driver owns channel
// routing (stdout vs stderr); the renderer owns formatting only. Three
// implementations back the three modes.
type renderer interface {
	notice(w io.Writer, kind noticeKind, msg string)
	item(w io.Writer, s string)
	table(w io.Writer, headers []string, rows [][]string) error
	kv(w io.Writer, pairs [][2]string) error
	// emit renders a structured result. Human renderers call r.Human via a
	// printer bound to w; the json renderer marshals r instead.
	emit(w io.Writer, r Result) error
	// logLine formats one streamed log line (from a LineWriter) with an optional
	// tag/prefix.
	logLine(w io.Writer, prefix, tag, line string)
}

func rendererFor(mode Mode, color bool) renderer {
	switch mode {
	case ModeFancy:
		return newFancyRenderer(color)
	case ModeJSON:
		return &jsonRenderer{}
	default:
		return &plainRenderer{}
	}
}

// linePrinter adapts a plain io.Writer into a Printer for Result.Human.
type linePrinter struct{ w io.Writer }

func (p linePrinter) Line(format string, a ...any) {
	fmt.Fprintf(p.w, format+"\n", a...)
}

// --- plain ---

type plainRenderer struct{}

func (plainRenderer) notice(w io.Writer, kind noticeKind, msg string) {
	switch kind {
	case noticeWarn:
		fmt.Fprintf(w, "warning: %s\n", msg)
	case noticeError:
		fmt.Fprintf(w, "error: %s\n", msg)
	default:
		fmt.Fprintln(w, msg)
	}
}

func (plainRenderer) item(w io.Writer, s string) { fmt.Fprintln(w, s) }

func (plainRenderer) table(w io.Writer, headers []string, rows [][]string) error {
	return writeTabTable(w, headers, rows)
}

func (plainRenderer) kv(w io.Writer, pairs [][2]string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, p := range pairs {
		fmt.Fprintf(tw, "%s:\t%s\n", p[0], p[1])
	}
	return tw.Flush()
}

func (plainRenderer) emit(w io.Writer, r Result) error {
	r.Human(linePrinter{w})
	return nil
}

func (plainRenderer) logLine(w io.Writer, prefix, _, line string) {
	fmt.Fprintf(w, "%s%s", prefix, line)
}

// --- fancy ---

// fancyRenderer styles output with lipgloss. Unlike the old tabwriter path it
// can color table cells safely, because lipgloss measures display width (via
// rivo/uniseg) rather than byte length — so ANSI escapes don't throw off column
// alignment. Layout survives when color is off (the styles carry Ascii profile),
// keeping fancy visually distinct from plain even under NO_COLOR.
type fancyRenderer struct{ s *styles }

func newFancyRenderer(color bool) *fancyRenderer {
	return &fancyRenderer{s: newStyles(color)}
}

func (f *fancyRenderer) notice(w io.Writer, kind noticeKind, msg string) {
	var sym string
	var st lipgloss.Style
	switch kind {
	case noticeSuccess, noticeDone:
		sym, st = symSuccess, f.s.success
	case noticeStep:
		sym, st = symStep, f.s.step
	case noticeWarn:
		sym, st = symWarn, f.s.warn
	case noticeError:
		sym, st = symFail, f.s.fail
	default:
		sym, st = symInfo, f.s.info
	}
	fmt.Fprintln(w, st.Render(sym)+" "+msg)
}

// item stays uncolored even in fancy mode: a single value is often piped, and a
// bare value is the least surprising thing to hand a caller.
func (f *fancyRenderer) item(w io.Writer, s string) { fmt.Fprintln(w, s) }

// table renders the minimal header-underline layout: a styled header row, a thin
// per-column rule of ─, then the rows — all aligned to the same column widths
// the plain tabwriter would produce (max display width per column, two-space
// gutter, last column not right-padded).
func (f *fancyRenderer) table(w io.Writer, headers []string, rows [][]string) error {
	if len(headers) == 0 && len(rows) == 0 {
		return nil
	}
	widths := columnWidths(headers, rows)

	var b strings.Builder
	if len(headers) > 0 {
		b.WriteString(f.styledRow(headers, widths, f.s.header))
		b.WriteByte('\n')
		rule := make([]string, len(widths))
		for i, wdt := range widths {
			rule[i] = strings.Repeat(ruleRune, wdt)
		}
		b.WriteString(f.styledRow(rule, widths, f.s.rule))
		b.WriteByte('\n')
	}
	for _, row := range rows {
		b.WriteString(f.styledRow(row, widths, lipgloss.Style{}))
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// styledRow pads each cell to its column width (by display width), applies st,
// and joins with a two-space gutter. The final cell is not right-padded, so no
// trailing whitespace escapes into the output.
func (f *fancyRenderer) styledRow(cells []string, widths []int, st lipgloss.Style) string {
	parts := make([]string, len(cells))
	for i, cell := range cells {
		s := cell
		if i < len(cells)-1 && i < len(widths) {
			if pad := widths[i] - lipgloss.Width(cell); pad > 0 {
				s = cell + strings.Repeat(" ", pad)
			}
		}
		parts[i] = st.Render(s)
	}
	return strings.Join(parts, "  ")
}

// columnWidths returns the max display width of each column across the header
// and all rows.
func columnWidths(headers []string, rows [][]string) []int {
	n := len(headers)
	for _, r := range rows {
		if len(r) > n {
			n = len(r)
		}
	}
	widths := make([]int, n)
	consider := func(cells []string) {
		for i, c := range cells {
			if wdt := lipgloss.Width(c); wdt > widths[i] {
				widths[i] = wdt
			}
		}
	}
	if len(headers) > 0 {
		consider(headers)
	}
	for _, r := range rows {
		consider(r)
	}
	return widths
}

// kv renders a bold key and its value, keys aligned to a common width.
func (f *fancyRenderer) kv(w io.Writer, pairs [][2]string) error {
	keyw := 0
	for _, p := range pairs {
		if wdt := lipgloss.Width(p[0]); wdt > keyw {
			keyw = wdt
		}
	}
	var b strings.Builder
	for _, p := range pairs {
		pad := keyw - lipgloss.Width(p[0])
		fmt.Fprintf(&b, "%s%s  %s\n", f.s.key.Render(p[0]+":"), strings.Repeat(" ", pad), p[1])
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (f *fancyRenderer) emit(w io.Writer, r Result) error {
	r.Human(linePrinter{w})
	return nil
}

func (f *fancyRenderer) logLine(w io.Writer, prefix, tag, line string) {
	fmt.Fprintf(w, "%s%s", f.s.logTagStyle(tag).Render(prefix), line)
}

// --- json ---

type jsonRenderer struct{}

func (jsonRenderer) notice(w io.Writer, kind noticeKind, msg string) {
	writeJSON(w, map[string]string{"level": kind.level(), "msg": msg})
}

func (jsonRenderer) item(w io.Writer, s string) {
	writeJSON(w, map[string]string{"value": s})
}

// table emits one JSON object per row, keyed by header — the most script-
// friendly NDJSON shape.
func (jsonRenderer) table(w io.Writer, headers []string, rows [][]string) error {
	for _, row := range rows {
		obj := make(map[string]string, len(headers))
		for i, h := range headers {
			if i < len(row) {
				obj[h] = row[i]
			}
		}
		if err := writeJSON(w, obj); err != nil {
			return err
		}
	}
	return nil
}

func (jsonRenderer) kv(w io.Writer, pairs [][2]string) error {
	obj := make(map[string]string, len(pairs))
	for _, p := range pairs {
		obj[p[0]] = p[1]
	}
	return writeJSON(w, obj)
}

func (jsonRenderer) emit(w io.Writer, r Result) error {
	return writeJSON(w, r)
}

func (jsonRenderer) logLine(w io.Writer, _, tag, line string) {
	writeJSON(w, map[string]string{"type": "log", "tag": tag, "line": line})
}

// writeJSON marshals v and writes it as one NDJSON line.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// writeTabTable renders an aligned table via text/tabwriter, reproducing the
// look the CLI's ad-hoc tabwriter sites use today.
func writeTabTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if len(headers) > 0 {
		writeRow(tw, headers)
	}
	for _, row := range rows {
		writeRow(tw, row)
	}
	return tw.Flush()
}

func writeRow(w io.Writer, cells []string) {
	for i, c := range cells {
		if i > 0 {
			fmt.Fprint(w, "\t")
		}
		fmt.Fprint(w, c)
	}
	fmt.Fprintln(w)
}
