//go:build linux

package barehost

// The detached per-container supervision shim — cornus's own conmon. Instead of
// the in-process supervisor (supervise_linux.go), which dies with the server (a
// crash during a server-down window goes uncounted until the next reconcile) and
// cannot read a reparented init's exit code, the server can spawn a detached
// `cornus daemon bare-shim` per instance. The shim:
//
//   - becomes a child subreaper (PR_SET_CHILD_SUBREAPER) BEFORE running runc, so
//     the container init that runc reparents away becomes the shim's own child and
//     Wait4 yields its precise exit status — enabling exit-code-aware on-failure
//     restarts (the in-process path cannot, see restartAllowed's doc);
//   - holds an exclusive flock on the record so only one shim supervises an id;
//   - runs runc create + start itself, supervises PID 1, applies the restart
//     policy with capped backoff, and re-reads the record each cycle so a Stop /
//     Delete that raced the exit is honored;
//   - serves a tiny unix control socket (ping / stop) the server dials.
//
// It reuses the in-package runtime driver, record store, restart-policy helpers,
// and file-backed IO, so its observable contract matches the in-process path
// exactly. It is gated behind CORNUS_BARE_SHIM (shim_control_linux.go) until it
// has soaked; the in-process supervisor remains the default.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ShimConfig is the detached shim's invocation, passed by the server as flags to
// `cornus daemon bare-shim` (cmd/cornus/bareshim.go).
type ShimConfig struct {
	ID      string // instance id (cornus-<app>-<i>); its record/bundle/log are derived from DataDir
	DataDir string // the backend DataDir (records/bundles/logs live under <DataDir>/bare)
	Runtime string // the OCI runtime binary (runc/crun/youki)
	Systemd bool   // systemd-cgroup driver (must match how the bundle's cgroupsPath was formed)
}

// shimState is the JSON the shim publishes at <recordDir>/shim.state so the server
// can discover and reach it (mirrors agentproc.State, which barehost cannot import
// — it is under cmd/cornus/internal).
type shimState struct {
	Pid    int    `json:"pid"`
	Socket string `json:"socket"`
}

// Control-socket wire protocol: one newline-terminated command per connection.
const (
	shimCmdPing = "ping"
	shimCmdStop = "stop"
	shimReplyOK = "ok"
)

func shimLockPath(recordDir string) string   { return filepath.Join(recordDir, "shim.lock") }
func shimStatePath(recordDir string) string  { return filepath.Join(recordDir, "shim.state") }
func shimSocketPath(recordDir string) string { return filepath.Join(recordDir, "shim.sock") }

// RunShim is the detached shim's entry point (called from `cornus daemon bare-shim`). It
// acquires the per-id flock (a loser — a shim already supervising this id — exits
// nil), then supervises the container until a terminal state (policy "no",
// on-failure exhausted, one-shot clean exit, or an explicit Stop/Delete), at which
// point it releases the lock and returns.
func RunShim(cfg ShimConfig) error {
	// Reap the reparented container init as our own child so Wait4 gives its exit
	// status. Set before any runc invocation so no init escapes the subreaper.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("bare-shim: set child subreaper: %w", err)
	}

	recordDir := filepath.Join(cfg.DataDir, "bare", "records", cfg.ID)
	if _, err := os.Stat(filepath.Join(recordDir, "record.json")); err != nil {
		return fmt.Errorf("bare-shim: no record for %s: %w", cfg.ID, err)
	}

	// Single-shim guard: a non-blocking exclusive flock. If another shim already
	// supervises this id, we are a redundant spawn — exit quietly.
	lockFile, err := os.OpenFile(shimLockPath(recordDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("bare-shim: open lock: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil // another shim owns this id
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	s := &shim{cfg: cfg, recordDir: recordDir, rt: newRuncRuntime(cfg.Runtime, cfg.Systemd)}
	return s.run()
}

// shim carries one supervision session's state.
type shim struct {
	cfg       ShimConfig
	recordDir string
	rt        containerRuntime
	stopping  atomic.Bool
	initPid   atomic.Int64 // the current container init, for the control socket to signal
}

func (s *shim) run() (retErr error) {
	// Serve the control socket (ping/stop) for the server.
	_ = os.Remove(shimSocketPath(s.recordDir))
	ln, err := net.Listen("unix", shimSocketPath(s.recordDir))
	if err != nil {
		return fmt.Errorf("bare-shim: listen control socket: %w", err)
	}
	defer ln.Close()
	defer os.Remove(shimSocketPath(s.recordDir))
	if err := s.writeState(shimState{Pid: os.Getpid(), Socket: shimSocketPath(s.recordDir)}); err != nil {
		return err
	}
	defer os.Remove(shimStatePath(s.recordDir))
	go s.serveControl(ln)

	for {
		rec, err := s.readRecord()
		if err != nil {
			return fmt.Errorf("bare-shim: read record: %w", err)
		}
		// A Stop/Delete may have landed before this cycle even started.
		if s.stopping.Load() || !rec.DesiredRunning || rec.ExplicitlyStopped {
			s.teardownContainer()
			return nil
		}

		pid, adopted, err := s.launch(rec)
		if err != nil {
			return fmt.Errorf("bare-shim: launch %s: %w", s.cfg.ID, err)
		}
		s.initPid.Store(int64(pid))

		code, known := s.waitInit(pid, adopted)

		// Re-read desired state: a Stop/Delete may have raced the exit.
		rec, err = s.readRecord()
		if err != nil {
			// The record vanished (Delete): nothing left to supervise.
			s.teardownContainer()
			return nil
		}
		rec.LastExitCode = code
		rec.LastExitUnix = time.Now().Unix()

		if s.stopping.Load() || !rec.DesiredRunning || rec.ExplicitlyStopped || !restartAllowedCode(rec, code, known) {
			s.teardownContainer()
			_ = s.writeRecord(rec)
			return nil
		}

		// Restart per policy after capped backoff.
		delay := backoffFor(rec.RestartCount)
		time.Sleep(delay)
		rec, err = s.readRecord()
		if err != nil {
			s.teardownContainer()
			return nil
		}
		if s.stopping.Load() || !rec.DesiredRunning || rec.ExplicitlyStopped {
			s.teardownContainer()
			return nil
		}
		rec.RestartCount++
		_ = s.writeRecord(rec)
	}
}

// launch (re)starts the container and returns its init pid. If the container is
// already running (a shim adopting a live init after the previous shim died, but
// the container survived), it adopts it instead of recreating — adopted==true, in
// which case Wait4 cannot read the exit code (the init is not this shim's child).
func (s *shim) launch(rec *instanceRecord) (pid int, adopted bool, err error) {
	ctx := context.Background()
	if st, serr := s.rt.State(ctx, s.cfg.ID); serr == nil && st.Status == runcStateRunning && st.Pid > 0 {
		return st.Pid, true, nil
	}
	// Clear any lingering state from a previous generation, then create + start
	// with file-backed stdio (the container inherits the log fd directly).
	_ = s.rt.Delete(ctx, s.cfg.ID, true)
	fio, err := newFileIO(rec.LogPath)
	if err != nil {
		return 0, false, err
	}
	defer fio.Close()
	if err := s.rt.Create(ctx, s.cfg.ID, rec.BundleDir, createOpts{IO: fio, PidFile: filepath.Join(s.recordDir, "pid")}); err != nil {
		return 0, false, err
	}
	if err := s.rt.Start(ctx, s.cfg.ID); err != nil {
		_ = s.rt.Delete(ctx, s.cfg.ID, true)
		return 0, false, err
	}
	return s.readPid(), false, nil
}

// waitInit blocks until the container init exits, returning its exit status. For a
// container this shim created, the init is a reparented child (subreaper), so
// Wait4 yields the real status (known==true) and reaps it plus any orphaned
// grandchildren. For an ADOPTED init (not our child) it falls back to a pidfd wait
// (exit detection only, no code — known==false), matching the in-process path.
func (s *shim) waitInit(pid int, adopted bool) (code int, known bool) {
	if adopted {
		pidfdWait(pid)
		return -1, false
	}
	var ws unix.WaitStatus
	for {
		wpid, err := unix.Wait4(pid, &ws, 0, nil)
		switch {
		case err == unix.EINTR:
			continue
		case err == unix.ECHILD:
			// Not (or no longer) our child: the subreaper race lost, or it was
			// already reaped. Detection-only fallback.
			pidfdWait(pid)
			return -1, false
		case err != nil:
			return -1, false
		}
		if wpid != pid {
			continue // an orphaned grandchild we reaped; keep waiting on the init
		}
		switch {
		case ws.Exited():
			code, known = ws.ExitStatus(), true
		case ws.Signaled():
			code, known = 128+int(ws.Signal()), true
		default:
			continue // stopped/continued — not an exit
		}
		// Reap any orphaned grandchildren left behind (best-effort; no concurrent
		// runc invocation is in flight here, so this never steals go-runc's own
		// child-process waits).
		for {
			if wp, werr := unix.Wait4(-1, &ws, unix.WNOHANG, nil); wp <= 0 || werr != nil {
				break
			}
		}
		return code, known
	}
}

// pidfdWait blocks until pid exits, using a pidfd (works on any process, not just
// a child). The exit code is not available this way. Mirrors waitProcessExit's
// success path.
func pidfdWait(pid int) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		// Already gone or unsupported: fall back to a signal-0 poll.
		for {
			if unix.Kill(pid, 0) != nil {
				return
			}
			time.Sleep(statePollInterval)
		}
	}
	defer unix.Close(fd)
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pfd, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n > 0 {
			return
		}
	}
}

// teardownContainer graceful-stops (if still running) and deletes the runtime
// container, keeping the bundle/rootfs so the server's Start can recreate it.
func (s *shim) teardownContainer() {
	ctx := context.Background()
	if pid := int(s.initPid.Load()); pid > 0 {
		s.gracefulKill(pid)
	}
	_ = s.rt.Delete(ctx, s.cfg.ID, true)
}

// gracefulKill sends SIGTERM, waits up to defaultStopGrace for the pid to exit,
// then SIGKILL. Used by teardownContainer and the control socket's stop.
func (s *shim) gracefulKill(pid int) {
	if unix.Kill(pid, unix.SIGTERM) != nil {
		return // already gone
	}
	deadline := time.Now().Add(defaultStopGrace)
	for time.Now().Before(deadline) {
		if unix.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(stopPollInterval)
	}
	_ = unix.Kill(pid, unix.SIGKILL)
}

// serveControl answers ping/stop on the control socket. A stop request flips the
// stopping flag and signals the current init, so the supervise loop breaks and the
// shim exits.
func (s *shim) serveControl(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go s.handleControl(conn)
	}
}

func (s *shim) handleControl(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	switch strings.TrimSpace(line) {
	case shimCmdPing:
		_, _ = conn.Write([]byte(shimReplyOK + "\n"))
	case shimCmdStop:
		s.stopping.Store(true)
		if pid := int(s.initPid.Load()); pid > 0 {
			go s.gracefulKill(pid) // async so we can ack promptly; the loop reaps
		}
		_, _ = conn.Write([]byte(shimReplyOK + "\n"))
	}
}

// --- record + state helpers (the shim shares the on-disk layout with the server) -

func (s *shim) readRecord() (*instanceRecord, error) {
	data, err := os.ReadFile(filepath.Join(s.recordDir, "record.json"))
	if err != nil {
		return nil, err
	}
	var rec instanceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *shim) writeRecord(rec *instanceRecord) error {
	data, err := json.MarshalIndent(rec, "", "\t")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.recordDir, "record.json.shim.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.recordDir, "record.json"))
}

func (s *shim) writeState(st shimState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := shimStatePath(s.recordDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, shimStatePath(s.recordDir))
}

func (s *shim) readPid() int {
	data, err := os.ReadFile(filepath.Join(s.recordDir, "pid"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

// restartAllowedCode is restartAllowed refined with the real exit code: on-failure
// now restarts only on a NONZERO exit (up to MaxAttempts), which the in-process
// path cannot distinguish. An unknown code (adopted init) falls back to the
// any-exit behavior. always/unless-stopped/"" restart unconditionally; "no" never.
func restartAllowedCode(rec *instanceRecord, code int, known bool) bool {
	switch rec.Restart {
	case "always", "unless-stopped", "":
		return true
	case "on-failure":
		if rec.MaxAttempts != 0 && rec.RestartCount >= rec.MaxAttempts {
			return false
		}
		if known {
			return code != 0
		}
		return true
	default: // "no"
		return false
	}
}
