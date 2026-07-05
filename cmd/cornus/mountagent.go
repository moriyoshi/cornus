package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"cornus/pkg/caretaker"
)

// MountAgentCmd is the DEPRECATED single-mount sidecar, kept as a thin alias over
// the caretaker (which the k8s backend now emits directly). It mounts one
// caller-local 9P export, relayed by a cornus server, at Target inside the pod
// and holds it until teardown.
type MountAgentCmd struct {
	Server   string `kong:"required,help='cornus server URL (ws(s):// or http(s)://) that relays the 9P export.'"`
	Session  string `kong:"required,help='Deploy-attach session id.'"`
	Name     string `kong:"required,help='9P backing name to pull.'"`
	Target   string `kong:"required,help='Mount point inside this container.'"`
	ReadOnly bool   `kong:"name='read-only',help='Mount read-only.'"`
}

// Run mounts the single export via the caretaker runtime until SIGINT/SIGTERM.
func (c *MountAgentCmd) Run(cli *CLI) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdown, err := setupCaretakerObservability(ctx)
	if err != nil {
		return err
	}
	defer shutdown()
	return caretaker.Run(ctx, caretaker.Config{Mounts: []caretaker.MountRole{{
		Server:   c.Server,
		Session:  c.Session,
		Name:     c.Name,
		Target:   c.Target,
		ReadOnly: c.ReadOnly,
	}}})
}

// MountcheckCmd exits 0 iff Target is a live mountpoint. DEPRECATED alias kept
// for older sidecars; the caretaker uses `caretaker-check`.
type MountcheckCmd struct {
	Target string `kong:"required,help='Path to check for being a mountpoint.'"`
}

// Run returns nil (exit 0) when Target is a mountpoint, else an error (exit 1).
func (c *MountcheckCmd) Run(cli *CLI) error {
	if caretaker.IsMountpoint(c.Target) {
		return nil
	}
	return fmt.Errorf("%s is not a mountpoint", c.Target)
}
