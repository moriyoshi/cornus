package main

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"cornus/cmd/cornus/internal/execdrive"
	"cornus/cmd/cornus/internal/sshagent"
	"cornus/pkg/api"
)

// ExecCmd runs a command inside a deployment's first instance via the remote
// cornus server (docker exec). With -t it allocates a TTY — but only when stdin
// is itself a terminal, so a piped/CI invocation degrades to a plain stream
// instead of a server PTY the client cannot drive; it then drives the local
// terminal in raw mode and forwards window resizes. With -i it bridges local
// stdin into the exec.
type ExecCmd struct {
	Server       string   `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL (http(s):// or ws(s)://). Falls back to the selected connection profile (see cornus config).'"`
	Interactive  bool     `kong:"name='interactive',short='i',help='Keep stdin open and forward it to the command.'"`
	Tty          bool     `kong:"name='tty',short='t',help='Allocate a pseudo-TTY.'"`
	ForwardAgent bool     `kong:"name='forward-agent',help='Forward the local ssh-agent (SSH_AUTH_SOCK) into the exec session, so commands like ssh can use agent-held keys. Supported against a remote-mode dockerhost/containerdhost backend (CORNUS_DOCKER_REMOTE / CORNUS_CONTAINERD_REMOTE), or a kubernetes deployment applied with agentForward set. Like ssh -A, only use this against a cornus server you trust: while the exec session is open, the server can ask the forwarded agent to sign arbitrary challenges.'"`
	Name         string   `kong:"arg,required,help='Deployment name to exec into.'"`
	Cmd          []string `kong:"arg,required,passthrough,help='Command and arguments to run (everything after the name is passed verbatim, so flags like -c reach the command, not cornus).'"`
}

// Run creates and starts an exec against the remote server, bridging stdio and
// propagating the command's exit code as cornus's exit code.
func (c *ExecCmd) Run(cli *CLI) error {
	ctx := cli.rootContext()
	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	cl := cn.Client()

	// A pseudo-TTY is interactive terminal behavior: only allocate one when stdin
	// is actually a terminal. Requesting -t from a pipe or CI would make the server
	// allocate a PTY the client cannot drive in raw mode, garbling the output — so
	// downgrade to a plain stream with a warning, as docker/kubectl do.
	tty := c.Tty
	if tty && !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "cornus: -t requested but stdin is not a terminal; continuing without a TTY")
		tty = false
	}

	if c.ForwardAgent {
		if _, err := sshagent.Dial(); err != nil {
			return fmt.Errorf("--forward-agent: %w", err)
		}
	}

	execID, err := cl.ExecCreate(ctx, c.Name, api.ExecConfig{
		Cmd:          c.Cmd,
		Tty:          tty,
		AttachStdin:  c.Interactive,
		AttachStdout: true,
		AttachStderr: true,
		ForwardAgent: c.ForwardAgent,
	})
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}

	if c.ForwardAgent {
		sess, err := cl.ExecAgentChannel(ctx, c.Name)
		if err != nil {
			return fmt.Errorf("--forward-agent: opening agent channel: %w", err)
		}
		defer sess.Close()
		go sshagent.ServeChannel(ctx, sess)
	}

	code, err := execdrive.Run(ctx, cl, execID, execdrive.Options{
		Tty:          tty,
		Interactive:  c.Interactive,
		ResizeNotify: notifyResize,
	})
	if err != nil {
		return err
	}
	// Propagate the remote command's exit status as cornus's own. The local
	// terminal is already restored (execdrive.Run's deferred cleanup ran on its
	// normal return), so os.Exit here does not skip it.
	if code != 0 {
		exitProcess(code)
	}
	return nil
}

// exitProcess terminates cornus with the given exit code. It is a variable so
// tests can override it; production behaviour is os.Exit.
var exitProcess = os.Exit
