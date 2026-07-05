package main

import (
	"context"
	"fmt"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/hostcheck"
	"cornus/pkg/server"
)

// PreflightCmd reports whether this process could actually drive the configured
// deploy backend's container runtime — the same detection and the same checks
// `cornus serve` runs at startup, so it answers for the real server rather than
// an approximation of it.
//
// It exists to be run BEFORE committing to a deployment: inside the container
// image you are about to run, with the same mounts and the same environment,
// `cornus daemon preflight` says whether the binds are right while it is still
// cheap to change them. Exits non-zero when the configuration is one `cornus
// serve` would refuse to start on, so a CI job or an image smoke test can gate
// on it.
type PreflightCmd struct{}

// Run evaluates the host environment and prints the verdict.
func (c *PreflightCmd) Run(cli *CLI, d *cliout.Driver) error {
	res, err := server.HostPreflight(context.Background(), cli.resolveConfig())
	if err != nil {
		return err
	}
	out := preflightResult{Summary: res.Summary(), OK: !res.Failed()}
	for _, ck := range res.Checks {
		out.Checks = append(out.Checks, preflightCheck{
			Name: ck.Name, Status: string(ck.Status), Detail: ck.Detail, Remedy: ck.Hint,
		})
	}
	if err := d.Emit(out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("host environment unusable for the configured deploy backend")
	}
	return nil
}

// preflightResult is the structured result of `cornus daemon preflight`: a
// per-check list in plain/fancy mode, one JSON object in json mode.
type preflightResult struct {
	Summary string           `json:"summary"`
	OK      bool             `json:"ok"`
	Checks  []preflightCheck `json:"checks,omitempty"`
}

type preflightCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Remedy string `json:"remedy,omitempty"`
}

func (r preflightResult) Human(p cliout.Printer) {
	p.Line("%s", r.Summary)
	for _, c := range r.Checks {
		// The colocation note restates the summary; do not print it twice.
		if c.Name == hostcheck.CheckColocation {
			continue
		}
		p.Line("  [%-4s] %s: %s", c.Status, c.Name, c.Detail)
		if c.Remedy != "" {
			p.Line("           remedy: %s", c.Remedy)
		}
	}
	if !r.OK {
		p.Line("")
		p.Line("`cornus serve` would refuse to start in this environment.")
	}
}
