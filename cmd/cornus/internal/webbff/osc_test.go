package webbff

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The scanner's contract, restated from reading osc.go so the tests below assert
// what the code does rather than what a fix was meant to do:
//
//   - Only OSC 0 and OSC 2 yield a title; only OSC 7 yields a directory. OSC 1
//     (icon name) and every other Ps — OSC 8 hyperlinks and OSC 133 prompt marks
//     ride the same stream — yield neither.
//   - Both terminators are honoured: BEL (0x07) and ST (ESC \).
//   - Title and cwd are reported INDEPENDENTLY: hasTitle=false means "this chunk
//     said nothing about the title", not "the title is empty".
//   - hasTitle=true with an empty string is distinct: the program CLEARED it.
//     There is no such form for cwd — a malformed OSC 7 commits nothing and
//     leaves the last good directory standing.
//   - State survives across scan calls, because a sequence can straddle a chunk.
//   - A payload past oscPayloadMax is dropped, and the scanner resynchronises.

const (
	esc = "\x1b"
	bel = "\x07"
	st  = "\x1b\\"
)

func TestOSCScannerTitleSequences(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		wantB bool
	}{
		{"osc0 bel", esc + "]0;hello" + bel, "hello", true},
		{"osc2 bel", esc + "]2;vim README.md" + bel, "vim README.md", true},
		{"osc2 st", esc + "]2;less /etc/hosts" + st, "less /etc/hosts", true},
		{"osc0 st", esc + "]0;user@host: ~/src" + st, "user@host: ~/src", true},

		// Ps values that are not window titles must not be mistaken for one.
		{"osc1 icon name only", esc + "]1;icon" + bel, "", false},
		{"osc7 is a directory, not a title", esc + "]7;file:///srv" + bel, "", false},
		{"osc8 hyperlink", esc + "]8;;https://example.com" + st, "", false},
		{"osc133 prompt mark", esc + "]133;A" + bel, "", false},
		{"osc4 colour", esc + "]4;1;#ff0000" + bel, "", false},

		{"plain output", "just some shell output\r\n", "", false},
		{"csi is not osc", esc + "[0m" + esc + "[2J", "", false},
		{"osc without separator", esc + "]2" + bel, "", false},

		// Clearing the title is a real instruction, distinct from "no title here".
		{"empty title clears", esc + "]2;" + bel, "", true},

		// Several sets in one chunk: only the last is in effect afterwards.
		{"last one wins", esc + "]2;first" + bel + "text" + esc + "]2;second" + bel, "second", true},
		{"title then non-title", esc + "]2;keep" + bel + esc + "]1;icon" + bel, "keep", true},

		// Payload hygiene: the stream is whatever runs in the container.
		{"controls stripped", esc + "]2;a\x01b\x7fc" + bel, "abc", true},
		{"trimmed", esc + "]2;   spaced   " + bel, "spaced", true},
		{"invalid utf8 dropped", esc + "]2;ok\xff\xfe" + bel, "ok", true},

		// An unterminated OSC interrupted by a new escape must not swallow it.
		{"restart on nested esc", esc + "]2;abandoned" + esc + "]2;real" + bel, "real", true},
		{"can aborts", esc + "]2;gone\x18" + esc + "]2;kept" + bel, "kept", true},
		{"sub aborts", esc + "]2;gone\x1a" + esc + "]2;kept" + bel, "kept", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscScanner
			u := s.scan([]byte(tc.in))
			if u.hasTitle != tc.wantB || u.title != tc.want {
				t.Fatalf("scan(%q) title = (%q, %v), want (%q, %v)", tc.in, u.title, u.hasTitle, tc.want, tc.wantB)
			}
		})
	}
}

func TestOSCScannerCwdSequences(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		wantB bool
	}{
		// The conformant form, and the host is ignored: inside a container it is
		// the container's own name and tells us nothing.
		{"file uri with host", esc + "]7;file://box/srv/app" + st, "/srv/app", true},
		{"file uri empty host", esc + "]7;file:///srv/app" + st, "/srv/app", true},
		{"bel terminator", esc + "]7;file:///srv/app" + bel, "/srv/app", true},
		{"percent decoded", esc + "]7;file:///srv/my%20app" + st, "/srv/my app", true},
		{"utf8 percent decoded", esc + "]7;file:///srv/%E3%83%86%E3%82%B9%E3%83%88" + st, "/srv/テスト", true},
		{"cleaned", esc + "]7;file:///srv/app/../lib/" + st, "/srv/lib", true},
		{"root", esc + "]7;file:///" + st, "/", true},

		// Non-conformant but real: emitters send a bare absolute path.
		{"bare absolute path", esc + "]7;/srv/app" + st, "/srv/app", true},

		// Everything that is not a usable absolute path commits NOTHING, so the
		// last good directory survives a garbled sequence.
		{"relative path rejected", esc + "]7;srv/app" + st, "", false},
		{"empty payload rejected", esc + "]7;" + st, "", false},
		{"file uri with no path rejected", esc + "]7;file://box" + st, "", false},
		{"bad percent escape rejected", esc + "]7;file:///srv/%zz" + st, "", false},
		{"newline rejected", esc + "]7;file:///srv/a%0Ab" + st, "", false},
		{"nul rejected", esc + "]7;file:///srv/a%00b" + st, "", false},
		{"invalid utf8 rejected", esc + "]7;file:///srv/%ff%fe" + st, "", false},
		{"over path_max rejected", esc + "]7;file:///" + strings.Repeat("d", cwdMaxBytes) + st, "", false},

		// Other Ps values are not directories.
		{"osc2 is a title, not a directory", esc + "]2;/srv/app" + bel, "", false},
		{"osc9 ignored", esc + "]9;9;/srv/app" + bel, "", false},

		{"last one wins", esc + "]7;file:///a" + st + esc + "]7;file:///b" + st, "/b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscScanner
			u := s.scan([]byte(tc.in))
			if u.hasCwd != tc.wantB || u.cwd != tc.want {
				t.Fatalf("scan(%q) cwd = (%q, %v), want (%q, %v)", tc.in, u.cwd, u.hasCwd, tc.want, tc.wantB)
			}
		})
	}
}

// The pid announcement is ours, not a terminal's (pkg/shells.WrapAnnouncePID
// emits it, this reads it). It rides the same private OSC code another program
// could pick, so the payload's tag is what keeps a collision from being read as a
// pid — and a wrong pid points the /proc probe at a stranger's process.
func TestOSCScannerPIDSequence(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  int
		wantB bool
	}{
		{"announced", esc + "]5379;cornus-pid=4242" + bel, 4242, true},
		{"st terminator", esc + "]5379;cornus-pid=7" + st, 7, true},
		{"untagged payload ignored", esc + "]5379;4242" + bel, 0, false},
		{"another program's payload ignored", esc + "]5379;hello world" + bel, 0, false},
		{"zero rejected", esc + "]5379;cornus-pid=0" + bel, 0, false},
		{"non-numeric rejected", esc + "]5379;cornus-pid=abc" + bel, 0, false},
		{"a title is not a pid", esc + "]0;cornus-pid=4242" + bel, 0, false},
		{"last one wins", esc + "]5379;cornus-pid=1" + bel + esc + "]5379;cornus-pid=2" + bel, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscScanner
			u := s.scan([]byte(tc.in))
			if u.hasPID != tc.wantB || u.pid != tc.want {
				t.Fatalf("scan(%q) pid = (%d, %v), want (%d, %v)", tc.in, u.pid, u.hasPID, tc.want, tc.wantB)
			}
			// A pid sequence must be invisible to the other two facts: it arrives in
			// the same stream as a prompt's title and directory. Scoped to inputs
			// that ARE pid sequences — "a title is not a pid" carries Ps 0, so it
			// setting a title is the correct answer, not a leak.
			if strings.Contains(tc.in, "]"+"5379;") && (u.hasTitle || u.hasCwd) {
				t.Fatalf("pid sequence also reported title=%v cwd=%v", u.hasTitle, u.hasCwd)
			}
		})
	}
}

// A prompt hook emits BOTH in one burst — the title and the directory, back to
// back, every time the prompt is drawn. They have to survive each other: an
// implementation with one "last value" slot would let whichever came second erase
// the first, and the failure would be invisible in any test that sent one alone.
func TestOSCScannerTitleAndCwdAreIndependent(t *testing.T) {
	var s oscScanner
	u := s.scan([]byte(esc + "]0;root@box: /srv/app" + bel + esc + "]7;file://box/srv/app" + st))
	if !u.hasTitle || u.title != "root@box: /srv/app" {
		t.Fatalf("title = (%q, %v), want (%q, true)", u.title, u.hasTitle, "root@box: /srv/app")
	}
	if !u.hasCwd || u.cwd != "/srv/app" {
		t.Fatalf("cwd = (%q, %v), want (%q, true)", u.cwd, u.hasCwd, "/srv/app")
	}

	// A chunk that carries only one of them must say nothing about the other,
	// which is what lets the session keep the value it already had.
	u = s.scan([]byte(esc + "]2;vim" + bel))
	if !u.hasTitle || u.hasCwd {
		t.Fatalf("title-only chunk reported hasTitle=%v hasCwd=%v, want true/false", u.hasTitle, u.hasCwd)
	}
	u = s.scan([]byte(esc + "]7;file:///tmp" + st))
	if u.hasTitle || !u.hasCwd {
		t.Fatalf("cwd-only chunk reported hasTitle=%v hasCwd=%v, want false/true", u.hasTitle, u.hasCwd)
	}
}

// A sequence routinely straddles a read boundary — the kernel splits where it
// likes, not where sequences end. Feeding one byte per call is the worst case of
// that, so a scanner that survives it survives any chunking.
func TestOSCScannerAcrossChunkBoundaries(t *testing.T) {
	const stream = "output" + esc + "]0;vim main.go" + bel + "more" + esc + "]7;file:///srv/app" + st + "tail"
	var s oscScanner
	var title, cwd string
	var titles, cwds int
	for i := 0; i < len(stream); i++ {
		u := s.scan([]byte{stream[i]})
		if u.hasTitle {
			title, titles = u.title, titles+1
		}
		if u.hasCwd {
			cwd, cwds = u.cwd, cwds+1
		}
	}
	if titles != 1 || cwds != 1 {
		t.Fatalf("byte-at-a-time feed reported %d titles and %d cwds, want exactly 1 of each", titles, cwds)
	}
	if title != "vim main.go" || cwd != "/srv/app" {
		t.Fatalf("byte-at-a-time = (%q, %q), want (%q, %q)", title, cwd, "vim main.go", "/srv/app")
	}

	// The same stream in one chunk must produce the identical result: chunking is
	// an artefact of the transport and may not change what the session reports.
	var whole oscScanner
	u := whole.scan([]byte(stream))
	if u.title != title || u.cwd != cwd {
		t.Fatalf("whole-chunk scan = (%q, %q), want (%q, %q)", u.title, u.cwd, title, cwd)
	}
}

func TestOSCScannerBoundsPayload(t *testing.T) {
	var s oscScanner
	// Mid-sequence, with no terminator yet in sight, is where the memory would
	// actually grow, so that is where the buffer has to be checked.
	if u := s.scan([]byte(esc + "]2;" + strings.Repeat("x", oscPayloadMax*4))); u.hasTitle {
		t.Fatal("unterminated OSC reported a title")
	}
	if len(s.buf) > oscPayloadMax {
		t.Fatalf("scanner buffered %d payload bytes mid-sequence, want at most %d", len(s.buf), oscPayloadMax)
	}
	if u := s.scan([]byte(bel)); u.hasTitle {
		t.Fatalf("overlong payload committed as title %q, want it dropped", u.title)
	}
	if u := s.scan([]byte(esc + "]2;after" + bel)); !u.hasTitle || u.title != "after" {
		t.Fatalf("scanner did not resynchronise: got (%q, %v), want (\"after\", true)", u.title, u.hasTitle)
	}

	// A title that is merely long is truncated, not dropped.
	long := esc + "]2;" + strings.Repeat("ü", titleMaxRunes*2) + bel
	var s2 oscScanner
	u := s2.scan([]byte(long))
	if !u.hasTitle {
		t.Fatal("long-but-valid title was dropped, want it truncated")
	}
	if n := len([]rune(u.title)); n != titleMaxRunes {
		t.Fatalf("truncated title has %d runes, want %d", n, titleMaxRunes)
	}
	if !strings.HasPrefix(u.title, "üü") {
		t.Fatalf("truncation split a multi-byte rune: %q", u.title)
	}
}

// The end-to-end path: a program inside the session sets a title, and the session
// list the web UI polls reports it. This is what the tab label reads.
func TestTermSessionReportsOSCTitle(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/bash"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	if got := ts.info().Title; got != "" {
		t.Fatalf("new session already has title %q, want none until output sets one", got)
	}

	shell := fe.shellConn()
	if _, err := shell.Write([]byte(esc + "]0;vim README.md" + bel)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "title from OSC", func() bool { return ts.info().Title == "vim README.md" })

	// Leaving the program restores the shell's own title — the tab has to follow.
	if _, err := shell.Write([]byte(esc + "]0;user@web: /srv" + bel)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "title updated", func() bool { return ts.info().Title == "user@web: /srv" })

	// Cmd stays what the session was launched with; the title is additive, so a UI
	// that ignores it still shows exactly what it showed before.
	if got := strings.Join(ts.info().Cmd, " "); got != "/bin/bash" {
		t.Fatalf("Cmd = %q, want it unchanged at %q", got, "/bin/bash")
	}
}

// The same path for the directory, which is what a split inherits. The launch dir
// must NOT move with it: `dir` is where this shell started, a fact about the past
// that the session list still has to report truthfully.
func TestTermSessionReportsOSCCwd(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/bash"}, "/srv")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	if got := ts.info().Cwd; got != "" {
		t.Fatalf("new session already reports cwd %q, want none until OSC 7 arrives", got)
	}
	if got := fe.lastCreate(t).WorkingDir; got != "/srv" {
		t.Fatalf("launch dir = %q, want %q", got, "/srv")
	}

	shell := fe.shellConn()
	if _, err := shell.Write([]byte(esc + "]7;file://web/srv/app" + st)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cwd from OSC 7", func() bool { return ts.info().Cwd == "/srv/app" })

	// A cd moves it again — the whole reason this is polled rather than recorded once.
	if _, err := shell.Write([]byte(esc + "]7;file://web/tmp" + st)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cwd tracks a cd", func() bool { return ts.info().Cwd == "/tmp" })

	// A garbled report leaves the last known-good directory standing rather than
	// blanking it: sending a consumer to the wrong place is worse than staleness.
	if _, err := shell.Write([]byte(esc + "]7;not-a-path" + st)); err != nil {
		t.Fatal(err)
	}
	if _, err := shell.Write([]byte(esc + "]0;marker" + bel)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the marker arrived", func() bool { return ts.info().Title == "marker" })
	if got := ts.info().Cwd; got != "/tmp" {
		t.Fatalf("cwd after a malformed OSC 7 = %q, want it left at %q", got, "/tmp")
	}
}

// A session whose shell emits nothing must report nothing, so the UI can tell
// "never reported" from "reported empty" and fall back to the pane's own values.
func TestTermSessionWithoutOSCHasNoTitleOrCwd(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	shell := fe.shellConn()
	if _, err := shell.Write([]byte("$ echo hi\r\nhi\r\n$ ")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "output arrived", func() bool { return ts.rings() > 0 })
	if got := ts.info(); got.Title != "" || got.Cwd != "" {
		t.Fatalf("plain output produced title %q cwd %q, want neither", got.Title, got.Cwd)
	}
}

// rings reports the replay ring's current length, so a test can wait for output to
// have been consumed by readLoop before asserting on what it did not produce.
func (ts *termSession) rings() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.ring.buf)
}

// The detector renders the stream to a headless screen and matches rules against
// the result, and the vt100 library it uses does NOT understand OSC: its scanner
// consumes "ESC ]" and lets the payload through as printable text. So the scanner
// has to hand the detector the bytes a terminal would actually PAINT.
//
// Asserted at the scanner because that is where the stripping happens, and again
// at the detector below because that is where being wrong does damage.
func TestOSCScannerStripsOSCFromVisibleBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"title", "a" + esc + "]0;vim README.md" + bel + "b", "ab"},
		{"cwd st", "a" + esc + "]7;file:///srv/app" + st + "b", "ab"},
		{"pid", "a" + esc + "]5379;cornus-pid=4242" + bel + "b", "ab"},
		{"back to back", esc + "]0;t" + bel + esc + "]7;file:///x" + st + "$ ", "$ "},
		{"nothing to strip", "plain output\r\n", "plain output\r\n"},

		// CSI and other escapes MUST survive: they are how the screen model learns
		// about colour, clearing and cursor motion. A stripper that ate them would
		// leave the detector matching against a screen nothing had ever drawn on.
		{"csi passes through", esc + "[0m" + "x" + esc + "[2J", esc + "[0m" + "x" + esc + "[2J"},
		{"csi around an osc", esc + "[1m" + esc + "]0;t" + bel + "hi" + esc + "[0m", esc + "[1m" + "hi" + esc + "[0m"},
		// An nF escape (charset selection) is STRIPPED, not passed through: the
		// vt100 model consumes ESC and the intermediate and then paints the final
		// byte, so passing it on left a stray "B" on the screen. See
		// TestOSCScannerStripsEveryStringSequence for the family.
		{"nf escape stripped whole", esc + "(B" + "x", "x"},
		{"esc esc", esc + esc + "[0m", esc + esc + "[0m"},

		// An unterminated OSC swallows until something ends it — but the sequence
		// that interrupts it is still the screen's business.
		{"restart on nested esc", esc + "]0;abandoned" + esc + "[0m" + "x", esc + "[0m" + "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscScanner
			if got := string(s.scan([]byte(tc.in)).visible); got != tc.want {
				t.Fatalf("scan(%q).visible = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Chunk boundaries again: the ESC introducing an OSC is HELD across the split,
	// so a scanner that emitted it eagerly would leak a stray ESC onto the screen
	// and a scanner that dropped it would eat the CSI that follows.
	var s oscScanner
	var got []byte
	for _, part := range []string{"a" + esc, "]0;ti", "tle" + bel + esc, "[0m" + "b"} {
		got = append(got, s.scan([]byte(part)).visible...)
	}
	if want := "a" + esc + "[0m" + "b"; string(got) != want {
		t.Fatalf("split-chunk visible = %q, want %q", got, want)
	}
}

// What the stripping is FOR. Feeding the detector raw bytes puts window titles and
// OSC 7 paths on the classification screen, where the shipped rules match them:
// `\by/n\b` hits a path like /srv/y/n, and the overwrite/delete rule hits any TUI
// that mirrors its dialog into its title. Either pins a session at "needs you"
// over text the user cannot see.
func TestDetectorIgnoresTextInsideOSCSequences(t *testing.T) {
	rules := loadRules()

	// A directory whose PATH contains y/n, reported the way a prompt hook does.
	d := newDetector(rules, []string{"/bin/bash"}, 24, 80, time.Millisecond)
	var s oscScanner
	for _, chunk := range []string{
		esc + "]5379;cornus-pid=4242" + bel,
		esc + "]7;file:///srv/y/n" + st,
		esc + "]0;Delete everything?" + bel,
		"$ ",
	} {
		d.feed(s.scan([]byte(chunk)).visible)
	}
	d.mu.Lock()
	screen := d.renderLocked()
	d.mu.Unlock()
	if strings.Contains(screen, "y/n") || strings.Contains(screen, "Delete") || strings.Contains(screen, "cornus-pid") {
		t.Fatalf("OSC payload text reached the classification screen: %q", screen)
	}
	d.onSettle()
	if got := d.current(); got == stateBlocked {
		t.Fatalf("session classified %q from text inside OSC sequences; screen was %q", got, screen)
	}

	// The same text ON THE SCREEN must still block — otherwise this test would pass
	// against a detector that had simply stopped working.
	// Scoped: the built-in rules apply to the program in the FOREGROUND, so the
	// detector has to believe one is there. Without this the assertion would pass
	// for the new reason (unknown agent) rather than the one it is testing.
	d2 := newDetector(rules, []string{"claude"}, 24, 80, time.Millisecond)
	// A shape the CLAUDE manifest actually classifies — the rules that decide this
	// are now upstream's, so the fixture has to be one of theirs.
	d2.feed([]byte("Do you want to proceed?\r\n\u276f 1. Yes\r\n  2. No\r\n"))
	d2.onSettle()
	if got := d2.current(); got != stateBlocked {
		t.Fatalf("visible prompt classified %q, want %q — the detector must still detect", got, stateBlocked)
	}
}

// Through the REAL path: bytes off the exec stream, into readLoop, into the
// detector. The test above feeds the detector directly and so proves the scanner
// and the detector agree — it says nothing about readLoop handing over the right
// bytes, and reverting that one line failed nothing until this existed.
//
// Each payload here is placed so that it WOULD match a shipped rule if it reached
// the screen: at the end of the buffer, where the anchors (`\?\s*$`, and the word
// boundary after "y/n") can bind. An earlier version of this test put them
// mid-stream, where consecutive OSC payloads concatenate — "/srv/y/n" ran into the
// next sequence's "0;" and killed the very boundary the rule needs — so it passed
// against the unstripped code too. Verified by neutralization, not by inspection.
func TestTermSessionDoesNotBlockOnTextInsideOSCSequences(t *testing.T) {
	dangerous := []struct {
		name string
		osc  string
		// seen reports that readLoop has consumed the sequence. Without waiting for
		// this the assertion below races the reader: a session starts out idle, so
		// "not blocked" is trivially true before any byte has been processed. That
		// is exactly how an earlier version of this test passed against the bug.
		seen func(termInfo) bool
	}{
		// A TUI mirroring its dialog into the window title.
		{
			"title mirroring a dialog",
			esc + "]0;Delete everything?" + bel,
			func(i termInfo) bool { return i.Title == "Delete everything?" },
		},
		// A directory whose PATH happens to contain y/n.
		{
			"cwd whose path contains y/n",
			esc + "]7;file:///srv/y/n" + st,
			func(i termInfo) bool { return i.Cwd == "/srv/y/n" },
		},
	}
	for _, tc := range dangerous {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeExec{}
			mgr := newTermManager(fe)
			mgr.detSettle = time.Millisecond
			ts, err := mgr.Create(context.Background(), "web", []string{"/bin/bash"}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Kill(ts.id)

			shell := fe.shellConn()
			// The prompt first, so the screen has something real on it, then the
			// sequence last so its payload would land at end of buffer.
			if _, err := shell.Write([]byte("$ " + tc.osc)); err != nil {
				t.Fatal(err)
			}
			eventually(t, "readLoop consumed the sequence", func() bool { return tc.seen(ts.info()) })
			// Settling to IDLE is the assertion. "not blocked" would be satisfied by a
			// state that has not been computed yet; idle can only be reached by the
			// settle timer having run on a screen with nothing alarming on it.
			eventually(t, "detector settled to idle", func() bool {
				return ts.info().State == string(stateIdle)
			})
		})
	}

	// The same text ON THE SCREEN must still block, or every assertion above would
	// be satisfied by a detector that had simply stopped classifying. Driven through
	// readLoop like the cases above, so it also proves the detector is still being
	// fed at all — stripping too much would silence it just as effectively as the
	// bug this test exists for.
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	mgr.detSettle = time.Millisecond
	// Launched straight into an agent, so the detector knows what is in front
	// without needing a probe — the built-in rules are scoped to the program.
	ts, err := mgr.Create(context.Background(), "web", []string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)
	if _, err := fe.shellConn().Write([]byte("\r\nDo you want to proceed?\r\n\u276f 1. Yes\r\n  2. No\r\n")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visible prompt blocks", func() bool { return ts.info().State == string(stateBlocked) })
}

// OSC is not the only sequence that carries a payload the screen must never see.
// DCS, APC, PM and SOS have the same shape — introducer, payload, ST — and the
// vt100 model paints every byte of all of them, because scanEscapeCommand returns
// after two bytes for anything that is not "ESC [". Measured before the fix: PM
// and SOS payloads matched the blocked rules outright, a DCS sixel put its whole
// image string on the screen, and an APC kitty-graphics blob put its base64 there.
//
// The nF escapes (charset selection, "ESC ( B") are the other family: the model
// consumes ESC and the intermediate and then paints the FINAL byte, so every one
// left a stray letter behind — and ncurses programs emit them constantly.
func TestOSCScannerStripsEveryStringSequence(t *testing.T) {
	b64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"
	cases := []struct{ name, in, want string }{
		{"DCS sixel", "a" + esc + "P0;1;0q#0;2;0;0;0#0~~@@vv@@~~$" + st + "b", "ab"},
		{"DCS tmux passthrough", "a" + esc + "Ptmux;" + esc + esc + "]0;inner" + bel + st + "b", "ab"},
		{"APC kitty graphics", "a" + esc + "_Gf=100,a=T;" + b64 + st + "b", "ab"},
		{"PM", "a" + esc + "^privacy message y/n" + st + "b", "ab"},
		{"SOS", "a" + esc + "Xstart of string, delete?" + st + "b", "ab"},
		{"charset select", "a" + esc + "(B" + "b", "ab"},
		{"charset select G1", "a" + esc + ")0" + "b", "ab"},
		{"stray ST swallowed", "a" + st + "b", "ab"},

		// BEL terminates OSC ONLY. Inside a DCS it is payload, and treating it as a
		// terminator would end the sequence early and spill the rest onto the screen.
		{"bel inside dcs is payload", "a" + esc + "Pq" + bel + "more-payload" + st + "b", "ab"},

		// Everything the screen model genuinely needs still gets through.
		{"csi survives", esc + "[31m" + "RED" + esc + "[0m", esc + "[31m" + "RED" + esc + "[0m"},
		{"two-byte escapes survive", esc + "7" + "x" + esc + "8", esc + "7" + "x" + esc + "8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscScanner
			if got := string(s.scan([]byte(tc.in)).visible); got != tc.want {
				t.Fatalf("scan(%q).visible = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// An unterminated string sequence must not blind the detector for the rest of the
// session. Swallowing forever is a worse failure than the leak the stripping
// exists to prevent: every session would look permanently idle.
func TestOSCScannerResynchronisesAfterAnUnterminatedStringSequence(t *testing.T) {
	var s oscScanner
	// A DCS that never ends, longer than the elide budget.
	s.scan([]byte(esc + "P" + strings.Repeat("x", stringElideMax+16)))
	got := string(s.scan([]byte("VISIBLE AGAIN")).visible)
	if got != "VISIBLE AGAIN" {
		t.Fatalf("scanner still blind after an overlong unterminated DCS: %q", got)
	}
}

// The payloads that were actually matching the shipped rules, driven through the
// real readLoop path. PM and SOS are the two that did.
func TestTermSessionDoesNotBlockOnDCSAndFriends(t *testing.T) {
	for _, tc := range []struct{ name, seq string }{
		{"PM carrying y/n", esc + "^status: continue? y/n" + st},
		{"SOS carrying a dialog", esc + "Xdelete everything?" + st},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeExec{}
			mgr := newTermManager(fe)
			mgr.detSettle = time.Millisecond
			ts, err := mgr.Create(context.Background(), "web", []string{"/bin/bash"}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Kill(ts.id)
			if _, err := fe.shellConn().Write([]byte("$ " + tc.seq)); err != nil {
				t.Fatal(err)
			}
			eventually(t, "readLoop consumed the sequence", func() bool { return ts.rings() > 0 })
			eventually(t, "detector settled to idle", func() bool {
				return ts.info().State == string(stateIdle)
			})
		})
	}
}
