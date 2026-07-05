package webbff

// Reading a session's foreground program and directory out of /proc, for the
// sessions whose shells never say (see osc.go for the ones that do).
//
// OSC is the cheap answer and always preferred: it costs nothing and arrives on
// the stream we already read. But a title is only emitted by a shell whose rc sets
// one, and OSC 7 essentially never is — the hook that emits it ships in
// /etc/profile.d/vte.sh on a desktop and in no stock container image. That leaves
// most real sessions with a tab named after the shell binary and no directory at
// all, which is the gap this closes.
//
// The anchor is the pid the launch wrapper announced (pkg/shells.WrapAnnouncePID),
// expressed in the CONTAINER's PID namespace. That is what makes this work on every
// backend: api.ExecState.Pid is a HOST pid — every existing reader of it goes to
// the host's /proc (barehost/copy_linux.go, containerdhost/stats_linux.go) — so
// inside a container with its own namespace it names a different process or none,
// and kubernetes and incus report 0 for it regardless.
//
// Cost is bounded by making the probe LAZY and cached: it runs only when the
// session list is actually being asked for, never blocks that request, and stops
// entirely when no browser is polling. A session that OSC already answers for is
// never probed at all.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"cornus/pkg/client"
)

const (
	// procProbeTTL is how long one probe's answer is reused. It is a little longer
	// than the browser's 2s session poll, so a steadily-polling UI issues about one
	// probe per session per interval rather than one per request.
	procProbeTTL = 3 * time.Second
	// procProbeTimeout bounds one probe. It is a two-file read inside a container
	// that is already running; anything slower than this is a backend in trouble,
	// and holding a goroutine and an exec open for it helps nobody.
	procProbeTimeout = 5 * time.Second
	// procProbeMaxOutput bounds what a probe may return. Two short lines is the
	// honest answer; the cap is what stops a hostile /proc read from being a way to
	// stream bytes into the BFF's memory.
	procProbeMaxOutput = 8 << 10
)

// procProbeScript reads the FOREGROUND process's identity and directory, given the
// session shell's pid as $1.
//
// It reports three things: the working directory, the kernel's short process
// name, and the full argv. The argv is not redundant — see procInfo.argv.
//
// The foreground process is found through the shell's controlling terminal rather
// than by walking children: `tpgid` in /proc/<pid>/stat is the process group the
// terminal currently belongs to, which is exactly "what is the user looking at" —
// the shell itself at a prompt, or vim while vim is running.
//
// Splitting the stat line on the LAST ")" is not fussiness. Field 2 is the
// executable's name in parentheses and may itself contain spaces AND parentheses
// (a process is free to set it), so cutting at the first ")" or counting fields
// from the left both misread the line for such a process — and misreading it here
// yields a plausible wrong pid, which is worse than no answer. After the last ")"
// the fields are fixed: state ppid pgrp session tty_nr tpgid, so tpgid is $6.
//
// Every step is guarded because every step can legitimately fail: the shell may
// have exited between the poll and the probe, and a session with no controlling
// terminal reports tpgid -1. Printing nothing is the correct answer then.
const procProbeScript = `p=$1
[ -r "/proc/$p/stat" ] || exit 0
read -r line < "/proc/$p/stat" || exit 0
rest=${line##*)}
set -- $rest
tp=$6
case "$tp" in ''|*[!0-9]*) exit 0;; esac
[ "$tp" -gt 0 ] || exit 0
printf 'cwd=%s\n' "$(readlink "/proc/$tp/cwd" 2>/dev/null)"
printf 'comm=%s\n' "$(cat "/proc/$tp/comm" 2>/dev/null)"
tr '\000' '\n' < "/proc/$tp/cmdline" 2>/dev/null | while IFS= read -r a; do
  printf 'arg=%s\n' "$a"
done
exit 0`

// procCapturer runs one short command inside a workload and returns its stdout.
//
// It is an interface rather than a direct call so the terminal manager stays
// testable without a daemon: the exec STREAM fake models one long-lived session
// and cannot also serve one-shot captures, and conflating the two would make every
// terminal test depend on how the probe happens to be spelled.
type procCapturer interface {
	Capture(ctx context.Context, workload string, cmd []string) (string, error)
}

// clientCapturer is the production procCapturer: one bounded exec through the same
// helper the file browser's directory listing uses, so the probe inherits its
// output capping and exit handling rather than growing a second copy of them.
type clientCapturer struct{ cl *client.Client }

func (c clientCapturer) Capture(ctx context.Context, workload string, cmd []string) (string, error) {
	res, err := execCapture(ctx, c.cl, workload, "", cmd, procProbeMaxOutput)
	if err != nil {
		return "", err
	}
	// stderr is deliberately ignored. The script silences its own reads and exits
	// 0 whatever happens, so anything on stderr is the container being unusual;
	// stdout is either the two lines we asked for or nothing.
	return res.Stdout, nil
}

// procInfo is what one probe learned. Both fields are best-effort and independent:
// a process can have a readable comm and an unreadable cwd (a directory it no
// longer has permission to resolve), and reporting one is better than neither.
type procInfo struct {
	cwd  string
	comm string
	// argv is the foreground process's command line. It is what `comm` cannot be:
	// comm is 15 bytes and names the INTERPRETER's thread ("node-MainThread" for
	// any Node program), so an agent behind a runtime is only identifiable from
	// the argv that runtime was given.
	argv []string
}

// probeProc runs the script once and parses its two lines. It returns a zero
// procInfo rather than an error for anything unrecognised: this is a nicety, and
// the caller's only sensible response to a failed probe is to keep the last answer.
// interp is the shell to run the script with. The caller passes the session's OWN
// shell, which is the only interpreter the image is known to have: a distroless
// image that ships bash and no /bin/sh runs terminals perfectly well today, and a
// probe hardcoded to /bin/sh would be silently dead in exactly that case.
func probeProc(ctx context.Context, cap procCapturer, workload string, interp []string, pid int) procInfo {
	if len(interp) == 0 {
		return procInfo{}
	}
	ctx, cancel := context.WithTimeout(ctx, procProbeTimeout)
	defer cancel()
	// The pid travels as an ARGUMENT, never spliced into the script text — the
	// same rule fs.go's listScriptCmd follows. It is an integer we parsed
	// ourselves, so this is belt-and-braces, but the belt is what survives someone
	// later widening where the pid comes from.
	cmd := make([]string, 0, len(interp)+4)
	cmd = append(cmd, interp...)
	cmd = append(cmd, "-c", procProbeScript, "sh", strconv.Itoa(pid))
	out, err := cap.Capture(ctx, workload, cmd)
	if err != nil {
		return procInfo{}
	}
	if len(out) > procProbeMaxOutput {
		out = out[:procProbeMaxOutput]
	}
	var info procInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "cwd="):
			// readlink reports a deleted directory as "/gone (deleted)". That is a
			// place nothing can be opened at, so it is not an answer.
			v := strings.TrimPrefix(line, "cwd=")
			if strings.HasPrefix(v, "/") && !strings.HasSuffix(v, " (deleted)") {
				info.cwd = v
			}
		case strings.HasPrefix(line, "arg="):
			info.argv = append(info.argv, strings.TrimPrefix(line, "arg="))
		case strings.HasPrefix(line, "comm="):
			// comm is the kernel's 15-char process name, so it is already short and
			// control-free; trimming is for the TTY's line endings, not hygiene.
			info.comm = strings.TrimSpace(strings.TrimPrefix(line, "comm="))
		}
	}
	return info
}
