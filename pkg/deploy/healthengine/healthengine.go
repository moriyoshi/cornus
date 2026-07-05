// Package healthengine runs container health probes for the backends whose
// runtime does not run them.
//
// dockerhost and kubernetes delegate: Docker runs Config.Healthcheck itself and
// cornus reads State.Health.Status off inspect, and kubernetes turns the
// healthcheck into an exec liveness/readiness probe. containerd, bare and incus
// have no such engine, so until now the healthcheck was DROPPED — and that is
// user-visible, because a compose file with `depends_on: condition:
// service_healthy` is refused up front on a backend that reports no health
// (cmd/cornus/internal/composecli/reconcile.go, errHealthUnsupported). This is
// the engine those three run instead.
//
// The state machine deliberately matches DOCKER's rather than being merely
// reasonable: `service_healthy` compares against Docker's vocabulary, so a
// difference here is a difference in whether real compose files converge.
package healthengine

import (
	"context"
	"strings"
	"sync"
	"time"

	"cornus/pkg/api"
)

// The health states, in Docker's vocabulary. A workload with no healthcheck has
// no state at all, which api.InstanceStatus.Health already spells as "".
const (
	StateStarting  = "starting"
	StateHealthy   = "healthy"
	StateUnhealthy = "unhealthy"
)

// Docker's defaults, applied when api.Healthcheck leaves a field empty.
const (
	DefaultInterval = 30 * time.Second
	DefaultTimeout  = 30 * time.Second
	DefaultRetries  = 3
)

// Probe runs the healthcheck command inside one instance and reports the exit
// code, where 0 is a pass.
//
// A non-nil error is a FAILED PROBE, not an engine fault. Exec against a
// container that is still starting fails, and Docker counts that as the probe
// failing — treating it as an engine error instead would leave the state stuck
// at starting for exactly the workloads a start period exists to cover.
type Probe func(ctx context.Context, instanceID string, argv []string) (exitCode int, err error)

// Engine tracks one probe loop per watched instance.
//
// The shape follows barehost's supervisor (a cancel func per instance under a
// mutex, watch / unwatch / stopAll), because that is the pattern the bare backend
// already uses for restart supervision and the two sit side by side there.
type Engine struct {
	probe Probe

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
	states   map[string]string
}

// New returns an Engine that runs probes through p.
func New(p Probe) *Engine {
	return &Engine{probe: p, watchers: map[string]context.CancelFunc{}, states: map[string]string{}}
}

// plan is a healthcheck resolved into what the loop actually needs.
type plan struct {
	argv          []string
	interval      time.Duration
	startInterval time.Duration
	timeout       time.Duration
	startPeriod   time.Duration
	retries       int
}

// A nil *Engine is a working "this backend runs no probes": every method is a
// no-op and State reports "", which is already what api.InstanceStatus.Health
// means when absent. That keeps a backend that has not wired an engine — or a
// partially-constructed one in a test — from panicking on the Status path, which
// would turn a missing feature into a crash.

// Watch starts (or replaces) the probe loop for instanceID.
//
// A nil, disabled, or testless healthcheck is not an error: it stops any existing
// loop and clears the state, so an instance that loses its healthcheck on
// redeploy stops reporting a stale one.
func (e *Engine) Watch(instanceID string, hc *api.Healthcheck) {
	if e == nil {
		return
	}

	p, ok := resolve(hc)
	if !ok {
		e.Unwatch(instanceID)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())

	e.mu.Lock()
	if prev, dup := e.watchers[instanceID]; dup {
		prev()
	}
	e.watchers[instanceID] = cancel
	// Visible as starting the moment it is watched, not only after the first
	// probe returns: a caller polling Status in between must not see "" and
	// conclude there is no healthcheck.
	e.states[instanceID] = StateStarting
	e.mu.Unlock()

	go e.run(ctx, instanceID, p)
}

// Unwatch stops probing instanceID and forgets its state. The container is not
// touched; the backend's own Stop/Delete handle the runtime.
func (e *Engine) Unwatch(instanceID string) {
	if e == nil {
		return
	}

	e.mu.Lock()
	if cancel, ok := e.watchers[instanceID]; ok {
		cancel()
		delete(e.watchers, instanceID)
	}
	delete(e.states, instanceID)
	e.mu.Unlock()
}

// StopAll cancels every loop (server shutdown). Containers keep running and are
// re-watched by the next startup reconcile.
func (e *Engine) StopAll() {
	if e == nil {
		return
	}

	e.mu.Lock()
	for id, cancel := range e.watchers {
		cancel()
		delete(e.watchers, id)
	}
	e.states = map[string]string{}
	e.mu.Unlock()
}

// State reports the health of instanceID, or "" when it has no healthcheck.
//
// "" is what api.InstanceStatus.Health already means when absent, so a backend
// can assign this unconditionally instead of branching on whether a healthcheck
// was declared.
func (e *Engine) State(instanceID string) string {
	if e == nil {
		return ""
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	return e.states[instanceID]
}

func (e *Engine) setState(instanceID, s string) {
	e.mu.Lock()
	// Only for an instance still watched: a probe that returns after Unwatch must
	// not resurrect the state it just cleared.
	if _, live := e.watchers[instanceID]; live {
		e.states[instanceID] = s
	}
	e.mu.Unlock()
}

// run is the probe loop. It sleeps, probes, updates — sequentially, so a probe
// slower than the interval simply delays the next one instead of stacking up.
// That is the whole of the "one probe in flight per instance" requirement, and it
// falls out of the loop's shape rather than needing a guard.
func (e *Engine) run(ctx context.Context, instanceID string, p plan) {
	started := false // has a probe ever passed? see the start-period rule below
	fails := 0
	deadline := time.Now().Add(p.startPeriod)

	for {
		// During the start period a start_interval, when set, probes more often —
		// compose healthcheck.start_interval, which kubernetes cannot honour and
		// this engine can.
		wait := p.interval
		if !started && p.startInterval > 0 && time.Now().Before(deadline) {
			wait = p.startInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		pctx, cancel := context.WithTimeout(ctx, p.timeout)
		code, err := e.probe(pctx, instanceID, p.argv)
		cancel()
		if ctx.Err() != nil {
			return
		}

		if err == nil && code == 0 {
			// A success ENDS the start period, per Docker: once the container has
			// been seen healthy it is "started", and later failures count normally
			// even if the start period has not elapsed.
			started = true
			fails = 0
			e.setState(instanceID, StateHealthy)
			continue
		}

		// A failure inside the start period does not count toward retries and does
		// not change the state. This is the rule most easily got wrong, and getting
		// it wrong makes every slow-starting workload flap to unhealthy.
		if !started && time.Now().Before(deadline) {
			e.setState(instanceID, StateStarting)
			continue
		}
		fails++
		if fails >= p.retries {
			e.setState(instanceID, StateUnhealthy)
		}
	}
}

// resolve turns an api.Healthcheck into a plan, reporting false when there is
// nothing to probe.
func resolve(hc *api.Healthcheck) (plan, bool) {
	if hc == nil || hc.Disabled() || len(hc.Test) == 0 {
		return plan{}, false
	}
	argv := argvFor(hc.Test)
	if len(argv) == 0 {
		return plan{}, false
	}
	p := plan{
		argv:          argv,
		interval:      dur(hc.Interval, DefaultInterval),
		startInterval: dur(hc.StartInterval, 0),
		timeout:       dur(hc.Timeout, DefaultTimeout),
		startPeriod:   dur(hc.StartPeriod, 0),
		retries:       hc.Retries,
	}
	if p.retries <= 0 {
		p.retries = DefaultRetries
	}
	return p, true
}

// argvFor maps Docker's CMD form onto a command to exec.
//
// "NONE" never reaches here (Disabled screens it). A Test with no recognized
// prefix is treated as a bare argv rather than rejected: the field is documented
// in Docker's form, but refusing to probe is a worse answer than probing what was
// most likely meant.
func argvFor(test []string) []string {
	switch strings.ToUpper(test[0]) {
	case "CMD":
		return test[1:]
	case "CMD-SHELL":
		return []string{"/bin/sh", "-c", strings.Join(test[1:], " ")}
	case "NONE":
		return nil
	default:
		return test
	}
}

// dur parses a Go duration string, falling back to def. A malformed value falls
// back rather than failing the deploy: the spec is validated where it is parsed,
// and a backend refusing to start a workload over an unreadable interval would be
// a worse failure than probing on the default one.
func dur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
