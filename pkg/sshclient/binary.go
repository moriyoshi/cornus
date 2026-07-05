package sshclient

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"cornus/pkg/supervisor"
)

// socketReadyTimeout bounds how long DialViaBinary waits for the forwarded unix
// socket to appear, and how long DialContext waits for it to return during a
// reconnect respawn.
const socketReadyTimeout = 20 * time.Second

// BinaryForwarder reaches a remote cornus server through the system `ssh` binary,
// for ssh_config features the pure-Go transport cannot honor (ProxyCommand, Match).
// It runs one persistent `ssh -N -L <unixsock>:<remote>` — supervised so it
// respawns on drop — and hands out connections by dialing the unix socket. No TCP
// port is bound. It is Linux/macOS only (unix-domain-socket forwarding).
type BinaryForwarder struct {
	sockPath string
	dir      string
	sup      *supervisor.Supervisor
	cancel   context.CancelFunc
	runs     atomic.Int64
}

// DialViaBinary starts the persistent ssh forward to remoteAddr (host:port, from
// the remote host's view) through destination, and waits for the unix socket to
// become connectable. firstConnect lets the initial ssh use the terminal for its
// own passphrase/host-key prompts; respawns run with BatchMode so a background
// reconnect never blocks on a prompt.
func DialViaBinary(ctx context.Context, destination, remoteAddr string, firstConnect bool) (*BinaryForwarder, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, fmt.Errorf("ssh: the ssh binary is required for this connection (ProxyCommand/Match), but was not found: %w", err)
	}
	host, port, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("ssh: invalid remote address %q: %w", remoteAddr, err)
	}
	base := runtimeDir()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("ssh: create runtime dir: %w", err)
	}
	dir, err := os.MkdirTemp(base, "cornus-ssh-")
	if err != nil {
		return nil, fmt.Errorf("ssh: create tunnel dir: %w", err)
	}
	sock := filepath.Join(dir, "tunnel.sock")

	supCtx, cancel := context.WithCancel(context.Background())
	b := &BinaryForwarder{sockPath: sock, dir: dir, cancel: cancel}
	b.sup = supervisor.New(supCtx, func(string, ...any) {})
	b.sup.AddSystem("ssh-forward", supervisor.ServiceFunc(func(runCtx context.Context) error {
		first := b.runs.Add(1) == 1
		return runSSHForward(runCtx, destination, host, port, sock, first && firstConnect)
	}), supervisor.Restart)

	waitCtx, waitCancel := context.WithTimeout(ctx, socketReadyTimeout)
	defer waitCancel()
	if err := waitForSocket(waitCtx, sock); err != nil {
		cancel()
		b.sup.Wait()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("ssh: tunnel did not come up: %w", err)
	}
	return b, nil
}

// DialContext returns a connection to the tunneled server by dialing the unix
// socket, waiting briefly for it if a reconnect respawn is in progress.
func (b *BinaryForwarder) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	deadline := time.Now().Add(socketReadyTimeout)
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", b.sockPath)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Close stops the ssh subprocess and removes the socket directory. Idempotent.
func (b *BinaryForwarder) Close() error {
	b.cancel()
	b.sup.Wait()
	return os.RemoveAll(b.dir)
}

// runSSHForward runs one instance of the persistent ssh forward until it exits or
// runCtx is cancelled. A clean context cancellation (Close) returns nil so the
// supervisor does not restart; any other exit returns an error so it respawns.
func runSSHForward(runCtx context.Context, destination, host, port, sock string, interactive bool) error {
	// ssh refuses to bind an existing socket path; clear any leftover from a prior run.
	_ = os.Remove(sock)

	cmd := exec.CommandContext(runCtx, "ssh", sshForwardArgs(destination, host, port, sock, interactive)...)
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	if runCtx.Err() != nil {
		return nil // Close() cancelled us; do not restart
	}
	if err != nil {
		return fmt.Errorf("ssh forward exited: %w", err)
	}
	return fmt.Errorf("ssh forward exited unexpectedly")
}

// sshForwardArgs builds the argv for the persistent local-forward. A respawn
// (non-interactive) adds BatchMode so a background reconnect fails rather than
// prompting.
func sshForwardArgs(destination, host, port, sock string, interactive bool) []string {
	args := []string{"-N", "-o", "ExitOnForwardFailure=yes"}
	if !interactive {
		args = append(args, "-o", "BatchMode=yes")
	}
	return append(args, "-L", fmt.Sprintf("%s:%s:%s", sock, host, port), destination)
}

// waitForSocket blocks until the unix socket at path is connectable or ctx is done.
func waitForSocket(ctx context.Context, path string) error {
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runtimeDir returns the base directory for the tunnel socket: $XDG_RUNTIME_DIR,
// then $CORNUS_AGENT_DIR, then the OS temp dir.
func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "cornus")
	}
	if d := os.Getenv("CORNUS_AGENT_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "cornus")
}
