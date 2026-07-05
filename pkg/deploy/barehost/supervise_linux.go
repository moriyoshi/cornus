//go:build linux

package barehost

// In-process supervision + restart policy. When a supervised container's PID 1
// exits, the supervisor restarts it per its policy (always / unless-stopped /
// on-failure[:N]) unless it was explicitly stopped, with capped exponential
// backoff. This is the restart-monitor role containerd's daemon plays — realized
// here as goroutines in the cornus server that pidfd-wait on each instance's
// init and re-run it. It supervises while the server is up and re-establishes
// supervision on startup (reconcile); a crash during a server restart is caught
// by that reconcile rather than instantly. (The detached-shim upgrade that keeps
// supervision alive across server downtime is a follow-up; the observable
// restart contract is identical.)
//
// Exit detection uses pidfd_open + poll: it works on the container init even
// though runc reparents it away from the server (so it is not our child), and is
// event-driven rather than a busy poll. On kernels without pidfd it falls back to
// polling runc state.

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"cornus/pkg/deploy/internal/hostrun"
)

const (
	// minBackoff/maxBackoff bound the restart backoff (matches the client
	// supervisor's shape).
	minBackoff = 100 * time.Millisecond
	maxBackoff = 30 * time.Second
	// stableRunThreshold: an instance that ran at least this long before exiting
	// has its restart counter reset, so a long-lived workload that finally crashes
	// restarts promptly instead of inheriting an old backoff.
	stableRunThreshold = 10 * time.Second
	// statePollInterval is the fallback exit-detection poll when pidfd is
	// unavailable.
	statePollInterval = 500 * time.Millisecond
)

// supervisor tracks a watcher goroutine per supervised instance.
type supervisor struct {
	b        *Backend
	mu       sync.Mutex
	watchers map[string]context.CancelFunc
}

func newSupervisor(b *Backend) *supervisor {
	return &supervisor{b: b, watchers: map[string]context.CancelFunc{}}
}

// watch (re)starts supervision of instance id whose current init is pid. An
// existing watcher for the id is replaced.
func (s *supervisor) watch(id string, pid int) {
	s.mu.Lock()
	if cancel, ok := s.watchers[id]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.watchers[id] = cancel
	s.mu.Unlock()
	go s.run(ctx, id, pid)
}

// unwatch stops supervising id (the container is not touched; Stop/Delete handle
// the runtime). Called before an intentional stop/delete so the exit is not
// treated as a crash.
func (s *supervisor) unwatch(id string) {
	s.mu.Lock()
	if cancel, ok := s.watchers[id]; ok {
		cancel()
		delete(s.watchers, id)
	}
	s.mu.Unlock()
}

// stopAll cancels every watcher (server shutdown). Containers keep running and
// are re-supervised on the next startup reconcile.
func (s *supervisor) stopAll() {
	s.mu.Lock()
	for id, cancel := range s.watchers {
		cancel()
		delete(s.watchers, id)
	}
	s.mu.Unlock()
}

func (s *supervisor) run(ctx context.Context, id string, pid int) {
	start := time.Now()
	if !waitProcessExit(ctx, s.b, id, pid) {
		return // ctx cancelled (unwatched): an intentional stop/delete, not a crash
	}
	s.onExit(ctx, id, time.Since(start))
}

// onExit applies the restart policy after an instance's init exits.
func (s *supervisor) onExit(ctx context.Context, id string, ranFor time.Duration) {
	log := slog.Default().With(slog.String("component", "bare-supervisor"), slog.String("instance", id))
	rec, err := s.b.readRecord(id)
	if err != nil || rec == nil || !rec.DesiredRunning || rec.ExplicitlyStopped || !restartAllowed(rec) {
		s.unwatch(id)
		return
	}
	if ranFor >= stableRunThreshold {
		rec.RestartCount = 0
	}
	delay := backoffFor(rec.RestartCount)
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	// Re-read after the backoff: a Stop/Delete may have raced in.
	rec, err = s.b.readRecord(id)
	if err != nil || rec == nil || !rec.DesiredRunning || rec.ExplicitlyStopped || !restartAllowed(rec) {
		s.unwatch(id)
		return
	}
	newPid, err := s.b.restartInstance(ctx, rec)
	if err != nil {
		log.Warn("restart failed; will not retry until next reconcile", "error", err)
		s.unwatch(id)
		return
	}
	rec.RestartCount++
	if werr := s.b.writeRecord(rec); werr != nil {
		log.Warn("persist restart count failed", "error", werr)
	}
	log.Info("restarted instance", "restartCount", rec.RestartCount, "pid", newPid)
	s.watch(id, newPid)
}

// restartAllowed reports whether an instance's policy permits another restart.
// on-failure is treated as restart-on-any-exit up to MaxAttempts (the exit code
// is not available for a reparented init without being its parent — the detached
// shim will refine this); always/unless-stopped restart unconditionally (the
// explicitly-stopped guard is checked separately); "no" never restarts.
func restartAllowed(rec *instanceRecord) bool {
	switch rec.Restart {
	case "always", "unless-stopped", "":
		return true
	case "on-failure":
		return rec.MaxAttempts == 0 || rec.RestartCount < rec.MaxAttempts
	default: // "no"
		return false
	}
}

// backoffFor returns the restart delay for the nth restart (capped exponential).
func backoffFor(count int) time.Duration {
	d := minBackoff
	for i := 0; i < count && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// waitProcessExit blocks until pid exits (returns true) or ctx is cancelled
// (returns false). It uses a pidfd — which works on the reparented container
// init — falling back to polling the runtime state by id on older kernels.
func waitProcessExit(ctx context.Context, b *Backend, id string, pid int) bool {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return pollStateExit(ctx, b, id)
	}
	defer unix.Close(fd)
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pfd, int(statePollInterval/time.Millisecond))
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return true // treat a poll error as "gone" — the caller re-checks the record
		}
		if n > 0 {
			return true // POLLIN on a pidfd => the process exited
		}
		select {
		case <-ctx.Done():
			return false
		default:
		}
	}
}

// pollStateExit polls the runtime for id until it is no longer running (returns
// true) or ctx is cancelled (returns false). The pidfd fallback.
func pollStateExit(ctx context.Context, b *Backend, id string) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(statePollInterval):
		}
		st, err := b.rt.State(ctx, id)
		if err != nil || st.Status != runcStateRunning {
			return true
		}
	}
}

// reconcile is the startup pass (kicked from New): it re-establishes supervision
// for every desired-running instance. A still-running instance is reattached
// (its live init is watched again); a dead-but-desired-running instance is
// restarted (a crash while the server was down, or a fresh boot). It reuses the
// existing bundle, so netns/rootfs must be intact — full host-reboot recovery
// that rebuilds the netns (following containerdhost's repairNetns) is a
// follow-up. Best-effort per instance.
func (b *Backend) reconcile() {
	recs, err := b.listRecords()
	if err != nil {
		return
	}
	ctx := context.Background()
	log := slog.Default().With(slog.String("component", "bare-supervisor"))
	recovered := false
	for _, rec := range recs {
		if !rec.DesiredRunning || rec.ExplicitlyStopped {
			continue
		}
		running := false
		if st, serr := b.rt.State(ctx, rec.ID); serr == nil && st.Status == runcStateRunning && st.Pid > 0 {
			running = true
		}
		// A not-running instance whose policy forbids another start (a completed
		// one-shot / exhausted on-failure) is left as-is.
		if !running && !restartAllowed(rec) {
			continue
		}
		// Host-reboot recovery: a dead netns pin means /run was cleared (tmpfs), so
		// the rootfs mount and netns are gone and the bundle points at a dead netns.
		// Rebuild both before (re)launching; a plain crash (pin intact, container
		// merely dead) skips straight to the launch.
		if !running && needsRebootRecovery(rec, hostrun.NetnsAlive) {
			if err := b.recoverInstance(ctx, rec); err != nil {
				log.Warn("reconcile reboot recovery failed", "instance", rec.ID, "error", err)
				continue
			}
			recovered = true
			log.Info("reconcile recovered instance after host reboot", "instance", rec.ID, "ip", rec.IP)
		}
		// Re-establish supervision: reattach a live container, adopt a running one
		// whose shim died, or relaunch a dead one — via the detached shim or the
		// in-process supervisor per CORNUS_BARE_SHIM.
		if err := b.launchSupervised(ctx, rec); err != nil {
			log.Warn("reconcile relaunch failed", "instance", rec.ID, "error", err)
		}
	}
	// Reboot recovery allocated fresh IPs; every peer's hosts file and the DNS
	// zones/listeners must follow.
	if recovered {
		if err := b.syncHosts(); err != nil {
			log.Warn("reconcile hosts sync after recovery failed", "error", err)
		}
		b.reconcileDNS()
	}
}

// readPid reads the container init pid from an instance's pid file (written by
// runc create --pid-file). Returns 0 if unreadable.
func (b *Backend) readPid(id string) int {
	data, err := os.ReadFile(b.recordDir(id) + "/pid")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// restartInstance re-runs a stopped instance from its existing bundle (same
// rootfs mount + netns + config.json, so the IP and mounts are unchanged), and
// returns the new init pid. Used by the supervisor's restart loop.
func (b *Backend) restartInstance(ctx context.Context, rec *instanceRecord) (int, error) {
	// Clear any lingering runtime state from the exited generation.
	_ = b.rt.Delete(ctx, rec.ID, true)
	fio, err := newFileIO(rec.LogPath)
	if err != nil {
		return 0, err
	}
	err = b.rt.Create(ctx, rec.ID, rec.BundleDir, createOpts{IO: fio, PidFile: b.recordDir(rec.ID) + "/pid"})
	fio.Close()
	if err != nil {
		return 0, err
	}
	if err := b.rt.Start(ctx, rec.ID); err != nil {
		return 0, err
	}
	return b.readPid(rec.ID), nil
}
