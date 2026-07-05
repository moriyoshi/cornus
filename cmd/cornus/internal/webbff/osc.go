package webbff

// OSC sniffing for persistent terminal sessions (see term.go): the window TITLE a
// session sets, and the working DIRECTORY it reports.
//
// A session's tab is labelled with the argv the BFF resolved at creation — the
// shell's executable path, fixed for the session's whole life — and its `dir` is
// where that shell was told to start, equally fixed. Neither tracks the session.
// A tab reading "/bin/bash" says nothing about the vim in it, and a split that
// inherits `dir` lands where the ORIGINAL shell started rather than where the user
// actually is.
//
// Terminals have always answered both questions with OSC sequences, and the
// programs that care already emit them: interactive shells set the title from
// their prompt hook, full-screen programs set it on entry and restore it on exit,
// and a prompt hook that reports the directory emits OSC 7. So the answers are
// already in the byte stream — we only have to read them, exactly as a terminal
// emulator does. This is a passive tap on output the session already produces: no
// extra exec, no syscall, no per-poll cost, and it works identically on every
// deploy backend because it never leaves the stream.
//
// Both are best-effort by nature, and OSC 7 markedly more so than the title: on a
// desktop the hook that emits it comes from /etc/profile.d/vte.sh, which container
// images do not ship. A session that reports nothing simply has nothing here, and
// every consumer falls back. Nothing sniffed here may ever be load-bearing.

import (
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"cornus/pkg/shells"
)

const (
	// oscPayloadMax bounds one OSC sequence's payload. The stream is untrusted
	// (anything running in the container writes it), and an unterminated OSC would
	// otherwise buffer without limit. Past the cap we keep scanning for the
	// terminator but drop the payload, so an overlong sequence costs nothing and
	// still resynchronises instead of swallowing the rest of the session.
	oscPayloadMax = 4096
	// titleMaxRunes caps what reaches the UI. Titles are shown in a tab, which
	// truncates in CSS anyway; this bounds the JSON, not the layout.
	titleMaxRunes = 128
	// cwdMaxBytes is PATH_MAX. A reported directory longer than the kernel can
	// hold is not a directory, it is noise in the stream.
	cwdMaxBytes = 4096
)

// oscState is the scanner's position within an escape sequence.
type oscState int

const (
	oscGround  oscState = iota // ordinary output
	oscEsc                     // saw ESC, awaiting the sequence type
	oscBody                    // inside a string sequence, accumulating the payload
	oscBodyEsc                 // inside a string sequence, saw ESC — maybe ST
	oscInterm                  // inside an nF escape (ESC + intermediates + final)
)

// stringKind is which string-type sequence is in flight. They all have the same
// shape — introducer, payload, ST — and all of them must be elided from the bytes
// the detector sees; only OSC's payload is additionally PARSED.
type stringKind int

const (
	strNone  stringKind = iota
	strOSC              // ESC ]  — window title, cwd, our pid announcement
	strOther            // ESC P (DCS) / ESC _ (APC) / ESC ^ (PM) / ESC X (SOS)
)

// stringEliteMax bounds how much a single unterminated string sequence may
// swallow. Without it a program that emits a bare "ESC P" blinds the activity
// detector for the rest of the session, which is a worse failure than the leak
// this stripping exists to prevent: the detector would report a permanently quiet
// screen and every session would look idle. Generous enough for a real payload —
// a sixel image or a kitty graphics blob runs to hundreds of kilobytes.
const stringElideMax = 1 << 20

// oscUpdate is what one chunk of output changed. The two facts are reported
// INDEPENDENTLY: a chunk routinely carries a title and no directory (any program
// that sets a title) or a directory and no title, and conflating them would make
// every prompt overwrite whichever the other mechanism had learned.
type oscUpdate struct {
	title    string
	hasTitle bool
	cwd      string
	hasCwd   bool
	// pid is the session process's id as the CONTAINER numbers it, announced once
	// by the launch wrapper the server installs (see pkg/shells.WrapAnnouncePID).
	// It arrives before the shell's first byte of output and never changes.
	pid    int
	hasPID bool
	// visible is the chunk with every OSC sequence removed — what a terminal would
	// actually PAINT. It exists because the activity detector renders the stream to
	// a headless screen and matches rules against the result, and the vt100 library
	// it uses does not understand OSC at all: its scanner consumes "ESC ]" and then
	// lets the payload through as ordinary printable text (scanEscapeCommand only
	// treats "ESC [" as introducing arguments).
	//
	// So without this a window title lands ON the classification screen. That is not
	// cosmetic: the blocked rule `\by/n\b` matches an OSC 7 path like
	// file:///srv/y/n, and `\b(delete|overwrite|proceed)\b[^\n]*\?$` matches any TUI
	// that mirrors its dialog into its title — either of which pins a session at
	// "needs you" over text the user cannot see. Sessions carry an OSC
	// unconditionally now (the pid announcement), so this is not a corner case.
	//
	// Only OSC is stripped. CSI and every other escape passes through untouched,
	// because those are exactly what the screen model needs in order to be a model
	// of the screen.
	visible []byte
}

// oscScanner extracts OSC window titles and working directories from a session's
// output stream.
//
// It is a byte-at-a-time state machine rather than a search over each chunk
// because chunk boundaries fall wherever the kernel put them: a sequence
// routinely arrives split across two reads, and any scanner that restarts per
// chunk drops exactly those. State carries across scan calls, so a sequence may be
// spread over any number of chunks.
//
// The zero value is ready to use. A scanner is owned by one goroutine (readLoop)
// and holds no lock of its own.
type oscScanner struct {
	state oscState
	buf   []byte
	// over records that this sequence's payload exceeded oscPayloadMax, so the
	// accumulated bytes are a prefix and must not be committed.
	over bool
	// kind is which string sequence is in flight; only strOSC is parsed.
	kind stringKind
	// elided counts bytes swallowed by the sequence in flight, so an unterminated
	// one cannot blind the detector forever.
	elided int
}

// scan feeds one chunk of session output and reports what it leaves in effect. A
// chunk can legitimately set the same fact several times (a prompt hook firing
// twice, a program setting then restoring); only the last one is current, so that
// is what is reported.
//
// A false has* means "this chunk said nothing about that", i.e. leave the
// session's current value alone.
func (s *oscScanner) scan(p []byte) oscUpdate {
	u := oscUpdate{visible: make([]byte, 0, len(p))}
	for _, b := range p {
		switch s.state {
		case oscGround:
			if b == 0x1b {
				// HELD, not emitted: whether this ESC is visible depends on the next
				// byte, which may not arrive until the next chunk. Holding it is what
				// lets an OSC be stripped whole without also eating a CSI.
				s.state = oscEsc
			} else {
				u.visible = append(u.visible, b)
			}
		case oscEsc:
			s.beginAfterEsc(b, &u, true)
		case oscBody:
			// Everything inside a string sequence is invisible, terminators included.
			s.elided++
			switch {
			case b == 0x07 && s.kind == strOSC:
				// BEL terminates OSC only. DCS/APC/PM/SOS end at ST, and treating a
				// BEL inside one as a terminator would end the sequence early and
				// spill the remainder of a binary payload onto the screen.
				s.commit(&u)
			case b == 0x1b: // maybe ESC \, the ST terminator
				s.state = oscBodyEsc
			case b == 0x18 || b == 0x1a: // CAN / SUB abort any sequence in flight
				s.reset()
			case s.elided > stringElideMax:
				// Unterminated past any plausible payload. Give up and resynchronise
				// rather than stay blind; the bytes after this are treated as text.
				s.reset()
			default:
				s.push(b)
			}
		case oscInterm:
			// Intermediates may repeat; the final (0x30-0x7e) ends it. Anything else
			// is malformed, and dropping it is the same answer.
			if b < 0x20 || b > 0x2f {
				s.state = oscGround
			}
		case oscBodyEsc:
			if b == '\\' { // ST: ESC \
				s.commit(&u)
				break
			}
			// Not ST, so the ESC belonged to a new sequence and this OSC was never
			// terminated. Abandon it and dispatch b as that sequence's second byte —
			// dropping it instead would lose a sequence that starts right here.
			// The ESC that aborted the string is NOT visible: it lived inside the
			// sequence we were eliding. It still introduces whatever comes next
			// (tmux passthrough doubles its inner ESCs, so this is the ordinary path
			// for that), which is why b is dispatched rather than dropped.
			s.reset()
			s.beginAfterEsc(b, &u, false)
		}
	}
	return u
}

// beginAfterEsc dispatches the byte following an ESC. Only OSC is tracked; every
// other escape sequence is of no interest, and consuming its second byte is enough
// to keep a `]` inside one from being mistaken for the start of an OSC.
//
// It is also where the held ESC is either dropped (an OSC begins, so the whole
// sequence is invisible) or released (anything else, which the screen must see).
// pendingVisible says whether the ESC that led here would itself have been
// painted. It is true in ground state and false when a string sequence was just
// aborted, because that ESC was part of the payload being elided.
func (s *oscScanner) beginAfterEsc(b byte, u *oscUpdate, pendingVisible bool) {
	switch {
	case b == ']':
		s.beginString(strOSC)
	case b == 'P' || b == '_' || b == '^' || b == 'X':
		// DCS, APC, PM, SOS. Their payloads are not ours to read — a sixel image, a
		// kitty graphics blob, a tmux passthrough — but they are string sequences
		// exactly like OSC, and the vt100 model paints every byte of them. PM and
		// SOS payloads were demonstrably matching the blocked rules; DCS and APC can
		// dump hundreds of kilobytes of image data onto the classification screen.
		s.beginString(strOther)
	case b == 0x1b:
		// ESC ESC: the first one is over and was not a string sequence, so it
		// becomes visible; the second is now the held one.
		if pendingVisible {
			u.visible = append(u.visible, 0x1b)
		}
		s.state = oscEsc
	case b == '\\':
		// A bare ST outside any string sequence — a no-op for a real terminal, and
		// left over here whenever a string sequence was abandoned early (tmux
		// passthrough doubles its inner ESCs, which breaks us out of the DCS before
		// its terminator arrives). Swallowed rather than painted as a stray "\".
		s.state = oscGround
	case b >= 0x20 && b <= 0x2f:
		// An nF escape: ESC, one or more intermediates (0x20-0x2f), then a final
		// (0x30-0x7e). Charset selection — "ESC ( B", "ESC ) 0" — is the common one
		// and ncurses programs emit it constantly. The vt100 model consumes only ESC
		// and the intermediate, then PAINTS the final, so every one of these left a
		// stray letter on the screen. Dropped whole instead: the model does not
		// implement charset selection, so there is nothing to lose by hiding it.
		s.state = oscInterm
	default:
		// An ordinary escape sequence — CSI above all, plus the two-byte ones like
		// ESC 7 / ESC M whose second byte IS the final. Both bytes are the screen's
		// business, and the rest of a CSI flows through in ground state, where the
		// model's own scanner terminates it correctly on a byte in 0x40-0x7e.
		u.visible = append(u.visible, 0x1b, b)
		s.state = oscGround
	}
}

// beginString enters a string sequence of the given kind.
func (s *oscScanner) beginString(k stringKind) {
	s.state = oscBody
	s.kind = k
	s.buf = s.buf[:0]
	s.over = false
	s.elided = 0
}

// push appends a payload byte, giving up on the payload past the cap while
// continuing to scan for the terminator.
func (s *oscScanner) push(b byte) {
	if len(s.buf) >= oscPayloadMax {
		s.over = true
		return
	}
	s.buf = append(s.buf, b)
}

func (s *oscScanner) reset() {
	s.state = oscGround
	s.buf = s.buf[:0]
	s.over = false
	s.kind = strNone
	s.elided = 0
}

// commit interprets a terminated OSC payload into u. The scanner returns to ground
// either way.
//
// An OSC payload is "Ps ; Pt". Ps 0 sets icon name AND window title and Ps 2 sets
// the window title; Ps 7 reports the working directory. Ps 1 is the icon name
// alone and every other Ps is something else entirely (OSC 8 hyperlinks and OSC
// 133 prompt marks both flow through here on a normal session), so they must be
// ignored rather than guessed at.
func (s *oscScanner) commit(u *oscUpdate) {
	body, over, kind := string(s.buf), s.over, s.kind
	s.reset()
	// Only OSC carries anything we read. The others are elided and discarded — we
	// are not a terminal, and a DCS reply or a graphics blob means nothing here.
	if over || kind != strOSC {
		return
	}
	i := strings.IndexByte(body, ';')
	if i < 0 {
		return
	}
	switch body[:i] {
	case "0", "2":
		// An EMPTY title is a real instruction — it is how a program CLEARS the
		// title — so this commits unconditionally.
		u.title, u.hasTitle = sanitizeTitle(body[i+1:]), true
	case "7":
		// A directory has no such "clear" form: OSC 7 with an unusable payload is
		// malformed, not an instruction to forget where we are. So unlike the
		// title, this commits ONLY on a successful parse, and a garbled sequence
		// leaves the last good directory standing.
		if dir, ok := parseCwdURI(body[i+1:]); ok {
			u.cwd, u.hasCwd = dir, true
		}
	case shells.PIDPs:
		// Ours, not a terminal's: the wrapper announces the session's pid here so
		// the BFF can read /proc from inside the container. The payload carries its
		// own tag, so another program using the same private code is ignored rather
		// than mistaken for a pid — see shells.ParsePID.
		if pid, ok := shells.ParsePID(body[:i], body[i+1:]); ok {
			u.pid, u.hasPID = pid, true
		}
	}
}

// parseCwdURI reads an OSC 7 payload into an absolute path.
//
// The conformant form is "file://<host>/<path>" with the path percent-encoded, and
// the host is deliberately ignored: inside a container it is the container's own
// hostname, which tells us nothing we do not already know, and rejecting on a
// mismatch would break every session whose shell reports a stale HOSTNAME.
//
// A bare absolute path is also accepted. It is not conformant, but emitters send
// it, and the alternative is discarding a directory we can read perfectly well.
func parseCwdURI(s string) (string, bool) {
	s = strings.TrimSpace(s)
	raw := s
	switch {
	case strings.HasPrefix(s, "file://"):
		// The path starts at the first "/" after the authority. Parsed by hand
		// rather than with url.Parse, which rejects payloads real emitters send
		// (an unescaped space in a path is common and perfectly readable).
		rest := s[len("file://"):]
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			return "", false
		}
		raw = rest[i:]
	case strings.HasPrefix(s, "/"):
	default:
		return "", false
	}
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return "", false // a malformed %-escape means we cannot know the path
	}
	// No leading-"/" check here: the switch above already rejected everything that
	// does not start with one, and in the file:// branch `raw` begins AT that
	// slash. A guard here would be unreachable, and an unreachable guard reads to
	// the next person as a tested invariant when nothing exercises it.
	if len(dec) > cwdMaxBytes {
		return "", false
	}
	// A path cannot contain NUL or a newline, and one that appears to is a framing
	// error somewhere upstream, not a directory. Rejecting beats sanitizing: a
	// silently truncated path names a REAL but DIFFERENT directory, which is the
	// one failure that could send a consumer somewhere the user never was.
	if strings.ContainsAny(dec, "\x00\n\r") {
		return "", false
	}
	if !utf8.ValidString(dec) {
		return "", false
	}
	return path.Clean(dec), true
}

// sanitizeTitle makes a title safe to store and to hand the UI as JSON: valid
// UTF-8, no control characters, bounded length. The payload comes from whatever
// runs in the container, so none of this can be assumed.
//
// Unlike a path this SANITIZES rather than rejects, because a title is decoration:
// a mangled one costs a confusing tab, where a mangled path could send a file pane
// somewhere real that the user never asked for.
func sanitizeTitle(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		// C0 and C1 controls, and DEL. A title is a label; a stray CR or NUL in it
		// is at best noise and at worst something a log or a tab renders oddly.
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > titleMaxRunes {
		count := 0
		for i := range s {
			if count == titleMaxRunes {
				return s[:i]
			}
			count++
		}
	}
	return s
}
