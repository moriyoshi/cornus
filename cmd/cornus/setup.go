package main

import (
	"errors"
	"fmt"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/setupwiz"
)

// SetupCmd is the interactive configuration wizard: it picks a deployment
// scenario, asks only that scenario's questions, materializes a connection
// profile (a context), optionally verifies it, and prints next-steps guidance
// plus optional setup artifacts. It is a guided front-end over `cornus config
// set-context` — see cmd/cornus/internal/setupwiz.
type SetupCmd struct{}

// Run resolves the config path and drives the wizard. It refuses json mode (an
// interactive flow would corrupt NDJSON) and reports it, pointing at the
// scriptable path; --output plain forces line prompts as the accessibility /
// non-TTY escape hatch.
func (c *SetupCmd) Run(cli *CLI) error {
	d := cli.out()
	if d.Mode() == cliout.ModeJSON {
		return fmt.Errorf("setup is interactive and cannot run in --output json mode; use 'cornus config set-context' for scripting")
	}
	path, err := cli.configPath()
	if err != nil {
		return err
	}
	wiz := setupwiz.NewWizard(d, setupwiz.NewUI(d), path)
	if err := wiz.Run(cli.rootContext()); err != nil {
		if errors.Is(err, setupwiz.ErrAborted) {
			d.Warn("setup aborted")
			return nil
		}
		return err
	}
	return nil
}
