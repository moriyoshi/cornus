package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"cornus/cmd/cornus/internal/clientagent"
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/composecli"
	"cornus/pkg/clientconduit"
)

// DaemonCmd groups the long-running helper daemons: the client-side Docker
// Engine API proxy and compose background mounts daemon (both run in the
// foreground by default; -d/--daemon re-execs them detached into the
// background), the pod-facing sidecars (`caretaker`, `caretaker-check`,
// `net-redirect`), and the deploy-backend-internal per-container shims
// (`bare-shim`, `containerd-log-shim`). The sidecars and `containerd-log-shim`
// also keep hidden top-level aliases (see CLI in main.go) because those
// spellings are baked into generated pod specs / the containerd binary log URI,
// which cannot address a nested subcommand. The server itself is `cornus serve`.
type DaemonCmd struct {
	Docker         DockerProxyCmd       `kong:"cmd,help='Docker Engine API proxy: point DOCKER_HOST at its socket and stock docker / docker compose drive a remote cornus server.'"`
	Mounts         composecli.MountsCmd `kong:"cmd,hidden,help='Obsolete: replaced by the unified agent (cornus daemon agent), driven by compose up -d.'"`
	Agent          AgentCmd             `kong:"cmd,hidden,help='Run the unified client-side background agent (spawned by clients; not run by hand).'"`
	Status         AgentStatusCmd       `kong:"cmd,name='status',help='Show the running cornus client agent inventory.'"`
	Stop           AgentStopCmd         `kong:"cmd,name='stop',help='Stop the running cornus client agent.'"`
	Caretaker      CaretakerCmd         `kong:"cmd,help='Pod sidecar: run the configured roles (9P mounts, ...) until teardown.'"`
	CaretakerCheck CaretakerCheckCmd    `kong:"cmd,name='caretaker-check',help='Exit 0 if every caretaker role is live (sidecar readiness probe).'"`
	NetRedirect    NetRedirectCmd       `kong:"cmd,name='net-redirect',help='Init container: iptables-redirect app egress into the caretaker proxy.'"`
	BareShim       BareShimCmd          `kong:"cmd,name='bare-shim',hidden,help='Per-container supervision shim for the bare deploy backend (spawned by the server, not by users).'"`
	LogShim        LogShimCmd           `kong:"cmd,name='containerd-log-shim',hidden,help='containerd binary logging driver (invoked by the containerd shim via the binary log URI alias, not by users).'"`
}

// AgentCmd runs the unified client-side background agent: one process, reached
// over a single control socket, that hosts every client-held workload session
// (compose projects, and the docker frontend once cut over). Hidden — clients
// spawn it via daemonize; users interact through `cornus compose up -d`,
// `cornus daemon docker`, `cornus daemon status`, and `cornus daemon stop`.
type AgentCmd struct{}

// Run runs the agent until SIGTERM/SIGINT or an idle-exit.
func (c *AgentCmd) Run() error { return clientagent.RunProcess() }

// AgentStatusCmd prints the running agent's inventory.
type AgentStatusCmd struct{}

func (c *AgentStatusCmd) Run(d *cliout.Driver) error {
	inv, err := clientagent.Status()
	if err != nil {
		return err
	}
	res := agentStatus{Running: inv != nil}
	if inv != nil {
		res.Servers = inv.Servers
		res.Projects = inv.Projects
		res.Dockers = inv.Dockers
		res.Banners = inv.Banners
	}
	return d.Emit(res)
}

// agentStatus is the structured result of `cornus daemon status`: a
// human-readable inventory in plain/fancy mode, a single JSON object in json.
type agentStatus struct {
	Running  bool                `json:"running"`
	Servers  []string            `json:"servers,omitempty"`
	Projects map[string][]string `json:"projects,omitempty"`
	Dockers  []string            `json:"dockers,omitempty"`
	Banners  []string            `json:"banners,omitempty"`
}

func (s agentStatus) Human(p cliout.Printer) {
	if !s.Running {
		p.Line("no cornus client agent is running")
		return
	}
	if len(s.Servers) > 0 {
		p.Line("servers: %s", strings.Join(s.Servers, ", "))
	}
	for name, svcs := range s.Projects {
		p.Line("project %s: %s", name, strings.Join(svcs, " "))
	}
	for _, sock := range s.Dockers {
		p.Line("docker frontend: %s", sock)
	}
	for _, b := range s.Banners {
		p.Line("%s", b)
	}
}

// AgentStopCmd stops the running agent.
type AgentStopCmd struct{}

func (c *AgentStopCmd) Run(d *cliout.Driver) error {
	if err := clientagent.Stop(); err != nil {
		return err
	}
	d.Done("cornus client agent stopped")
	return nil
}

// DockerProxyCmd runs a local daemon that speaks a subset of the Docker
// Engine REST API on a unix socket and translates container operations into
// cornus deploys against a remote cornus server. Point DOCKER_HOST at its
// socket and stock `docker` runs workloads on the remote cornus, with the
// caller's local bind-mount directories streamed over 9P.
//
//	cornus daemon docker --host http://cornus:5000 --socket /run/cornus-docker.sock
//	DOCKER_HOST=unix:///run/cornus-docker.sock docker run -d -v ./conf:/etc/app:ro nginx
type DockerProxyCmd struct {
	Host           string `kong:"name='host',env='CORNUS_HOST',help='Remote cornus server URL. Falls back to the selected connection profile, then http://localhost:5000.'"`
	Socket         string `kong:"name='socket',help='Unix socket to listen on (default: $XDG_RUNTIME_DIR/cornus-docker.sock).',env='CORNUS_DOCKER_SOCK'"`
	Daemon         bool   `kong:"name='daemon',short='d',help='Run in the background as a daemon (default: run in the foreground).'"`
	NoForwardPorts bool   `kong:"name='no-forward-ports',help='Do not publish container ports (docker -p) on local listeners.'"`
}

// Run registers a Docker API frontend with the single background agent, which
// hosts the socket and translates docker operations into deploys on the shared
// server connection. Foreground blocks until Ctrl-C, then deregisters the
// frontend; -d/--daemon registers it and returns (the agent keeps hosting it).
func (c *DockerProxyCmd) Run(r *clientconn.Resolver, d *cliout.Driver) error {
	cn, err := r.Resolve(c.Host)
	if err != nil {
		return err
	}
	defer cn.Cleanup()

	sock := c.Socket
	if sock == "" {
		sock = defaultDockerSocket()
	}
	egCfg := cn.ConduitConfig("")
	if c.NoForwardPorts {
		egCfg.Mode = clientconduit.ModeNone
	}
	absCfg, err := r.AbsConfigPath()
	if err != nil {
		return err
	}
	req := clientagent.Request{
		Action: "docker-serve",
		Socket: sock,
		Conn: clientagent.ConnSpec{
			ConfigFile: absCfg,
			Context:    r.Context,
			Server:     c.Host, // raw --host; the agent re-resolves (profile, svcforward)
			ViaServer:  cn.ViaServer(nil),
			Token:      os.Getenv("CORNUS_TOKEN"),
		},
		Conduit:        clientagent.ToWireConduit(egCfg),
		NoForwardPorts: c.NoForwardPorts,
	}

	socket, err := clientagent.EnsureRunning()
	if err != nil {
		return fmt.Errorf("start background agent: %w", err)
	}
	resp, err := clientagent.Send(socket, req)
	if err != nil {
		return fmt.Errorf("register docker frontend with agent: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("docker frontend: %s", resp.Error)
	}
	d.Info("cornus daemon docker listening on unix://%s (hosted by the cornus agent)", sock)
	d.Info("  export DOCKER_HOST=unix://%s", sock)

	if c.Daemon {
		return nil // the agent keeps hosting the frontend
	}
	// Foreground: hold until Ctrl-C, then deregister the frontend.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if _, err := clientagent.Send(socket, clientagent.Request{Action: "docker-stop", Socket: sock}); err != nil {
		return fmt.Errorf("stop docker frontend: %w", err)
	}
	d.Done("cornus daemon docker stopped")
	return nil
}

func defaultDockerSocket() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "cornus-docker.sock")
	}
	return "/tmp/cornus-docker.sock"
}
