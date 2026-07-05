package setupwiz

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"cornus/cmd/cornus/internal/cliout"
)

// plainUI is the line-oriented fallback UI. It renders every prompt to the
// driver's diagnostics stream (stderr, channel discipline) and reads answers from
// ONE bufio.Reader held for the whole wizard lifetime. A fresh reader per prompt
// (as cliout.terminalPrompt uses) would be correct for a single question but would
// silently swallow any bytes it buffered past the newline — fatal for a piped,
// multi-question wizard where several answers arrive in one write. EOF or a read
// error at any prompt maps to ErrAborted, which unwinds the flow without saving.
type plainUI struct {
	d *cliout.Driver
	r *bufio.Reader
	w io.Writer
}

// backToken is the line a user types at any plain-UI prompt to go back to the
// previous step (the line-mode analogue of Esc in the rich UI).
const backToken = "<"

func newPlainUI(d *cliout.Driver) *plainUI {
	return &plainUI{d: d, r: bufio.NewReader(d.Stdin()), w: d.Err()}
}

// readLine reads one line from the shared reader, trimming the trailing newline.
// It reports ErrAborted on EOF or any read error (both mean "no more input"), so
// a truncated script aborts rather than materializing a silently-wrong profile.
// Going back in plain mode is the explicit "<" token, not Ctrl-D.
func (u *plainUI) readLine() (string, error) {
	line, err := u.r.ReadString('\n')
	if err != nil {
		// A final line without a trailing newline still carries its content; only
		// treat it as an abort when nothing was read.
		if line == "" {
			return "", ErrAborted
		}
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (u *plainUI) Note(format string, a ...any) {
	fmt.Fprintf(u.w, format+"\n", a...)
}

func (u *plainUI) Select(title, help string, opts []Option, def int) (int, error) {
	for {
		fmt.Fprintln(u.w, title)
		if help != "" {
			fmt.Fprintln(u.w, "  "+help)
		}
		for i, o := range opts {
			marker := " "
			if i == def {
				marker = "*"
			}
			line := fmt.Sprintf("  %s %d) %s", marker, i+1, o.Label)
			if o.Desc != "" {
				line += " — " + o.Desc
			}
			fmt.Fprintln(u.w, line)
		}
		fmt.Fprintf(u.w, "Select [%d] (< to go back): ", def+1)

		line, err := u.readLine()
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == backToken {
			return 0, ErrBack
		}
		if line == "" {
			return def, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(opts) {
			fmt.Fprintf(u.w, "please enter a number between 1 and %d\n", len(opts))
			continue
		}
		return n - 1, nil
	}
}

func (u *plainUI) Input(q Question) (string, error) {
	for {
		fmt.Fprint(u.w, q.Title)
		if q.Example != "" {
			fmt.Fprintf(u.w, " (e.g. %s)", q.Example)
		}
		if q.Default != "" {
			fmt.Fprintf(u.w, " [%s]", q.Default)
		}
		fmt.Fprintln(u.w)
		if q.Help != "" {
			fmt.Fprintln(u.w, "  "+q.Help)
		}
		if q.Secret {
			fmt.Fprintln(u.w, "  (input is not hidden here)")
		}
		fmt.Fprint(u.w, "> ")

		line, err := u.readLine()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(line) == backToken {
			return "", ErrBack
		}
		val := strings.TrimSpace(line)
		if val == "" {
			val = q.Default
		}
		if q.Validate != nil {
			if verr := q.Validate(val); verr != nil {
				fmt.Fprintf(u.w, "  invalid: %v\n", verr)
				continue
			}
		}
		return val, nil
	}
}

func (u *plainUI) Confirm(question string, defaultYes bool) (bool, error) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	for {
		fmt.Fprintf(u.w, "%s %s ", question, suffix)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case backToken:
			return false, ErrBack
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(u.w, "please answer y or n")
		}
	}
}
