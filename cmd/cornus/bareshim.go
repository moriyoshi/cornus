package main

import "cornus/pkg/deploy/barehost"

// BareShimCmd is the hidden per-container supervision shim for the bare
// (daemonless OCI-runtime) deploy backend — cornus's own conmon. It is
// registered under the daemon group (`cornus daemon bare-shim`); the bare
// backend spawns it detached (setsid) per instance when CORNUS_BARE_SHIM is set;
// it runs runc create/start, supervises PID 1 (reaping it as a subreaper for the
// precise exit code), applies the restart policy, and serves a control socket the
// server dials. It is never invoked by users. See pkg/deploy/barehost/shim_linux.go.
type BareShimCmd struct {
	ID      string `kong:"name='id',required,help='Instance id to supervise (cornus-<app>-<replica>).'"`
	Runtime string `kong:"name='runtime',default='runc',help='OCI runtime binary (runc/crun/youki).'"`
	Systemd bool   `kong:"name='systemd-cgroup',help='Use the systemd cgroup driver (must match the bundle).'"`
}

// Run supervises the instance until a terminal state, then returns. It blocks for
// the container's whole life, so this process is the detached shim. The backend
// DataDir comes from the global --data-dir flag (CORNUS_DATA), which the server
// passes when it spawns the shim.
func (c *BareShimCmd) Run(cli *CLI) error {
	return barehost.RunShim(barehost.ShimConfig{
		ID:      c.ID,
		DataDir: cli.DataDir,
		Runtime: c.Runtime,
		Systemd: c.Systemd,
	})
}
