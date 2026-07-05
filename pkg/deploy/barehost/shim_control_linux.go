//go:build linux

package barehost

// Server side of the detached shim: discover-or-spawn a `cornus daemon bare-shim` per
// instance and reach it over its control socket. This is the counterpart to
// shim_linux.go and is only used when CORNUS_BARE_SHIM is set; otherwise the
// backend keeps the in-process supervisor (supervise_linux.go). barehost cannot
// import cmd/cornus/internal/{daemonize,agentproc} (internal to cmd/cornus), so
// the small spawn + ping + stop machinery is reimplemented here against the same
// on-disk layout the shim writes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// shimReadyTimeout bounds how long ensureShim waits for a freshly spawned shim to
// publish a reachable control socket.
const shimReadyTimeout = 10 * time.Second

// ensureShim makes a live, supervising shim exist for id: it pings any existing
// one (fast path), else spawns a detached `cornus daemon bare-shim` and waits for it to
// come up. The shim's own flock dedupes concurrent/redundant spawns, so this is
// safe to call from createInstance, Start, and reconcile alike.
func (b *Backend) ensureShim(id string) error {
	if b.shimAlive(id) {
		return nil
	}
	if err := b.spawnShim(id); err != nil {
		return err
	}
	deadline := time.Now().Add(shimReadyTimeout)
	for time.Now().Before(deadline) {
		if b.shimAlive(id) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("bare: shim for %s did not become ready", id)
}

// spawnShim launches a detached (setsid) `cornus daemon bare-shim` for id, its own stdio
// redirected to a per-instance shim log. It does not wait — the shim runs until a
// terminal state and the server reaches it via the control socket.
func (b *Backend) spawnShim(id string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("bare: resolve self for shim: %w", err)
	}
	recordDir := b.recordDir(id)
	logf, err := os.OpenFile(filepath.Join(recordDir, "shim.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("bare: open shim log: %w", err)
	}
	defer logf.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer null.Close()

	args := []string{"daemon", "bare-shim", "--id", id, "--data-dir", b.dataDir, "--runtime", b.runtime}
	if b.systemdCgroup {
		args = append(args, "--systemd-cgroup")
	}
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach: survive the server
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("bare: spawn shim for %s: %w", id, err)
	}
	// Setsid detaches the shim from the server's session/terminal, but it is
	// still the server's CHILD: without a Wait it would linger as a zombie once
	// it exits (Stop/Delete/terminal policy). Reap it in the background — if the
	// server dies first the shim reparents to init, which reaps it instead.
	go func() { _ = cmd.Wait() }()
	return nil
}

// shimAlive reports whether a supervising shim for id is up: its recorded pid is
// live AND it answers a ping on its control socket (the PID-reuse-safe check).
func (b *Backend) shimAlive(id string) bool {
	st, err := readShimState(b.recordDir(id))
	if err != nil || st == nil || st.Pid <= 0 {
		return false
	}
	if unix.Kill(st.Pid, 0) != nil {
		return false // pid gone
	}
	return pingShim(st.Socket)
}

// shimStop asks id's shim to stop the container and exit, waiting for it to go.
// It reports whether a live shim actually HANDLED the teardown (accepted the stop
// over its control socket and exited, deleting the container on the way out).
// False means the caller must stop the container itself: no shim exists (e.g. a
// companion, which is never shim-supervised, or a shim that already exited), or
// the shim was unreachable/wedged — in which case it is signalled dead here, but
// the shim has NO signal handler, so dying that way leaves the container running.
func (b *Backend) shimStop(id string) bool {
	st, err := readShimState(b.recordDir(id))
	if err != nil || st == nil || st.Pid <= 0 {
		return false
	}
	accepted := sendShim(st.Socket, shimCmdStop) == nil
	if !accepted {
		// Socket unreachable: kill the (wedged) shim so a fresh one can take the
		// flock later; the CONTAINER is the caller's to stop.
		_ = unix.Kill(st.Pid, unix.SIGTERM)
	}
	// Wait for the shim process to exit.
	deadline := time.Now().Add(shimReadyTimeout + defaultStopGrace)
	for time.Now().Before(deadline) {
		if unix.Kill(st.Pid, 0) != nil {
			return accepted
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = unix.Kill(st.Pid, unix.SIGKILL) // last resort; container is the caller's to stop
	return false
}

// pingShim dials the control socket and reports whether it answers a ping.
func pingShim(socket string) bool {
	return sendShim(socket, shimCmdPing) == nil
}

// sendShim dials the shim's unix control socket, sends one command, and expects
// an "ok" reply.
func sendShim(socket, cmd string) error {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(defaultStopGrace + 2*time.Second))
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return err
	}
	buf := make([]byte, len(shimReplyOK)+1)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	if string(buf[:n]) != shimReplyOK+"\n" && string(buf[:n]) != shimReplyOK {
		return fmt.Errorf("bare: unexpected shim reply %q", string(buf[:n]))
	}
	return nil
}

// readShimState loads a shim's published state, or (nil, nil) when none exists.
func readShimState(recordDir string) (*shimState, error) {
	data, err := os.ReadFile(shimStatePath(recordDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st shimState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// launchSupervised brings rec's instance up and under supervision. Shim path: a
// detached shim owns runc create/start + the restart loop. In-process path: the
// server creates/starts and a goroutine watches. Used by createInstance and Start.
func (b *Backend) launchSupervised(ctx context.Context, rec *instanceRecord) error {
	if b.useShim {
		return b.ensureShim(rec.ID)
	}
	// In-process: adopt an already-running container (reattach), else clear any
	// lingering non-running state and create + start afresh.
	if st, err := b.rt.State(ctx, rec.ID); err == nil {
		if st.Status == runcStateRunning && st.Pid > 0 {
			b.super.watch(rec.ID, st.Pid)
			return nil
		}
		_ = b.rt.Delete(ctx, rec.ID, true)
	}
	fio, err := newFileIO(rec.LogPath)
	if err != nil {
		return err
	}
	err = b.rt.Create(ctx, rec.ID, rec.BundleDir, createOpts{IO: fio, PidFile: filepath.Join(b.recordDir(rec.ID), "pid")})
	fio.Close()
	if err != nil {
		return fmt.Errorf("bare: create %s: %w", rec.ID, err)
	}
	if err := b.rt.Start(ctx, rec.ID); err != nil {
		_ = b.rt.Delete(ctx, rec.ID, true)
		return fmt.Errorf("bare: start %s: %w", rec.ID, err)
	}
	if pid := b.readPid(rec.ID); pid > 0 {
		b.super.watch(rec.ID, pid)
	} else if st, err := b.rt.State(ctx, rec.ID); err == nil && st.Pid > 0 {
		b.super.watch(rec.ID, st.Pid)
	}
	return nil
}

// stopSupervised stops id's supervision and graceful-stops its container, keeping
// the bundle/rootfs so Start can recreate it. The caller has already persisted the
// stop intent (ExplicitlyStopped). Shim path: tell the shim to stop + exit; when
// no live shim handled it (a companion — never shim-supervised — or a dead/wedged
// shim), fall through to stopping the container directly, exactly like the
// in-process path (SIGTERM -> grace -> SIGKILL -> delete). unwatch is a no-op for
// ids without an in-process watcher, so the fallthrough is safe in both modes.
func (b *Backend) stopSupervised(ctx context.Context, id string) {
	if b.useShim && b.shimStop(id) {
		return
	}
	b.super.unwatch(id)
	b.stopInstance(ctx, id)
}

// teardownSupervised stops id's supervision and container (for Delete and
// createInstance error rollback), leaving the rootfs/netns/record for the caller
// to reap. It graceful-stops (SIGTERM -> grace -> SIGKILL) rather than an abrupt
// SIGKILL: a mount-caretaker companion holds a kernel 9P mount and must unmount
// it on shutdown, else its cgroup stays busy and `runc delete` fails ("device or
// resource busy"), leaking the container. Same shim-or-fallthrough shape as
// stopSupervised — in shim mode, companions and dead-shim instances still get the
// graceful direct stop. Idempotent.
func (b *Backend) teardownSupervised(ctx context.Context, id string) {
	if b.useShim && b.shimStop(id) {
		_ = b.rt.Delete(ctx, id, true) // idempotent; the shim already deleted
		return
	}
	b.super.unwatch(id)
	b.stopInstance(ctx, id) // SIGTERM -> grace -> SIGKILL -> delete
}
