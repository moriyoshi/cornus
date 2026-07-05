// Package agentdetect classifies what a terminal session's foreground program is
// doing — working, idle, or blocked waiting for a human — using the herdr
// agent-detection manifests vendored under third_party/herdr.
//
// It is a Go reimplementation of herdr's detection semantics, deliberately
// faithful rather than inspired: the manifests are third-party data refreshed
// from upstream as agent UIs change, so anything this evaluates differently is a
// bug that will surface as a misclassification nobody can explain from the TOML.
// Where a choice looked arbitrary it was taken from src/detect/manifest.rs rather
// than reasoned about — the priority tie-break and the `any`-only-when-non-empty
// rule especially.
//
// Two things make this work where the previous rule set did not:
//
//   - REGIONS. Every rule names the part of the screen it reads (a prompt box
//     body, the last N non-empty lines, the window title). Cornus's own rules
//     matched one fixed region, which is why `[y/n]` in `git log` output was
//     indistinguishable from a prompt.
//   - OSC AS EVIDENCE. `osc_title` and `osc_progress` are regions sourced from the
//     escape sequences, not the screen — so a title is a signal here rather than
//     noise to be stripped.
package agentdetect

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml"
)

// State is the classification a rule assigns.
type State string

const (
	StateWorking State = "working"
	StateIdle    State = "idle"
	StateBlocked State = "blocked"
	StateUnknown State = "unknown"
	StateDone    State = "done"
)

// Input is what a manifest is evaluated against. The OSC fields are separate from
// the screen because upstream sources the `osc_title` / `osc_progress` regions
// from them directly — they are evidence in their own right, not screen content.
type Input struct {
	Screen      string
	OSCTitle    string
	OSCProgress string
}

// Gate is a recursive matcher. A gate matches when every one of its own
// conditions holds; the nested forms give AND / OR / NOT.
type Gate struct {
	All    []Gate
	Any    []Gate
	Not    []Gate
	fold   []string // `contains`, pre-lowercased to match upstream's lowercase compare
	re     []*regexp.Regexp
	lineRe []*regexp.Regexp
}

// Rule is one [[rule]] table.
type Rule struct {
	ID       string
	State    State
	Priority int
	Region   string
	Gate     Gate

	VisibleIdle     bool
	VisibleBlocker  bool
	VisibleWorking  bool
	SkipStateUpdate bool
}

// Manifest is one agent's detection manifest.
type Manifest struct {
	ID               string
	Aliases          []string
	Version          string
	MinEngineVersion int
	Rules            []Rule
}

// Result is what evaluating a manifest produced.
type Result struct {
	// Matched is false when no rule fired; State is then meaningless and the
	// caller must keep whatever it had. This is NOT the same as matching a rule
	// whose state is "unknown", which is a positive statement.
	Matched bool
	State   State
	// RuleID and Region name the winning rule, so a misclassification can be
	// traced to a line of TOML instead of guessed at.
	RuleID string
	Region string
	// SkipStateUpdate is the winning rule asking the caller to leave the committed
	// state alone — used upstream for screens that are evidence of nothing.
	SkipStateUpdate bool

	// priority is the winning rule's, kept only to resolve the next comparison.
	priority int
}

// rawManifest / rawRule / rawGate mirror the TOML exactly. They are separate from
// the compiled forms so a bad regexp fails at load with the rule's id attached,
// rather than at match time with nothing to name.
type rawManifest struct {
	ID               string    `toml:"id"`
	Version          string    `toml:"version"`
	MinEngineVersion int       `toml:"min_engine_version"`
	UpdatedAt        string    `toml:"updated_at"`
	Aliases          []string  `toml:"aliases"`
	Rules            []rawRule `toml:"rules"`
}

type rawRule struct {
	ID              string `toml:"id"`
	State           string `toml:"state"`
	Priority        int    `toml:"priority"`
	Region          string `toml:"region"`
	VisibleIdle     bool   `toml:"visible_idle"`
	VisibleBlocker  bool   `toml:"visible_blocker"`
	VisibleWorking  bool   `toml:"visible_working"`
	SkipStateUpdate bool   `toml:"skip_state_update"`
	// The gate fields are spelled out rather than embedded: go-toml does not
	// flatten an anonymous struct the way encoding/json does, so an embedded
	// rawGate silently parsed as empty — and an empty gate matches EVERYTHING,
	// which made every rule fire on every screen.
	All       []rawGate `toml:"all"`
	Any       []rawGate `toml:"any"`
	Not       []rawGate `toml:"not"`
	Contains  []string  `toml:"contains"`
	Regex     []string  `toml:"regex"`
	LineRegex []string  `toml:"line_regex"`
}

// gate collects a rule's own matcher fields.
func (r rawRule) gate() rawGate {
	return rawGate{All: r.All, Any: r.Any, Not: r.Not,
		Contains: r.Contains, Regex: r.Regex, LineRegex: r.LineRegex}
}

type rawGate struct {
	All       []rawGate `toml:"all"`
	Any       []rawGate `toml:"any"`
	Not       []rawGate `toml:"not"`
	Contains  []string  `toml:"contains"`
	Regex     []string  `toml:"regex"`
	LineRegex []string  `toml:"line_regex"`
}

// defaultRegion matches upstream's serde default.
const defaultRegion = "whole_recent"

// ParseManifest compiles one manifest's TOML.
func ParseManifest(data []byte) (*Manifest, error) {
	var raw rawManifest
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.ID == "" {
		return nil, fmt.Errorf("manifest has no id")
	}
	m := &Manifest{
		ID: raw.ID, Aliases: raw.Aliases, Version: raw.Version,
		MinEngineVersion: raw.MinEngineVersion,
	}
	for _, rr := range raw.Rules {
		g, err := compileGate(rr.gate())
		if err != nil {
			return nil, fmt.Errorf("%s: rule %q: %w", raw.ID, rr.ID, err)
		}
		region := rr.Region
		if region == "" {
			region = defaultRegion
		}
		// An absent state is Unknown upstream, not an error.
		st := State(rr.State)
		if st == "" {
			st = StateUnknown
		}
		m.Rules = append(m.Rules, Rule{
			ID: rr.ID, State: st, Priority: rr.Priority, Region: region, Gate: g,
			VisibleIdle: rr.VisibleIdle, VisibleBlocker: rr.VisibleBlocker,
			VisibleWorking: rr.VisibleWorking, SkipStateUpdate: rr.SkipStateUpdate,
		})
	}
	return m, nil
}

func compileGate(rg rawGate) (Gate, error) {
	g := Gate{}
	for _, c := range rg.Contains {
		// Lowercased once here because the match compares against a lowercased
		// region; doing it per evaluation would be the same answer, slower.
		g.fold = append(g.fold, strings.ToLower(c))
	}
	for _, p := range rg.Regex {
		re, err := regexp.Compile(translateRegex(p))
		if err != nil {
			return Gate{}, fmt.Errorf("regex %q: %w", p, err)
		}
		g.re = append(g.re, re)
	}
	for _, p := range rg.LineRegex {
		re, err := regexp.Compile(translateRegex(p))
		if err != nil {
			return Gate{}, fmt.Errorf("line_regex %q: %w", p, err)
		}
		g.lineRe = append(g.lineRe, re)
	}
	for _, sub := range rg.All {
		s, err := compileGate(sub)
		if err != nil {
			return Gate{}, err
		}
		g.All = append(g.All, s)
	}
	for _, sub := range rg.Any {
		s, err := compileGate(sub)
		if err != nil {
			return Gate{}, err
		}
		g.Any = append(g.Any, s)
	}
	for _, sub := range rg.Not {
		s, err := compileGate(sub)
		if err != nil {
			return Gate{}, err
		}
		g.Not = append(g.Not, s)
	}
	return g, nil
}

// Classify evaluates every rule and returns the winner.
//
// The selection rule is upstream's and is not the obvious one: the highest
// PRIORITY wins, and ties go to the rule that appears FIRST in the file (a later
// rule must strictly exceed the incumbent to displace it). Rules are all
// evaluated rather than short-circuited, because priority order and file order
// are independent.
func (m *Manifest) Classify(in Input) Result {
	var out Result
	for i := range m.Rules {
		r := &m.Rules[i]
		text := Region(in, r.Region)
		if !r.Gate.matches(text) {
			continue
		}
		if out.Matched && out.priorityAtLeast(r.Priority) {
			continue
		}
		out = Result{
			Matched: true, State: r.State, RuleID: r.ID, Region: r.Region,
			SkipStateUpdate: r.SkipStateUpdate, priority: r.Priority,
		}
	}
	return out
}

func (r Result) priorityAtLeast(p int) bool { return r.priority >= p }

// matches reports whether the gate holds for a region's text.
func (g Gate) matches(text string) bool {
	lower := strings.ToLower(text)
	return g.matchesFolded(text, lower)
}

func (g Gate) matchesFolded(text, lower string) bool {
	for _, needle := range g.fold {
		if !strings.Contains(lower, needle) {
			return false
		}
	}
	for _, re := range g.re {
		if !re.MatchString(text) {
			return false
		}
	}
	// line_regex is per LINE: every pattern must match SOME line. Note the
	// asymmetry with regex, which matches the region as a whole — a pattern
	// anchored with ^ means "the start of some line" only in this form.
	for _, re := range g.lineRe {
		if !anyLineMatches(text, re) {
			return false
		}
	}
	for _, sub := range g.All {
		if !sub.matchesFolded(text, lower) {
			return false
		}
	}
	// `any` constrains only when it is present; an empty list is not "nothing
	// matched", it is "no such condition".
	if len(g.Any) > 0 {
		ok := false
		for _, sub := range g.Any {
			if sub.matchesFolded(text, lower) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, sub := range g.Not {
		if sub.matchesFolded(text, lower) {
			return false
		}
	}
	return true
}

func anyLineMatches(text string, re *regexp.Regexp) bool {
	for _, line := range splitLines(text) {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// splitLines mirrors Rust's str::lines: split on \n, drop one trailing \r, and do
// NOT produce a final empty element for a trailing newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

// Region extracts the part of the input a rule reads. An unrecognised region
// yields "", which matches upstream and means the rule simply cannot fire —
// preferable to falling back to the whole screen, which would silently widen a
// rule written for a narrow region.
func Region(in Input, spec string) string {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "osc_title":
		return in.OSCTitle
	case "osc_progress":
		return in.OSCProgress
	}
	c := in.Screen
	switch spec {
	case "whole_recent":
		return c
	case "after_last_prompt_marker":
		return afterLastPromptMarker(c)
	case "after_last_horizontal_rule":
		return afterLastHorizontalRule(c)
	case "prompt_box_body":
		return promptBoxBody(c)
	}
	if n, ok := regionCount(spec, "bottom_non_empty_lines"); ok {
		return bottomNonEmptyLines(c, n)
	}
	if n, ok := regionCount(spec, "bottom_lines"); ok {
		return bottomLines(c, n)
	}
	if n, ok := regionCount(spec, "top_non_empty_lines"); ok {
		return topNonEmptyLines(c, n)
	}
	return ""
}

func regionCount(spec, name string) (int, bool) {
	rest, ok := strings.CutPrefix(spec, name)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutPrefix(rest, "(")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// lineStartOffset is the byte offset where line i begins, mirroring upstream's
// sum of line.len()+1.
func lineStartOffset(content string, lines []string, i int) int {
	if i > len(lines) {
		i = len(lines)
	}
	off := 0
	for _, l := range lines[:i] {
		off += len(l) + 1
	}
	if off > len(content) {
		off = len(content)
	}
	return off
}

// joinFrom returns content from line i onward. It SLICES rather than re-joins so
// a trailing newline survives — upstream returns a &str into the original, and a
// line_regex anchored with $ can tell the difference.
func joinFrom(content string, lines []string, i int) string {
	if i >= len(lines) {
		return ""
	}
	return content[lineStartOffset(content, lines, i):]
}

func bottomLines(content string, n int) string {
	lines := splitLines(content)
	if n >= len(lines) {
		return content
	}
	return joinFrom(content, lines, len(lines)-n)
}

// bottomNonEmptyLines returns everything from the nth-from-last non-empty line
// onward — so the slice INCLUDES any blank lines interleaved below it.
func bottomNonEmptyLines(content string, n int) string {
	lines := splitLines(content)
	seen, start := 0, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		seen++
		start = i
		if seen == n {
			break
		}
	}
	if start < 0 {
		return ""
	}
	return joinFrom(content, lines, start)
}

func topNonEmptyLines(content string, n int) string {
	lines := splitLines(content)
	seen, end := 0, -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		seen++
		end = i
		if seen == n {
			break
		}
	}
	if end < 0 {
		return ""
	}
	// Upstream slices up to the START of the line after the last kept one, so
	// the trailing newline is included.
	return content[:lineStartOffset(content, lines, end+1)]
}

// isHorizontalRule recognises a box-drawing rule. A short run of ─ counts only if
// nothing follows it; three or more count regardless, which is how a bordered
// prompt box with a title in its top edge is still a rule.
func isHorizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	n := 0
	for _, r := range t {
		if r != '─' {
			break
		}
		n++
	}
	if n == 0 {
		return false
	}
	suffix := strings.TrimLeft(string([]rune(t)[n:]), " \t")
	return suffix == "" || n >= 3
}

func afterLastHorizontalRule(content string) string {
	lines := splitLines(content)
	last := -1
	for i, l := range lines {
		if isHorizontalRule(l) {
			last = i
		}
	}
	return joinFrom(content, lines, last+1)
}

// codexPromptLine is upstream's marker for a codex-style input line.
func codexPromptLine(line string) bool {
	return line == "›" || strings.HasPrefix(line, "› ")
}

func afterLastPromptMarker(content string) string {
	lines := splitLines(content)
	for i := len(lines) - 1; i >= 0; i-- {
		if codexPromptLine(lines[i]) {
			return joinFrom(content, lines, i+1)
		}
	}
	return content
}

// promptBoxBody is the text between the SECOND-from-last horizontal rule and the
// next rule below it — i.e. the inside of the bottom-most box. Counting borders
// from the bottom is what makes it the CURRENT prompt box rather than some box
// scrolled past.
func promptBoxBody(content string) string {
	lines := splitLines(content)
	top, borders := -1, 0
	for i := len(lines) - 1; i >= 0; i-- {
		if isHorizontalRule(lines[i]) {
			borders++
			if borders == 2 {
				top = i
				break
			}
		}
	}
	if top < 0 {
		return ""
	}
	end := len(lines)
	for i := top + 1; i < len(lines); i++ {
		if isHorizontalRule(lines[i]) {
			end = i
			break
		}
	}
	if top+1 >= end {
		return ""
	}
	return strings.Join(lines[top+1:end], "\n")
}

// rustEscape matches the Rust regex-crate escapes Go's RE2 spells differently:
// \uHHHH and \u{HH..}. Go writes both as \x{HH..}.
var rustEscape = regexp.MustCompile(`\\u\{([0-9A-Fa-f]{1,6})\}|\\u([0-9A-Fa-f]{4})`)

// translateRegex converts a manifest pattern from Rust regex syntax to Go RE2.
//
// The manifests are authored against Rust's regex crate, and two of its spellings
// are not RE2's. Both conversions below are mechanical; the second is an
// APPROXIMATION and is called out as such.
//
//   - \uHHHH / \u{HH..} -> \x{HH..}. Identical meaning, different spelling. This
//     is most of it: the braille spinner ranges [⠀-⣿] that agents draw
//     progress with, and variation selectors after an emoji.
//   - \p{Alphabetic} -> \p{L}. NOT identical: Unicode's Alphabetic property is
//     L plus Nl plus Other_Alphabetic, and RE2 has no name for the last of those.
//     \p{L} is narrower by combining marks that can begin a word in some scripts.
//     Accepted because every use of it in the bundle is "a word follows a spinner
//     glyph", where the difference cannot arise — but a manifest refresh that
//     uses it for something else deserves a second look.
//
// Anything still uncompilable is left to the caller to skip, per rule.
func translateRegex(pattern string) string {
	out := rustEscape.ReplaceAllStringFunc(pattern, func(m string) string {
		sub := rustEscape.FindStringSubmatch(m)
		hex := sub[1]
		if hex == "" {
			hex = sub[2]
		}
		return "\\x{" + hex + "}"
	})
	out = strings.ReplaceAll(out, `\p{Alphabetic}`, `\p{L}`)
	out = strings.ReplaceAll(out, `\P{Alphabetic}`, `\P{L}`)
	return out
}
