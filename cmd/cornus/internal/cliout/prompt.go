package cliout

import (
	"bufio"
	"fmt"
	"strings"
)

// Confirm asks a yes/no question and reports the answer. When stdin is not a
// terminal it returns defaultYes without prompting, so scripts and CI stay
// deterministic and the prompt only appears interactively. Otherwise it
// delegates to d.Prompt (overridable in tests). This preserves the exact
// contract of the former config.confirmSetDefaultContext var.
func (d *Driver) Confirm(question string, defaultYes bool) bool {
	if !d.inTTY {
		return defaultYes
	}
	if d.Prompt == nil {
		return defaultYes
	}
	return d.Prompt(question, defaultYes)
}

// terminalPrompt is the default Prompt: it writes the question to stderr and
// reads a line from stdin, treating an empty line as the default.
func (d *Driver) terminalPrompt(question string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	fmt.Fprintf(d.err, "%s %s ", question, suffix)
	line, _ := bufio.NewReader(d.in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes
	case "y", "yes":
		return true
	default:
		return false
	}
}
