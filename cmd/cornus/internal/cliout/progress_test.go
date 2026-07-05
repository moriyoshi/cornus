package cliout

import (
	"strings"
	"testing"
)

// In every non-(fancy+TTY) mode a Progress is a silent no-op overlay: it never
// starts a live program, and Task/Update/Done/Fail/SetFraction/Stop are safe and
// produce no output of their own. The surrounding notices/events are what carry
// the text — assert the overlay itself stays quiet and inert.
func TestProgressFallbackIsSilent(t *testing.T) {
	for _, mode := range []Mode{ModePlain, ModeJSON} {
		d, out, errb := newTest(mode, false)
		p := d.Progress()
		if p.live != nil {
			t.Fatalf("mode %v: Progress went live without a TTY", mode)
		}
		task := p.Task("web: starting")
		task.Update("web: pulling")
		p.SetFraction(0.5)
		task.Done("done")
		other := p.Task("db")
		other.Fail("boom")
		p.Stop()
		p.Stop() // idempotent
		if out.Len() != 0 || errb.Len() != 0 {
			t.Errorf("mode %v: no-op Progress wrote output: out=%q err=%q", mode, out.String(), errb.String())
		}
	}
}

// Fancy mode without a TTY (the case buffer-based tests hit) must also fall back
// to the silent overlay, so the live bubbletea program never runs against a
// non-terminal writer.
func TestProgressFancyNonTTYFallsBack(t *testing.T) {
	d, out, errb := newTest(ModeFancy, true)
	if d.errTTY {
		t.Fatal("newTest driver unexpectedly reports errTTY")
	}
	p := d.Progress()
	if p.live != nil {
		t.Fatal("fancy Progress went live against a non-TTY buffer")
	}
	p.Task("x").Done("")
	p.Stop()
	if out.Len() != 0 || errb.Len() != 0 {
		t.Errorf("fancy non-tty Progress wrote output: out=%q err=%q", out.String(), errb.String())
	}
}

// ParseProgressStyle maps the user-facing --progress / CORNUS_PROGRESS values to
// a style, defaulting an empty value and rejecting an unknown one (so a flag can
// error while env silently defaults).
func TestParseProgressStyle(t *testing.T) {
	cases := []struct {
		in   string
		want ProgressStyle
		ok   bool
	}{
		{"", ProgressStatus, true},
		{"status", ProgressStatus, true},
		{"STATUS", ProgressStatus, true},
		{"  stream  ", ProgressStream, true},
		{"stream", ProgressStream, true},
		{"nonsense", ProgressStatus, false}, // unknown -> default, not ok
	}
	for _, tc := range cases {
		got, ok := ParseProgressStyle(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseProgressStyle(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// New resolves CORNUS_PROGRESS into the driver's style, and SetProgressStyle
// (the --progress flag) overrides it.
func TestDriverProgressStyleResolution(t *testing.T) {
	envd := New(Options{Output: "plain", Env: func(k string) string {
		if k == "CORNUS_PROGRESS" {
			return "stream"
		}
		return ""
	}})
	if envd.ProgressStyle() != ProgressStream {
		t.Fatalf("CORNUS_PROGRESS=stream not honored: got %v", envd.ProgressStyle())
	}
	def := New(Options{Output: "plain", Env: func(string) string { return "" }})
	if def.ProgressStyle() != ProgressStatus {
		t.Fatalf("default style not ProgressStatus: got %v", def.ProgressStyle())
	}
	def.SetProgressStyle(ProgressStream)
	if def.ProgressStyle() != ProgressStream {
		t.Fatalf("SetProgressStyle did not override: got %v", def.ProgressStyle())
	}
}

// liveProgressEligible is the pure gate for the in-place region: fancy + a real
// stderr TTY + the status style. Anything else (a pipe, plain mode, or the
// stream preference on a capable terminal) falls back to append-only output.
func TestLiveProgressEligible(t *testing.T) {
	cases := []struct {
		name   string
		mode   Mode
		errTTY bool
		style  ProgressStyle
		want   bool
	}{
		{"fancy tty status -> live", ModeFancy, true, ProgressStatus, true},
		{"fancy tty stream -> fallback", ModeFancy, true, ProgressStream, false},
		{"fancy no-tty status -> fallback", ModeFancy, false, ProgressStatus, false},
		{"plain tty status -> fallback", ModePlain, true, ProgressStatus, false},
		{"json tty status -> fallback", ModeJSON, true, ProgressStatus, false},
	}
	for _, tc := range cases {
		d := &Driver{mode: tc.mode, errTTY: tc.errTTY, progressStyle: tc.style}
		if got := d.liveProgressEligible(); got != tc.want {
			t.Errorf("%s: liveProgressEligible() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// While no live program is active, notices/logs/events take their ordinary
// channel (routeAbove reports false), unchanged by the progress plumbing.
func TestRouteAboveInactive(t *testing.T) {
	d, _, errb := newTest(ModePlain, false)
	if d.routeAbove(func(b *strings.Builder) { b.WriteString("x") }) {
		t.Fatal("routeAbove reported handled with no active program")
	}
	d.Warn("careful")
	if !strings.Contains(errb.String(), "warning: careful") {
		t.Errorf("notice did not reach stderr normally: %q", errb.String())
	}
}
