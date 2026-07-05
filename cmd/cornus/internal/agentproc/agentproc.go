// Package agentproc manages the lifecycle of a single background daemon process
// reached over a unix control socket: discover-or-spawn (with a flock so two
// clients never race a spawn), a state file for pid/socket bookkeeping, the
// daemon-side listen + cleanup, and stop (a control request with a
// signalAndWait fallback). It generalizes the per-project plumbing the compose
// mounts daemon grew, so the unified client agent — and any future daemon — get
// one tested implementation.
//
// It is deliberately protocol-agnostic: the ping wire format belongs to the
// caller (the agent's control protocol), supplied as a PingFunc.
package agentproc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"cornus/cmd/cornus/internal/daemonize"
)

// Spec identifies a daemon instance and how to spawn it.
type Spec struct {
	Socket    string   // unix control socket path
	StatePath string   // JSON state file (pid/socket/log)
	LogPath   string   // detached child's stdio log
	SpawnArgs []string // argv passed to the re-exec (e.g. ["daemon","agent"])
}

// PingFunc reports whether a live instance answers on socket, and its protocol
// version. It must have no side effects beyond the ping.
type PingFunc func(socket string) (proto int, live bool)

// State is the on-disk record of a running instance.
type State struct {
	Pid    int    `json:"pid"`
	Socket string `json:"socket"`
	Log    string `json:"log,omitempty"`
}

// readyTimeout bounds how long EnsureRunning waits for a freshly spawned agent
// to answer ping.
const readyTimeout = 10 * time.Second

// spawnProcess re-execs the binary detached; a package var so tests can inject a
// fake without exec'ing the test binary.
var spawnProcess = daemonize.Spawn

// EnsureRunning returns once a live instance answers ping on spec.Socket. If
// none is, it spawns one under a flock (so concurrent callers spawn exactly one)
// and waits for it to become ready.
func EnsureRunning(spec Spec, ping PingFunc) error {
	if _, live := ping(spec.Socket); live {
		return nil
	}
	// The lock file lives beside the state file; create the dir first (before the
	// lock) so the flock target exists.
	if err := os.MkdirAll(filepath.Dir(spec.StatePath), 0o700); err != nil {
		return err
	}
	return withLock(spec.StatePath+".lock", func() error {
		// Re-check under the lock: another caller may have spawned it.
		if _, live := ping(spec.Socket); live {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(spec.Socket), 0o700); err != nil {
			return err
		}
		if spec.LogPath != "" {
			if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
				return err
			}
		}
		if _, err := spawnProcess(spec.SpawnArgs, spec.LogPath); err != nil {
			return fmt.Errorf("spawn agent: %w", err)
		}
		deadline := time.Now().Add(readyTimeout)
		for time.Now().Before(deadline) {
			if _, live := ping(spec.Socket); live {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("agent did not become ready on %s", spec.Socket)
	})
}

// Listen is the daemon side: it removes a stale socket, binds spec.Socket, writes
// the state file (with this process's pid), and returns the listener plus a
// cleanup that closes the listener and removes the socket + state. The caller
// runs its own accept loop on the returned listener.
func Listen(spec Spec) (net.Listener, func(), error) {
	if err := os.MkdirAll(filepath.Dir(spec.Socket), 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Remove(spec.Socket); err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("remove stale socket %s: %w", spec.Socket, err)
	}
	ln, err := net.Listen("unix", spec.Socket)
	if err != nil {
		return nil, nil, fmt.Errorf("listen %s: %w", spec.Socket, err)
	}
	if err := WriteState(spec.StatePath, State{Pid: os.Getpid(), Socket: spec.Socket, Log: spec.LogPath}); err != nil {
		_ = ln.Close()
		_ = os.Remove(spec.Socket)
		return nil, nil, err
	}
	cleanup := func() {
		_ = ln.Close()
		_ = os.Remove(spec.Socket)
		RemoveState(spec.StatePath)
	}
	return ln, cleanup, nil
}

// Stop stops the instance: it first tries sendStop (a graceful control request);
// if that fails and the state file names a live pid, it falls back to
// signalAndWait. State is removed on success.
func Stop(spec Spec, sendStop func(socket string) error) error {
	var stopErr error
	if sendStop != nil {
		stopErr = sendStop(spec.Socket)
	}
	if stopErr == nil {
		RemoveState(spec.StatePath)
		return nil
	}
	st, err := ReadState(spec.StatePath)
	if err != nil {
		return err
	}
	if st == nil || st.Pid == 0 {
		return stopErr // nothing to fall back to
	}
	if err := signalAndWait(st.Pid, 30*time.Second); err != nil {
		return err
	}
	RemoveState(spec.StatePath)
	return nil
}

// WriteState writes st as JSON (0600) to path.
func WriteState(path string, st State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// ReadState reads the state file; a missing file returns (nil, nil).
func ReadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("corrupt agent state %s: %w", path, err)
	}
	return &st, nil
}

// RemoveState deletes the state file, ignoring a missing one.
func RemoveState(path string) { _ = os.Remove(path) }
