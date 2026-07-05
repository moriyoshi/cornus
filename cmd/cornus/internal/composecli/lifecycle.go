package composecli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sync/errgroup"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/devcontainer"
	"cornus/pkg/logging"
)

// execRunner is the subset of *client.Client the container lifecycle needs. It
// is an interface so runContainerHooks can be tested with a fake (mirroring the
// deployAttacher seam in dockerproxy).
type execRunner interface {
	ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error)
	ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error)
	ExecInspect(ctx context.Context, execID string) (api.ExecState, error)
}

// runInitialize runs a devcontainer initializeCommand on the HOST (in baseDir)
// before any container is created, streaming its output. Each resolved command
// runs to completion; a non-zero exit aborts.
func runInitialize(ctx context.Context, baseDir string, cmd *devcontainer.LifecycleCommand) error {
	if cmd == nil {
		return nil
	}
	log := logging.FromContext(ctx)
	for _, argv := range cmd.Commands {
		if len(argv) == 0 {
			continue
		}
		log.InfoContext(ctx, "initializeCommand", slog.Group("devcontainer", "argv", quoteArgv(argv)))
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = baseDir
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("initializeCommand %s: %w", quoteArgv(argv), err)
		}
	}
	return nil
}

// runContainerHooks runs a service's container-side lifecycle commands in order
// (onCreate -> updateContent -> postCreate -> postStart -> postAttach) via exec
// against the running deployment. Each phase's commands run concurrently (the
// object form); a non-zero exit aborts. Output streams to out (buffered per
// command so parallel commands don't interleave).
func runContainerHooks(ctx context.Context, cl execRunner, resource string, h *devcontainer.Hooks, out io.Writer) error {
	if h == nil {
		return nil
	}
	phases := []struct {
		name string
		cmd  *devcontainer.LifecycleCommand
	}{
		{"onCreateCommand", h.OnCreate},
		{"updateContentCommand", h.UpdateContent},
		{"postCreateCommand", h.PostCreate},
		{"postStartCommand", h.PostStart},
		{"postAttachCommand", h.PostAttach},
	}
	log := logging.FromContext(ctx)
	for _, p := range phases {
		if p.cmd == nil {
			continue
		}
		log.InfoContext(ctx, "lifecycle", slog.Group("devcontainer", "phase", p.name, "resource", resource))
		if err := runPhase(ctx, cl, resource, h.User, h.WorkDir, p.cmd, out); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	return nil
}

// runStartHooks runs only the per-start lifecycle commands (postStartCommand ->
// postAttachCommand) via exec against the running deployment. Per the Dev
// Container spec these run every time the container starts (up/start/restart),
// whereas onCreate/updateContent/postCreate are once-per-create and are NOT run
// here. A no-op when h is nil or declares no start hooks.
func runStartHooks(ctx context.Context, cl execRunner, resource string, h *devcontainer.Hooks, out io.Writer) error {
	if h == nil {
		return nil
	}
	phases := []struct {
		name string
		cmd  *devcontainer.LifecycleCommand
	}{
		{"postStartCommand", h.PostStart},
		{"postAttachCommand", h.PostAttach},
	}
	log := logging.FromContext(ctx)
	for _, p := range phases {
		if p.cmd == nil {
			continue
		}
		log.InfoContext(ctx, "lifecycle", slog.Group("devcontainer", "phase", p.name, "resource", resource))
		if err := runPhase(ctx, cl, resource, h.User, h.WorkDir, p.cmd, out); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	return nil
}

// runPhase runs one lifecycle phase's command(s). A single command streams
// directly; multiple (object-form) commands run concurrently with buffered
// output flushed in declaration order afterwards.
func runPhase(ctx context.Context, cl execRunner, resource, user, workDir string, cmd *devcontainer.LifecycleCommand, out io.Writer) error {
	if len(cmd.Commands) == 1 {
		return runExec(ctx, cl, resource, user, workDir, cmd.Commands[0], out)
	}
	bufs := make([]bytes.Buffer, len(cmd.Commands))
	g, gctx := errgroup.WithContext(ctx)
	for i, argv := range cmd.Commands {
		i, argv := i, argv
		g.Go(func() error { return runExec(gctx, cl, resource, user, workDir, argv, &bufs[i]) })
	}
	err := g.Wait()
	for i := range bufs {
		_, _ = out.Write(bufs[i].Bytes())
	}
	return err
}

// runExec creates, starts, and drains one exec, returning an error on a non-zero
// exit. Mirrors the non-interactive drive in cmd/cornus/exec.go.
func runExec(ctx context.Context, cl execRunner, resource, user, workDir string, argv []string, out io.Writer) error {
	if len(argv) == 0 {
		return nil
	}
	execID, err := cl.ExecCreate(ctx, resource, api.ExecConfig{
		Cmd:          argv,
		User:         user,
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}
	conn, err := cl.ExecStart(ctx, execID, api.ExecStartConfig{})
	if err != nil {
		return fmt.Errorf("starting exec: %w", err)
	}
	_, _ = io.Copy(out, conn)
	conn.Close()

	st, err := cl.ExecInspect(ctx, execID)
	if err != nil {
		return fmt.Errorf("inspecting exec: %w", err)
	}
	if st.ExitCode != 0 {
		return fmt.Errorf("command %s exited with code %d", quoteArgv(argv), st.ExitCode)
	}
	return nil
}

// runServiceHooks runs the container lifecycle for a devcontainer service after
// it is ready. A no-op for services without hooks (i.e. plain Compose). out is
// the per-service output writer (see UpCmd.runForeground's hookLines): with
// several services' hooks now able to run concurrently, callers must no longer
// hand every service the same raw os.Stdout, which has no line-buffering of its
// own and would interleave concurrent writers' bytes.
func (r *runtime) runServiceHooks(ctx context.Context, name string, out io.Writer) error {
	h := r.hooks[name]
	if h == nil {
		return nil
	}
	return runContainerHooks(ctx, r.client, r.plans[name].Resource, h, out)
}

// waitRunningThenHooks gates a mounted service's container lifecycle on the
// cluster-side reconcile before running it. A mounted deploy-attach hold returns
// as soon as the backend has created the objects (for the kubernetes backend that
// is before any pod is scheduled), so a devcontainer's hooks — driven by a
// server-side exec that resolves the deployment's pod — must wait for the
// workload to report running or the exec races the scheduler and fails with
// "no pods for deployment ...". A no-op when h is nil (a plain Compose bind
// mount). Never fails on a slow rollout: reportReconcile returns after the
// timeout with the last status seen, then the hooks run regardless (matching the
// mount-free path, which also proceeds after its reconcile wait times out).
//
// prog is the caller's shared *cliout.Progress (see reportReconcile's doc
// comment) — required even when h is nil's caller already checked, since
// several services' waitRunningThenHooks calls can run concurrently.
func waitRunningThenHooks(ctx context.Context, poll statusPoller, cl execRunner, service, deployName, resource string, h *devcontainer.Hooks, d *cliout.Driver, prog *cliout.Progress, status *serviceStatus, out io.Writer, pollInterval, timeout time.Duration) error {
	if h == nil {
		return nil
	}
	// status, when set, is the mounted service's shared line: the reconcile
	// transitions fold onto it (the same line its attach-phase diagnostics used),
	// and the owning goroutine finishes it.
	reportReconcile(ctx, poll, service, deployName, d, prog, status, pollInterval, timeout)
	if err := ctx.Err(); err != nil {
		return err
	}
	return runContainerHooks(ctx, cl, resource, h, out)
}

// runMountedServiceHooks waits for a mounted service's workload to report running,
// then runs its container lifecycle via server-side exec. The live *client.Client
// serves as both the status poller and the exec runner. A no-op for services
// without hooks (plain Compose bind mounts). See waitRunningThenHooks.
func (r *runtime) runMountedServiceHooks(ctx context.Context, name, deployName string, d *cliout.Driver, prog *cliout.Progress, status *serviceStatus, out io.Writer) error {
	return waitRunningThenHooks(ctx, r.client, r.client, name, deployName, r.plans[name].Resource, r.hooks[name], d, prog, status, out, reconcilePollInterval, reconcileWaitTimeout)
}

// runServiceStartHooks runs only the per-start lifecycle commands (postStart ->
// postAttach) for a devcontainer service after a start/restart. A no-op for
// services without hooks (i.e. plain Compose).
func (r *runtime) runServiceStartHooks(ctx context.Context, name string) error {
	h := r.hooks[name]
	if h == nil {
		return nil
	}
	return runStartHooks(ctx, r.client, r.plans[name].Resource, h, os.Stdout)
}

// quoteArgv renders an argv for human-readable logging.
func quoteArgv(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	var b bytes.Buffer
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if a == "" || bytes.ContainsAny([]byte(a), " \t\"") {
			fmt.Fprintf(&b, "%q", a)
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}
