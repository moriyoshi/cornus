package dockerhost

// The self-spawned Podman API service (CORNUS_PODMAN_SERVICE=1).
//
// This exists so no `podman.socket` need ever be enabled. It needs only the
// podman binary, and it behaves identically rootful and rootless because the
// service inherits cornus's own uid.
//
// It does not make Podman "a daemon". Podman is daemonless either way: the
// systemd unit is socket-activated and this child is started on demand, and both
// exit when idle. The only difference is who owns the lifecycle — and owning it
// is what lets cornus offer the option at all.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"cornus/pkg/runtimeendpoint"
)

// podmanServiceIdleTimeout is the service's own `--time`, in seconds.
//
// Non-zero on purpose. `--time=0` means "never exit", which is right only while
// a supervisor is guaranteed to clean up; if cornus is killed with SIGKILL it
// runs no deferred code, and a never-exiting orphan would hold the socket and
// outlive every reason for its existence. A finite idle timeout makes the child
// reap itself in that case, and is harmless otherwise because an active cornus
// polls the daemon far more often than this.
const podmanServiceIdleTimeout = 300

// podmanServiceStartTimeout bounds the wait for the socket to appear.
const podmanServiceStartTimeout = 20 * time.Second

// podmanService is a `podman system service` process cornus owns.
type podmanService struct {
	Endpoint runtimeendpoint.Endpoint
	// SocketPath is the unix socket the service was told to listen on.
	SocketPath string

	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// startPodmanService launches the service and waits for its socket.
func startPodmanService(ctx context.Context) (*podmanService, error) {
	bin, err := exec.LookPath("podman")
	if err != nil {
		return nil, fmt.Errorf(
			"%s=1 asks cornus to run `podman system service`, but podman is not on PATH: %w. "+
				"Install podman, or set %s to an already-listening API socket instead",
			envPodmanService, err, envPodmanSocket)
	}

	sock, err := podmanServiceSocketPath()
	if err != nil {
		return nil, err
	}
	// A socket left behind by a previous run would make the service fail to bind.
	// Removing it is safe: the path is per-process (see podmanServiceSocketPath),
	// so nothing else can legitimately be listening on it.
	_ = os.Remove(sock)

	// The service is tied to a context of its own rather than the caller's, so
	// Close() can stop it independently of whatever ctx the constructor was given
	// (which is typically a short-lived startup context).
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(runCtx, bin, "system", "service",
		"--time", strconv.Itoa(podmanServiceIdleTimeout),
		"unix://"+sock)
	// Inherit stderr so a startup failure is visible in cornus's own logs rather
	// than swallowed into a pipe nobody reads.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start `podman system service`: %w", err)
	}

	svc := &podmanService{SocketPath: sock, cmd: cmd, cancel: cancel}
	if err := waitForSocket(ctx, sock, cmd); err != nil {
		svc.Stop()
		return nil, err
	}
	ep, err := runtimeendpoint.Parse("unix://"+sock, "")
	if err != nil {
		svc.Stop()
		return nil, err
	}
	svc.Endpoint = ep
	return svc, nil
}

// waitForSocket blocks until the service's socket exists, the process exits, or
// the timeout elapses.
//
// It watches the PROCESS as well as the path. Without that, a podman that fails
// immediately — a bad storage config, a conflicting rootless setup — would be
// reported as a generic timeout after the full wait, when the real error was
// already on stderr within milliseconds.
func waitForSocket(ctx context.Context, sock string, cmd *exec.Cmd) error {
	deadline := time.Now().Add(podmanServiceStartTimeout)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		select {
		case werr := <-exited:
			return fmt.Errorf("`podman system service` exited before its socket appeared: %w "+
				"(its own error is above, on cornus's stderr)", werr)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("`podman system service` did not create %s within %s", sock, podmanServiceStartTimeout)
		}
	}
}

// podmanServiceSocketPath picks a PER-PROCESS socket path.
//
// Per-process rather than fixed, so two cornus servers on one host do not fight
// over the same path. Podman's own storage locking handles concurrent access to
// the image and container store, so several services (and a person's `podman`
// CLI) coexist happily — it is only the socket that must not collide.
func podmanServiceSocketPath() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("podman service socket dir %s: %w", dir, err)
	}
	return filepath.Join(dir, fmt.Sprintf("cornus-podman-%d.sock", os.Getpid())), nil
}

// Stop terminates the service and removes its socket.
func (s *podmanService) Stop() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// Wait was already claimed by waitForSocket's goroutine; the context
		// cancellation above is what actually signals the process.
		_ = s.cmd.Process.Kill()
	}
	if s.SocketPath != "" {
		_ = os.Remove(s.SocketPath)
	}
}
