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
