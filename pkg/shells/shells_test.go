package shells

import (
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestFromArgv(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		ok   bool
	}{
		{"plain shell", []string{"/bin/bash"}, []string{"/bin/bash"}, true},
		{"shell with args takes argv0 only", []string{"/bin/sh", "-c", "exec app"}, []string{"/bin/sh"}, true},
		{"busybox needs its applet", []string{"/bin/busybox", "sh"}, []string{"/bin/busybox", "sh"}, true},
		{"busybox without a shell applet", []string{"/bin/busybox", "httpd"}, nil, false},
		{"bare busybox", []string{"/bin/busybox"}, nil, false},
		{"ordinary program", []string{"/usr/bin/python3"}, nil, false},
		{"application with args", []string{"/app/server", "--port", "80"}, nil, false},
		// A wrapper script is not a shell even though its name ends in .sh — the
		// match is on the whole basename, not on a suffix.
		{"entrypoint script", []string{"/docker-entrypoint.sh"}, nil, false},
		{"empty", nil, nil, false},
		{"empty argv0", []string{""}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FromArgv(tc.in)
			if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FromArgv(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestSplit(t *testing.T) {
	if got := Split("/bin/busybox sh"); !reflect.DeepEqual(got, []string{"/bin/busybox", "sh"}) {
		t.Fatalf("Split = %q", got)
	}
	if got := Split(`"/opt/my shell/sh"`); !reflect.DeepEqual(got, []string{"/opt/my shell/sh"}) {
		t.Fatalf("Split of a quoted path with a space = %q", got)
	}
	if got := Split("   "); got != nil {
		t.Fatalf("Split of blank = %q, want nil", got)
	}
}

// Which argvs may be wrapped at all. Everything refused here launches exactly as
// it does today, which is the property that makes wrapping safe to do by default.
func TestWrapAnnouncePIDRefusesWhatItCannotRun(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		ok   bool
	}{
		{"sh", []string{"/bin/sh"}, true},
		{"bash with args", []string{"/bin/bash", "-l"}, true},
		{"busybox sh", []string{"/bin/busybox", "sh"}, true},
		// fish is a real interactive shell and IS in the shell set, but has no $$,
		// so wrapping it would announce nothing while still paying the cost.
		{"fish refused despite being a shell", []string{"/usr/bin/fish"}, false},
		{"ordinary program refused", []string{"/usr/bin/top"}, false},
		{"empty refused", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WrapAnnouncePID(tc.in)
			if ok != tc.ok {
				t.Fatalf("WrapAnnouncePID(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if !ok {
				return
			}
			// The interpreter must be argv[0] itself. Wrapping through a guessed
			// /bin/sh would break a distroless image that ships bash and no sh.
			if got[0] != tc.in[0] {
				t.Fatalf("wrapped interpreter = %q, want argv[0] %q", got[0], tc.in[0])
			}
		})
	}
	// fish must still be recognised as a SHELL — the refusal above is about the
	// wrapper's script, not about what fish is. Asserted so a future edit cannot
	// "fix" the refusal by quietly dropping fish from the shell set.
	if !IsShell("fish") {
		t.Fatal("fish stopped being a shell; the wrap refusal must not be spelled that way")
	}
}

// The load-bearing property, executed rather than reasoned about: the pid the
// wrapper prints BEFORE `exec` is the pid of the process that comes AFTER it.
// Everything downstream (reading /proc from inside the container) is wrong if this
// is wrong, and it is exactly the kind of claim that reads as obviously true and
// would silently stop holding if the script ever forked instead of exec'ing.
func TestWrapAnnouncePIDReportsTheFinalProcessPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell wrapper")
	}
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		t.Skipf("no %s on this host", sh)
	}
	// The wrapped command prints its OWN pid, so the two numbers can be compared.
	// It is a shell so that it is wrappable; what it runs is beside the point.
	argv, ok := WrapAnnouncePID([]string{sh, "-c", `printf 'final=%s' $$`})
	if !ok {
		t.Fatal("WrapAnnouncePID refused /bin/sh")
	}
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("running the wrapped argv: %v (output %q)", err, out)
	}

	announced := regexp.MustCompile(`\x1b\]` + PIDPs + `;` + regexp.QuoteMeta(pidTag) + `(\d+)\x07`).
		FindSubmatch(out)
	if announced == nil {
		t.Fatalf("no pid OSC in output %q", out)
	}
	final := regexp.MustCompile(`final=(\d+)`).FindSubmatch(out)
	if final == nil {
		t.Fatalf("the wrapped command did not run; output %q", out)
	}
	if string(announced[1]) != string(final[1]) {
		t.Fatalf("announced pid %s but the exec'd process was %s — `exec` is not replacing in place",
			announced[1], final[1])
	}
	// And the announcement is parseable by the reader that has to consume it.
	if _, ok := ParsePID(PIDPs, pidTag+string(announced[1])); !ok {
		t.Fatalf("ParsePID rejected the payload the wrapper emits")
	}
}

// An argv is re-run EXACTLY as asked however it is spelled — the quoting is what
// stands between a path with a space in it and a shell splitting it into two
// arguments that name nothing.
func TestWrapAnnouncePIDPreservesAwkwardArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell wrapper")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}
	// A space, a single quote and a dollar sign: the three things naive quoting
	// gets wrong, and all three are legal in an argument.
	want := `it's $HOME and a space`
	argv, ok := WrapAnnouncePID([]string{"/bin/sh", "-c", `printf '%s' "$1"`, "sh", want})
	if !ok {
		t.Fatal("WrapAnnouncePID refused /bin/sh")
	}
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("running the wrapped argv: %v (output %q)", err, out)
	}
	// Strip the pid announcement; what remains must be the argument verbatim —
	// unsplit, unexpanded, unmangled.
	body := regexp.MustCompile(`\x1b\][^\x07]*\x07`).ReplaceAllString(string(out), "")
	if body != want {
		t.Fatalf("wrapped argv round-tripped to %q, want %q", body, want)
	}
}

func TestParsePID(t *testing.T) {
	cases := []struct {
		ps, text string
		want     int
		ok       bool
	}{
		{PIDPs, pidTag + "1234", 1234, true},
		{PIDPs, pidTag + " 1234 ", 1234, true},
		// Another program using the same private code must not be read as a pid.
		{PIDPs, "something-else", 0, false},
		{PIDPs, pidTag + "0", 0, false},
		{PIDPs, pidTag + "-3", 0, false},
		{PIDPs, pidTag + "notanumber", 0, false},
		{"0", pidTag + "1234", 0, false},
		{"7", pidTag + "1234", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParsePID(tc.ps, tc.text)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ParsePID(%q, %q) = (%d, %v), want (%d, %v)", tc.ps, tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

// The script must not carry a literal ESC: it travels as an argv through an HTTP
// body, a backend API and a container runtime, and a raw control byte is the kind
// of thing one of those hops normalises. The shell's printf expands the escape.
func TestWrapAnnouncePIDScriptCarriesNoRawControlBytes(t *testing.T) {
	argv, ok := WrapAnnouncePID([]string{"/bin/sh"})
	if !ok {
		t.Fatal("WrapAnnouncePID refused /bin/sh")
	}
	script := argv[len(argv)-1]
	if strings.ContainsAny(script, "\x1b\x07") {
		t.Fatalf("script carries a raw control byte: %q", script)
	}
	if !strings.Contains(script, `\033`) || !strings.Contains(script, `\007`) {
		t.Fatalf("script lost its printf escapes: %q", script)
	}
}
