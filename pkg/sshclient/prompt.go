package sshclient

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// NewInteractivePrompt returns a passphrase-prompt callback suitable for the first
// (foreground) connect, or nil when no interactive method is available (so the
// caller fails closed instead of hanging on a reconnect). It honors OpenSSH's
// SSH_ASKPASS / SSH_ASKPASS_REQUIRE, falling back to the controlling terminal:
//
//   - SSH_ASKPASS_REQUIRE=force  — always use SSH_ASKPASS (nil if it is unset)
//   - SSH_ASKPASS_REQUIRE=prefer — use SSH_ASKPASS when set, else the TTY
//   - SSH_ASKPASS_REQUIRE=never  — always use the TTY
//   - unset — use SSH_ASKPASS only when there is no controlling terminal
//
// It is a var so tests can substitute a deterministic prompt.
var NewInteractivePrompt = func() func(keyPath string) ([]byte, error) {
	askpass := os.Getenv("SSH_ASKPASS")
	switch os.Getenv("SSH_ASKPASS_REQUIRE") {
	case "force":
		if askpass == "" {
			return nil
		}
		return askpassPrompt(askpass)
	case "prefer":
		if askpass != "" {
			return askpassPrompt(askpass)
		}
	case "never":
		// fall through to the TTY
	default:
		if askpass != "" && !haveTTY() {
			return askpassPrompt(askpass)
		}
	}
	if haveTTY() {
		return ttyPrompt()
	}
	return nil
}

// askpassPrompt runs the SSH_ASKPASS program with the prompt as its argument and
// reads the passphrase from its stdout (OpenSSH convention).
func askpassPrompt(prog string) func(string) ([]byte, error) {
	return func(keyPath string) ([]byte, error) {
		out, err := exec.Command(prog, fmt.Sprintf("Enter passphrase for %s: ", keyPath)).Output()
		if err != nil {
			return nil, fmt.Errorf("SSH_ASKPASS program %s: %w", prog, err)
		}
		return []byte(strings.TrimRight(string(out), "\r\n")), nil
	}
}

// ttyPrompt reads the passphrase from the controlling terminal with echo off.
func ttyPrompt() func(string) ([]byte, error) {
	return func(keyPath string) ([]byte, error) {
		f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			f = os.Stdin
		} else {
			defer f.Close()
		}
		fmt.Fprintf(f, "Enter passphrase for %s: ", keyPath)
		pass, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(f)
		if err != nil {
			return nil, err
		}
		return pass, nil
	}
}

// haveTTY reports whether a controlling terminal is available for prompting.
func haveTTY() bool {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		_ = f.Close()
		return true
	}
	return term.IsTerminal(int(os.Stdin.Fd()))
}
