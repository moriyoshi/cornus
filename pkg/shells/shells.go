// Package shells answers "is this argv an interactive shell, and which one?",
// and wraps a shell launch so the session announces its own process id.
//
// It sits in pkg/ rather than in the web BFF because both ends of the control
// plane need it and they must agree. The BFF asks it which shells to probe for
// and which programs are shells rather than agents; the SERVER asks it whether an
// exec can safely be wrapped, right beside where it injects SSH_AUTH_SOCK for a
// forwarded agent (pkg/server/deploy_exec.go). Two copies of "is this a shell?"
// that disagreed would produce an exec the server wrapped and the BFF did not
// expect, which is the one failure mode that breaks a terminal outright.
package shells

import (
	"path"
	"strconv"
	"strings"

	shellwords "github.com/mattn/go-shellwords"
)

// basenames is the set of program basenames treated as an interactive shell.
// Shared by the BFF's shell discovery and its agent detection (a plain shell is
// not an "agent"), so the two answers cannot drift apart.
var basenames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "ash": true, "dash": true, "fish": true,
}

// posixBasenames is the subset whose shells spell `-c`, `exec` and `$$` the POSIX
// way, which is what WrapAnnouncePID's script is written in.
//
// fish is deliberately absent even though it is a perfectly good interactive
// shell: it has no `$$` (the current pid is `$fish_pid`), so the wrapper would
// announce an empty pid there and the session would pay the cost of being wrapped
// for nothing.
var posixBasenames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "ash": true, "dash": true,
}

const (
	// PIDPs is the OSC parameter a wrapped session announces its pid under. It is
	// in the private range no terminal acts on, so the sequence is invisible in
	// any emulator and harmless in the replay ring.
	PIDPs = "5379"

	// pidTag makes the payload self-identifying. The private OSC range is not
	// reserved for us, so a program inside the container could use the same
	// number for something else; without the tag its payload would be read as a
	// pid, and a wrong pid points /proc at somebody else's process.
	pidTag = "cornus-pid="
)

// IsShell reports whether a program BASENAME names an interactive shell.
func IsShell(base string) bool { return basenames[base] }

// Split splits one candidate STRING into argv with the same parser Compose uses
// for command/entrypoint (pkg/compose/types.go), so "/bin/busybox sh" is two words
// and a quoted path with a space in it is one.
func Split(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	argv, err := shellwords.Parse(s)
	if err != nil || len(argv) == 0 {
		return nil
	}
	return argv
}

// FromArgv reports the shell a container's entrypoint/command implies, if any.
// Only argv[0] is taken: the point is "this image demonstrably has this shell",
// not "re-run the app's startup line" — for ["/bin/sh","-c","exec app"] the
// candidate is /bin/sh, not the whole thing.
//
// The busybox case is the one where argv[0] alone is not runnable as a shell:
// /bin/busybox needs its applet name, so both words travel.
func FromArgv(argv []string) ([]string, bool) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, false
	}
	base := path.Base(argv[0])
	if base == "busybox" && len(argv) > 1 && basenames[argv[1]] {
		return []string{argv[0], argv[1]}, true
	}
	if basenames[base] {
		return []string{argv[0]}, true
	}
	return nil, false
}

// WrapAnnouncePID returns an argv that prints its own pid as an OSC sequence and
// then BECOMES argv, or false if argv is not something we can safely wrap.
//
// The pid matters because it is the only anchor for reading /proc from inside the
// container, and it is the only one available on every backend. api.ExecState.Pid
// is a HOST pid (see barehost/copy_linux.go, containerdhost/copy_linux.go, which
// read it from the host's /proc), so inside a container with its own PID namespace
// that number names a different process or none; and kubernetes and incus report 0
// for it regardless. `$$` captured by the wrapper is the pid as the CONTAINER
// numbers it, which is what an auxiliary exec into that container can use.
//
// The wrapper's pid survives because `exec` replaces the process in place rather
// than forking: the pid printed before the exec is the pid of the shell that comes
// after it. That is the whole trick, and it is why the printf must come first.
//
// Two properties make this safe to do by default on a shell:
//
//   - The INTERPRETER is argv[0] itself, never a guessed /bin/sh. We were about to
//     execute argv[0], so it demonstrably exists; wrapping through /bin/sh would
//     break a distroless image that ships bash and no sh, which works today.
//   - A failing printf costs nothing. `exec` runs regardless, so the worst case of
//     an image with no working printf is a session with no announced pid — the
//     same state as an unwrapped session, not a broken one.
//
// It returns false rather than guessing for anything that is not a POSIX shell,
// including fish and any ordinary program: those launch exactly as they do today.
func WrapAnnouncePID(argv []string) ([]string, bool) {
	interp, ok := posixInterpreter(argv)
	if !ok {
		return nil, false
	}
	// \033 and \007 are left for the SHELL's printf to interpret, so they are
	// backslash escapes in the script text rather than literal control bytes: a
	// literal ESC embedded in an argv survives fewer round trips intact.
	script := `printf '\033]` + PIDPs + `;` + pidTag + `%s\007' $$` + "\n" +
		"exec " + quoteArgv(argv)
	out := make([]string, 0, len(interp)+2)
	out = append(out, interp...)
	return append(out, "-c", script), true
}

// ParsePID reads a pid out of an OSC payload, or false if this is not one of ours.
func ParsePID(ps, text string) (int, bool) {
	if ps != PIDPs || !strings.HasPrefix(text, pidTag) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(text, pidTag)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// posixInterpreter picks the shell to run the wrapper script with: argv[0] itself,
// which is the only interpreter we know the image has.
func posixInterpreter(argv []string) ([]string, bool) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, false
	}
	base := path.Base(argv[0])
	if base == "busybox" && len(argv) > 1 && posixBasenames[argv[1]] {
		return []string{argv[0], argv[1]}, true
	}
	if posixBasenames[base] {
		return []string{argv[0]}, true
	}
	return nil, false
}

// quoteArgv renders argv as a single-quoted shell word list, so `exec` re-runs
// exactly the argv that was asked for however it is spelled. Single quotes are
// literal in POSIX sh for every byte except the quote itself, which is why the
// only escape needed is the '\” dance.
func quoteArgv(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}
