package composecli

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/execdrive"
	"cornus/cmd/cornus/internal/sshagent"
	"cornus/pkg/api"
)

// ExecCmd runs a command inside a service's running container, mirroring
// `docker compose exec`. It resolves the service to its deployment resource and
// execs into the first instance via the cornus server. Like docker compose exec
// it defaults to an interactive session with a pseudo-TTY: a TTY is allocated
// when stdin is a terminal (downgraded to a plain stream otherwise, so piped
// input still works), stdin is bridged into the command, and the remote command's
// exit code becomes cornus's own.
type ExecCmd struct {
	Detach       bool     `kong:"name='detach',short='d',help='Detached mode: run the command in the background. Not yet supported by the cornus exec backends.'"`
	Env          []string `kong:"name='env',short='e',sep='none',help='Set an environment variable KEY=VALUE (repeatable). A bare KEY takes its value from the local environment.'"`
	Workdir      string   `kong:"name='workdir',short='w',help='Working directory for the command inside the container.'"`
	User         string   `kong:"name='user',short='u',help='Run the command as this user (name or uid[:gid]).'"`
	NoTTY        bool     `kong:"name='no-TTY',short='T',help='Disable pseudo-TTY allocation (a TTY is allocated by default when stdin is a terminal).'"`
	Privileged   bool     `kong:"name='privileged',help='Give extended privileges to the command.'"`
	Index        int      `kong:"name='index',default='1',help='Index of the container instance when the service has multiple replicas (only the first instance is addressable).'"`
	ForwardAgent bool     `kong:"name='forward-agent',help='Forward the local ssh-agent (SSH_AUTH_SOCK) into the exec session, so commands like ssh can use agent-held keys. Supported against a remote-mode dockerhost/containerdhost backend (CORNUS_DOCKER_REMOTE / CORNUS_CONTAINERD_REMOTE), or a kubernetes deployment applied with agentForward set. Like ssh -A, only use this against a cornus server you trust: while the exec session is open, the server can ask the forwarded agent to sign arbitrary challenges.'"`
	Service      string   `kong:"arg,required,help='Service to exec into.'"`
	Cmd          []string `kong:"arg,required,passthrough,help='Command and arguments to run (everything after the service is passed verbatim, so flags like -c reach the command, not cornus).'"`
}

// execExit terminates cornus with the given exit code. It is a variable so tests
// can override it; production behaviour is os.Exit, mirroring package main's
// exitProcess for `cornus exec`.
var execExit = os.Exit

// Run resolves the service, creates an exec, and drives it interactively,
// propagating the command's exit code as cornus's exit code.
func (c *ExecCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	if c.Detach {
		// The exec backends (dockerhost, containerd, kubernetes) run an exec as an
		// attached stream; none can leave one running after the client detaches, so a
		// detached exec would be silently killed on return. Refuse it explicitly
		// rather than pretend, until the backends grow real detach support.
		return fmt.Errorf("compose exec --detach is not yet supported")
	}
	if c.Index > 1 {
		// The server execs into the deployment's first instance; there is no
		// instance-index selector, so a higher --index cannot be honored.
		return fmt.Errorf("--index %d: cornus execs into the service's first instance; higher replica indices are not addressable", c.Index)
	}

	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()

	plan, ok := rt.plans[c.Service]
	if !ok {
		return fmt.Errorf("no such service: %s", c.Service)
	}
	env, err := parseExecEnv(c.Env)
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	tty, downgraded := resolveExecTTY(c.NoTTY, term.IsTerminal(int(os.Stdin.Fd())))
	if downgraded {
		d.Warn("pseudo-TTY not allocated: stdin is not a terminal (pass -T to silence)")
	}

	if c.ForwardAgent {
		if _, err := sshagent.Dial(); err != nil {
			return fmt.Errorf("--forward-agent: %w", err)
		}
	}

	execID, err := rt.client.ExecCreate(ctx, plan.Resource, c.execConfig(env, tty))
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}

	if c.ForwardAgent {
		sess, err := rt.client.ExecAgentChannel(ctx, plan.Resource)
		if err != nil {
			return fmt.Errorf("--forward-agent: opening agent channel: %w", err)
		}
		defer sess.Close()
		go sshagent.ServeChannel(ctx, sess)
	}

	code, err := execdrive.Run(ctx, rt.client, execID, execdrive.Options{
		Tty:          tty,
		Interactive:  true, // docker compose exec keeps stdin open by default
		ResizeNotify: notifyResize,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		execExit(code)
	}
	return nil
}

// execConfig builds the ExecConfig for an attached compose exec: the command,
// resolved env, working dir, user, and privileged flag, with stdout/stderr always
// attached and stdin kept open (interactive by default, like docker compose exec).
func (c *ExecCmd) execConfig(env []string, tty bool) api.ExecConfig {
	return api.ExecConfig{
		Cmd:          c.Cmd,
		Tty:          tty,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          env,
		WorkingDir:   c.Workdir,
		User:         c.User,
		Privileged:   c.Privileged,
		ForwardAgent: c.ForwardAgent,
	}
}

// resolveExecTTY decides whether to allocate a pseudo-TTY for a compose exec. The
// default is on (matching docker compose exec); -T (noTTY) turns it off, and it
// downgrades to off when stdin is not a terminal — a server PTY the client cannot
// drive in raw mode would garble the output. The second return reports that
// downgrade so the caller can warn (unless -T already asked for no TTY).
func resolveExecTTY(noTTY, stdinIsTerminal bool) (tty, downgraded bool) {
	if noTTY {
		return false, false
	}
	if !stdinIsTerminal {
		return false, true
	}
	return true, false
}

// parseExecEnv turns --env entries into a KEY=VALUE slice for ExecConfig.Env.
// "KEY=VALUE" sets KEY to VALUE; a bare "KEY" takes its value from the local
// environment (docker exec --env parity). An empty name is rejected.
func parseExecEnv(entries []string) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		key, val, has := strings.Cut(e, "=")
		if key == "" {
			return nil, fmt.Errorf("invalid --env %q: empty name", e)
		}
		if !has {
			val = os.Getenv(key)
		}
		out = append(out, key+"="+val)
	}
	return out, nil
}
