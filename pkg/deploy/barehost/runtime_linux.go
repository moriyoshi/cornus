//go:build linux

package barehost

import (
	"context"
	"errors"
	"strings"

	runc "github.com/containerd/go-runc"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// runtimeState is the subset of go-runc's Container state the backend acts on.
// Keeping it local means the lifecycle/supervision code does not depend on
// go-runc's concrete type, so it can be driven by a fake in unit tests.
type runtimeState struct {
	ID     string
	Pid    int
	Status string // runc's states: "created", "running", "stopped", "paused"
	Bundle string
}

// Runtime states reported by runc's `state`/`list`.
const (
	runcStateCreated = "created"
	runcStateRunning = "running"
	runcStateStopped = "stopped"
	runcStatePaused  = "paused"
)

// runtimeStats is the subset of a runtime's `events --stats` output the backend
// renders into Docker-format stats. It is the runtime-native metrics source used
// for sandboxed runtimes (gVisor/runsc) whose guest resource accounting is not
// visible in the host cgroup files sampleCgroup reads. Keeping it local (rather
// than exposing go-runc's Stats type) means the stats code and its fake stay
// decoupled from go-runc, matching runtimeState. Units follow go-runc: CPU in
// nanoseconds, memory and blkio in bytes.
type runtimeStats struct {
	CPUTotal  uint64
	CPUUser   uint64
	CPUKernel uint64
	MemUsage  uint64
	MemLimit  uint64
	MemStats  map[string]uint64
	Pids      uint64
	Blkio     []blkioEntry
}

// blkioEntry is one per-device block-IO counter from runtimeStats.
type blkioEntry struct {
	Major uint64
	Minor uint64
	Op    string
	Value uint64
}

// createOpts carries the options the backend needs when creating a container.
// IO is go-runc's own IO interface (a fake ignores it). PidFile receives the
// container init PID so the supervisor can track it; Detach lets `runc create`
// return without the runtime process staying in the foreground.
type createOpts struct {
	IO      runc.IO
	PidFile string
	Detach  bool
	NoPivot bool
}

// containerRuntime abstracts a low-level OCI runtime CLI (runc/crun/youki),
// driven through go-runc. It is the single seam over the runtime binary so the
// backend's lifecycle and supervision logic is unit-testable against a fake.
// create/start/kill/delete/state map 1:1 onto the runc CLI verbs all three
// runtimes share.
type containerRuntime interface {
	// Create sets up the container from the bundle (config.json + rootfs) with an
	// "init" process, ready to Start.
	Create(ctx context.Context, id, bundle string, opts createOpts) error
	// Start runs a created container's init process.
	Start(ctx context.Context, id string) error
	// State reports a container's runtime state. A container the runtime does not
	// know returns an error satisfying isNotExist.
	State(ctx context.Context, id string) (runtimeState, error)
	// Kill sends sig to the container's init process (all=true to every process
	// in the container's cgroup).
	Kill(ctx context.Context, id string, sig int, all bool) error
	// Delete removes a stopped container's runtime state (force also kills it).
	Delete(ctx context.Context, id string, force bool) error
	// List reports every container under the runtime's state root.
	List(ctx context.Context) ([]runtimeState, error)
	// Exec runs an additional process inside a running container and blocks until
	// it exits. Stdio is wired through opts.IO (non-TTY) or opts.ConsoleSocket
	// (TTY). A non-zero exit is reported as an error wrapping *runc.ExitError.
	Exec(ctx context.Context, id string, process specs.Process, opts runtimeExecOpts) error
	// Stats reports a running container's resource metrics from the runtime
	// itself (`runc events --stats`). This is the metrics source for sandboxed
	// runtimes (gVisor/runsc) whose guest accounting the host cgroup files do not
	// reflect; cgroupfs runtimes (runc/crun/youki) read the cgroup directly and
	// never call this.
	Stats(ctx context.Context, id string) (runtimeStats, error)
}

// runtimeExecOpts carries the stdio for an exec: IO for the non-TTY pipe path,
// or ConsoleSocket for the TTY path (runc sends the pty master over it). Detach
// is required with a console socket — runc refuses "console socket if runc will
// not detach or allocate tty", so a TTY exec sets it: runc creates the pty, sends
// the master, and returns, leaving the exec process running on the pty.
type runtimeExecOpts struct {
	IO            runc.IO
	ConsoleSocket runc.ConsoleSocket
	Detach        bool
}

// execExitCode extracts a process exit code from an Exec error: 0 for nil, the
// runtime's reported status for an *runc.ExitError, else 1 for an opaque failure.
func execExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *runc.ExitError
	if errors.As(err, &ee) {
		return ee.Status
	}
	return 1
}

// runcRuntime is the production containerRuntime: a thin adapter over *runc.Runc,
// which shells out to the configured runtime binary (Command) against a state
// root (Root) on tmpfs.
type runcRuntime struct {
	r *runc.Runc
}

// newRuncRuntime builds the production runtime driver for the given binary
// (runc/crun/youki or an absolute path), keeping runtime state under
// runcStateRoot on tmpfs and honoring the detected cgroup driver.
func newRuncRuntime(command string, systemdCgroup bool) *runcRuntime {
	return &runcRuntime{r: &runc.Runc{
		Command:       command,
		Root:          runcStateRoot,
		SystemdCgroup: systemdCgroup,
		// Setpgid isolates the runtime child in its own process group so a signal
		// to the backend/shim group does not leak to it mid-create.
		Setpgid: true,
	}}
}

func (rr *runcRuntime) Create(ctx context.Context, id, bundle string, opts createOpts) error {
	return rr.r.Create(ctx, id, bundle, &runc.CreateOpts{
		IO:      opts.IO,
		PidFile: opts.PidFile,
		Detach:  opts.Detach,
		NoPivot: opts.NoPivot,
	})
}

func (rr *runcRuntime) Start(ctx context.Context, id string) error {
	return rr.r.Start(ctx, id)
}

func (rr *runcRuntime) State(ctx context.Context, id string) (runtimeState, error) {
	c, err := rr.r.State(ctx, id)
	if err != nil {
		return runtimeState{}, err
	}
	return toRuntimeState(c), nil
}

func (rr *runcRuntime) Kill(ctx context.Context, id string, sig int, all bool) error {
	return rr.r.Kill(ctx, id, sig, &runc.KillOpts{All: all})
}

func (rr *runcRuntime) Delete(ctx context.Context, id string, force bool) error {
	return rr.r.Delete(ctx, id, &runc.DeleteOpts{Force: force})
}

func (rr *runcRuntime) List(ctx context.Context) ([]runtimeState, error) {
	cs, err := rr.r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runtimeState, 0, len(cs))
	for _, c := range cs {
		out = append(out, toRuntimeState(c))
	}
	return out, nil
}

func (rr *runcRuntime) Exec(ctx context.Context, id string, process specs.Process, opts runtimeExecOpts) error {
	return rr.r.Exec(ctx, id, process, &runc.ExecOpts{IO: opts.IO, ConsoleSocket: opts.ConsoleSocket, Detach: opts.Detach})
}

func (rr *runcRuntime) Stats(ctx context.Context, id string) (runtimeStats, error) {
	s, err := rr.r.Stats(ctx, id)
	if err != nil {
		return runtimeStats{}, err
	}
	return toRuntimeStats(s), nil
}

func toRuntimeState(c *runc.Container) runtimeState {
	return runtimeState{ID: c.ID, Pid: c.Pid, Status: c.Status, Bundle: c.Bundle}
}

// toRuntimeStats maps go-runc's Stats onto the backend-local runtimeStats,
// taking only the fields the Docker-JSON encoder consumes. CPU usage is already
// in nanoseconds and memory/blkio in bytes, matching StatsSample.
func toRuntimeStats(s *runc.Stats) runtimeStats {
	if s == nil {
		return runtimeStats{}
	}
	out := runtimeStats{
		CPUTotal:  s.Cpu.Usage.Total,
		CPUUser:   s.Cpu.Usage.User,
		CPUKernel: s.Cpu.Usage.Kernel,
		MemUsage:  s.Memory.Usage.Usage,
		MemLimit:  s.Memory.Usage.Limit,
		MemStats:  s.Memory.Raw,
		Pids:      s.Pids.Current,
	}
	for _, e := range s.Blkio.IoServiceBytesRecursive {
		out.Blkio = append(out.Blkio, blkioEntry{Major: e.Major, Minor: e.Minor, Op: e.Op, Value: e.Value})
	}
	return out
}

// isNotExist reports whether err is a runtime "container does not exist" error.
// go-runc surfaces runc's stderr verbatim (State wraps it into the error), and
// runc/crun/youki all phrase a missing container as "does not exist"; a missing
// state directory shows as "no such file or directory". Matching the message is
// the only portable option — go-runc exposes no typed sentinel.
func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "not exist") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "container with id") // crun/youki phrasings include the id
}
