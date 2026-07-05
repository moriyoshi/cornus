// Package setupwiz implements `cornus setup`: an interactive wizard that picks a
// deployment scenario, asks only the questions that scenario needs, materializes
// a connection profile (a clientconfig "context"), optionally verifies it, and
// prints scenario-tailored next-steps guidance plus optional setup artifacts. It
// is a guided front-end over `cornus config set-context` — no new profile
// semantics.
//
// It lives in an internal package (not package main), like clientconn/cliout, so
// it can be unit-tested and reused; it imports cliout, clientconn, clientconfig,
// and svcforward, but never package main.
//
// Interaction model: one UI interface with two implementations — teaUI (rich
// bubbletea dialogs on a real TTY) and plainUI (deterministic line prompts). The
// flow (wizard.go) is a UI-agnostic imperative function so both implementations,
// and the scripted test stub, drive the exact same sequence of questions. The
// wizard only ever narrates via cliout Step/Done/Warn/Info; it never runs a
// cliout.Progress concurrently, so stdin ownership is unambiguous.
package setupwiz

import (
	"errors"

	"cornus/cmd/cornus/internal/cliout"
)

// ErrAborted is returned by any UI method when the user aborts the wizard
// (Ctrl-C in the rich UI, EOF / read error in the plain UI). The flow unwinds
// immediately on it, and because materialization is a single late atomic point,
// an abort never leaves partial state on disk.
var ErrAborted = errors.New("setup aborted")

// ErrBack is returned by a UI method when the user asks to go back to the
// previous step (Esc in the rich UI, "<" in the plain UI). The flow's step
// runner catches it and re-asks the previous question instead of aborting; from
// the first question it unwinds to the scenario picker.
var ErrBack = errors.New("go back")

// Question describes a single free-text prompt.
type Question struct {
	// Title is the prompt shown to the user.
	Title string
	// Help is optional secondary text shown beneath the title.
	Help string
	// Default is returned when the user submits an empty answer.
	Default string
	// Example illustrates the expected input (e.g. "https://cornus.example.com").
	// The rich UI shows it as placeholder ghost text when there is no Default; the
	// plain UI appends it to the prompt as "(e.g. ...)". It is never submitted.
	Example string
	// Secret masks the input (passwords/tokens): the rich UI echoes '*', the
	// plain UI warns that input is not hidden, and transcripts show '********'.
	Secret bool
	// Validate, when non-nil, is called on the resolved answer; a non-nil error
	// re-asks the question (rich) or is reported and re-asked (plain).
	Validate func(string) error
}

// Option is one choice in a Select.
type Option struct {
	// Label is the short choice text.
	Label string
	// Desc is optional one-line description shown beside/under the label.
	Desc string
}

// NewUI selects the interaction implementation for the driver: the rich
// bubbletea UI on a full interactive terminal (fancy mode with both stdin and
// stderr terminals — the stdin gate stops a raw-mode read on a pipe), else the
// deterministic line-prompt UI. JSON mode never reaches here (SetupCmd.Run
// rejects it up front, since prompts would corrupt NDJSON).
func NewUI(d *cliout.Driver) UI {
	if d.Mode() == cliout.ModeFancy && d.InTTY() && d.ErrTTY() {
		return newTeaUI(d)
	}
	return newPlainUI(d)
}

// UI is the wizard's interaction surface. Select/Input/Confirm may each return
// ErrAborted (abort the wizard) or ErrBack (return to the previous step).
type UI interface {
	// Select asks the user to pick one of opts, starting the cursor at def, and
	// returns the chosen index.
	Select(title, help string, opts []Option, def int) (int, error)
	// Input asks a free-text question and returns the answer (Default when empty).
	Input(q Question) (string, error)
	// Confirm asks a yes/no question and returns the answer.
	Confirm(question string, defaultYes bool) (bool, error)
	// Note prints a neutral informational line (not a prompt).
	Note(format string, a ...any)
}
