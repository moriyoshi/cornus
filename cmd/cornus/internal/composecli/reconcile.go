package composecli

import (
	"context"
	"fmt"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/compose"
)

// The live *client.Client drives the reconcile watch; assert the contract so a
// signature change on Client.Status is caught here rather than at the call site.
var _ statusPoller = (*client.Client)(nil)

// Default cadence and bound for the post-deploy reconcile watch. The plain
// POST /.cornus/v1/deploy call returns as soon as the backend has created the objects
// (for the kubernetes backend that is before any pod is scheduled), so the CLI
// polls GET /.cornus/v1/deploy/{name} to surface the cluster-side reconcile the way
// `docker compose up` reports each container's Created/Started progression.
const (
	reconcilePollInterval = 500 * time.Millisecond
	reconcileWaitTimeout  = 120 * time.Second
	// teardownWaitTimeout bounds how long `down` waits for a deployment's
	// workloads to fully terminate before giving up and returning anyway.
	teardownWaitTimeout = 120 * time.Second
)

// statusPoller is the slice of *client.Client that reportReconcile needs; a
// narrow interface keeps the reporter unit-testable with a scripted fake.
type statusPoller interface {
	Status(ctx context.Context, name string) (api.DeployStatus, error)
}

// reportReconcile polls the deployment's status until every instance is running
// (or ctx is cancelled, or timeout elapses), printing one line per instance to
// out whenever that instance's reported state changes — e.g.
//
//	web  web-0: pending
//	web  web-0: running
//
// service is the compose service name used as the line prefix (matching the
// other `up` output); deployName is the deployment/status key to poll. It never
// fails the up: on timeout it notes the wait gave up and returns the last status
// seen, so the caller can still print its summary and hold ports/mounts open.
//
// prog is a *cliout.Progress the caller owns (created once, Stopped once) rather
// than one reportReconcile starts itself: a fresh d.Progress() call starts its
// own live bubbletea program in fancy+TTY mode, and two of those running at once
// (e.g. several services reconciling concurrently) would race for terminal
// ownership. Task/Update/Done/Fail are documented safe to call concurrently, so
// every concurrent caller sharing one Progress is what makes this safe.
func reportReconcile(ctx context.Context, cl statusPoller, service, deployName string, d *cliout.Driver, prog *cliout.Progress, poll, timeout time.Duration) api.DeployStatus {
	// The deployment is up once it reports at least one instance and every
	// instance is running (an empty set is the pre-container state the backends
	// report before any container exists).
	up := func(st api.DeployStatus) bool {
		if len(st.Instances) == 0 {
			return false
		}
		for _, in := range st.Instances {
			if !in.Running {
				return false
			}
		}
		return true
	}
	// A live spinner (fancy+TTY only; a no-op otherwise) shows the service coming
	// up while the per-instance transition events scroll above it. The caller
	// prints the authoritative "up" summary via svcUp, so the task finishes
	// silently — no duplicate line in plain mode, no second line in fancy.
	// A foreground `up` blocks here until the service is running. If the deployment
	// is REMOVED out from under us while we wait — an external `down` from another
	// terminal — stop waiting at once instead of blocking the full timeout, so the
	// caller can proceed to its watchGone self-exit deterministically (within a poll
	// interval) rather than after `timeout`. Only a seen-then-empty transition
	// counts as removal (an instance existed, now none); the pre-creation empty
	// state (no instance ever) must keep waiting for the container to appear.
	sawInstances := false
	ready := func(st api.DeployStatus) bool {
		if len(st.Instances) > 0 {
			sawInstances = true
		}
		if up(st) {
			return true
		}
		return sawInstances && len(st.Instances) == 0
	}
	task := prog.Task(service + ": starting")
	st, outcome := pollTransitions(ctx, cl, service, deployName, d, poll, timeout, ready)
	if outcome == pollTimeout {
		task.Fail("")
		d.Warn("%s: gave up waiting for running state after %s (%s)", service, timeout, runningSummary(st))
	} else {
		task.Done("")
	}
	return st
}

// completionServices returns the set of selected services that some OTHER
// selected service depends on with condition service_completed_successfully.
// Such a one-shot dependency runs to completion and never reaches the Running
// state that the shared reportReconcile gate waits on, so deploying it with the
// ordinary Running gate would burn the full reconcile timeout (and log a false
// "gave up waiting for running state") before the dependent could proceed.
// Callers deploy the returned services with reportCompletion instead, making the
// one-shot's own wait completion-aware.
func completionServices(rt *runtime, selected map[string]bool) map[string]bool {
	out := map[string]bool{}
	for name := range selected {
		svc, ok := rt.project.Services()[name]
		if !ok {
			continue
		}
		for _, dep := range svc.DependsOn {
			if dep.Condition == compose.DependsOnCompleted && selected[dep.Service] {
				out[dep.Service] = true
			}
		}
	}
	return out
}

// reportCompletion is the one-shot counterpart of reportReconcile: it polls a
// completion service's deployment until it has terminated (at least one instance
// exists and NONE are Running), printing per-instance transitions like the up
// gate. Unlike reportReconcile it never warns about "running state" on the
// expected exit — a service depended on with service_completed_successfully is
// meant to exit rather than reach Running — so the one-shot's own iteration no
// longer stalls the full reconcile timeout on a gate it can never satisfy. The
// dependent's waitForDependencies still enforces the exit-0 contract; this
// reporter only governs the one-shot's own iteration.
//
// prog is caller-owned; see reportReconcile's doc comment for why.
func reportCompletion(ctx context.Context, cl statusPoller, service, deployName string, d *cliout.Driver, prog *cliout.Progress, poll, timeout time.Duration) api.DeployStatus {
	// Terminal: at least one instance exists and none are still running.
	done := func(st api.DeployStatus) bool {
		if len(st.Instances) == 0 {
			return false
		}
		for _, in := range st.Instances {
			if in.Running {
				return false
			}
		}
		return true
	}
	task := prog.Task(service + ": starting")
	st, outcome := pollTransitions(ctx, cl, service, deployName, d, poll, timeout, done)
	if outcome == pollTimeout {
		task.Fail("")
		d.Warn("%s: gave up waiting for completion after %s (%s)", service, timeout, runningSummary(st))
	} else {
		task.Done("")
	}
	return st
}

// reportTeardown is the `down` counterpart of reportReconcile: it polls the
// deployment's status until it has no instances left (fully removed), printing
// one line per instance whenever that instance's reported state changes — e.g.
//
//	web  web-0: running
//	web  web-0: pending
//	web  removed
//
// so the user sees the workloads terminate the way `docker compose down` does,
// instead of the CLI returning the moment the delete is accepted. It never fails
// the down: on timeout it notes the wait gave up and returns the last status
// seen. service is the compose service name used as the line prefix; deployName
// is the deployment/status key to poll.
func reportTeardown(ctx context.Context, cl statusPoller, service, deployName string, d *cliout.Driver, poll, timeout time.Duration) api.DeployStatus {
	gone := func(st api.DeployStatus) bool { return len(st.Instances) == 0 }
	p := d.Progress()
	defer p.Stop()
	task := p.Task(service + ": removing")
	st, outcome := pollTransitions(ctx, cl, service, deployName, d, poll, timeout, gone)
	switch outcome {
	case pollDone:
		task.Done("")
		d.Event(svcEvent(service, "removed", ""))
	case pollTimeout:
		task.Fail("")
		d.Warn("%s: gave up waiting for teardown after %s (%s)", service, timeout, runningSummary(st))
	default:
		task.Done("")
	}
	return st
}

// suppressCascaded returns nil in place of a non-nil err when gctx is already
// done. Used at the outer boundary of each service's goroutine when several
// services deploy+reconcile concurrently (see UpCmd.runForeground/upDetached):
// once one service's genuine, first-hand failure cancels the shared gctx (an
// errgroup.WithContext derivative — cancellation also covers the up's own real
// Ctrl-C/SIGTERM, since gctx is derived from the outer ctx too), every OTHER
// still-in-flight service's blocked call (waitForDependencies, Deploy,
// reportReconcile, hooks, ...) unblocks with some cancellation-shaped error of
// its own. That error is not informative — it is fallout, not a cause — and
// letting it compete with the real one for errgroup's captured "first error"
// would non-deterministically hide the actual failure behind an unrelated
// "context canceled". Only a genuine, first-hand error (gctx still live when
// it occurred) is allowed through; the top-level shutdownExit/finish call
// already distinguishes a real Ctrl-C from a genuine failure via the ORIGINAL,
// undecorated ctx, once per `up` — not per service — so nothing is lost by
// suppressing here.
func suppressCascaded(gctx context.Context, err error) error {
	if err != nil && gctx.Err() != nil {
		return nil
	}
	return err
}

// pollOutcome is how a pollTransitions loop ended.
type pollOutcome int

const (
	pollDone      pollOutcome = iota // done(st) became true
	pollTimeout                      // the timeout elapsed first
	pollCancelled                    // ctx was cancelled first
)

// pollTransitions polls deployName's status until done(st) is true (or ctx is
// cancelled, or timeout elapses), printing one line per instance to out whenever
// that instance's reported state changes. It returns the last status seen and
// which condition ended the loop; callers print their own terminal/timeout
// messages. Shared by reportReconcile (up) and reportTeardown (down), which
// differ only in the done predicate and those messages.
func pollTransitions(ctx context.Context, cl statusPoller, service, deployName string, d *cliout.Driver, poll, timeout time.Duration, done func(api.DeployStatus) bool) (api.DeployStatus, pollOutcome) {
	seen := make(map[string]string)
	var last api.DeployStatus
	// observe records state transitions since the previous poll and returns
	// whether the done predicate now holds.
	observe := func(st api.DeployStatus) bool {
		last = st
		for _, in := range st.Instances {
			if prev, ok := seen[in.ID]; !ok || prev != in.State {
				seen[in.ID] = in.State
				d.Event(serviceEvent{Service: service, Event: "transition", Instance: in.ID, State: in.State})
			}
		}
		return done(st)
	}

	// Report the state at hand immediately so the user sees the initial
	// container states without waiting a full tick.
	if st, err := cl.Status(ctx, deployName); err == nil {
		if observe(st) {
			return last, pollDone
		}
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return last, pollCancelled
		case <-deadline.C:
			return last, pollTimeout
		case <-ticker.C:
			st, err := cl.Status(ctx, deployName)
			if err != nil {
				continue // transient (e.g. not yet visible); keep polling until the deadline
			}
			if observe(st) {
				return last, pollDone
			}
		}
	}
}

// dependencySatisfied reports whether a dependency's observed status meets the
// given compose depends_on condition. It is a pure predicate over the status so
// it can be unit-tested directly and reused as the `done` closure of a
// pollTransitions wait.
//
//   - service_started: at least one instance and every instance Running (the
//     same "up" semantics reportReconcile gates on).
//   - service_healthy: at least one instance and every instance reports Health
//     "healthy". Backends that never report health (e.g. containerd) can never
//     satisfy this — the wait then falls to its timeout (see waitForDependencies).
//   - service_completed_successfully: at least one instance and every instance
//     has terminated (not Running) with ExitCode 0.
//   - any unknown condition is treated as service_started.
func dependencySatisfied(st api.DeployStatus, condition string) bool {
	if len(st.Instances) == 0 {
		return false
	}
	switch condition {
	case compose.DependsOnHealthy:
		for _, in := range st.Instances {
			if in.Health != "healthy" {
				return false
			}
		}
		return true
	case compose.DependsOnCompleted:
		for _, in := range st.Instances {
			if in.Running || in.ExitCode == nil || *in.ExitCode != 0 {
				return false
			}
		}
		return true
	default: // service_started and any unrecognized condition
		for _, in := range st.Instances {
			if !in.Running {
				return false
			}
		}
		return true
	}
}

// waitForDependencies blocks until every selected depends_on dependency of
// serviceName satisfies its condition, so the caller starts a service only once
// its dependencies are ready — the compose `depends_on` long-form contract. It
// is called at the top of each service's iteration in both `up` paths (before
// the service's own build/deploy), and dependencies are always earlier in the
// dependency-ordered `names`, so each was already deployed by the time we wait.
//
// Dependencies not in `selected` (not part of this `up`) are skipped. For each
// remaining dependency it polls the dependency's deployment status via
// pollTransitions until dependencySatisfied holds, ctx is cancelled, or timeout
// elapses. Honoring `required`: a required dependency's timeout returns an error
// that aborts the up; a non-required one logs a warning and proceeds.
//
// Backend limitations, surfaced as a timeout here (never a hang):
//   - service_healthy on containerd can never be satisfied (Health is always
//     ""), so a required healthy-dependency will time out and abort there.
//   - service_completed_successfully depends on a one-shot dependency that runs
//     to completion. Such a dependency never reaches the Running state, so its own
//     up-loop iteration deploys it with reportCompletion (see completionServices),
//     which returns as soon as the one-shot terminates rather than stalling the
//     Running gate. This wait then observes the completed instance and enforces
//     the exit-0 contract.
func waitForDependencies(ctx context.Context, rt *runtime, cl statusPoller, serviceName string, selected map[string]bool, d *cliout.Driver, poll, timeout time.Duration) error {
	svc, ok := rt.project.Services()[serviceName]
	if !ok {
		return nil
	}
	for _, dep := range svc.DependsOn {
		if !selected[dep.Service] {
			continue // dependency not part of this up; nothing to wait on
		}
		plan, ok := rt.plans[dep.Service]
		if !ok {
			continue // unknown dependency (already ignored by Order); skip
		}
		cond := dep.Condition
		if cond == "" {
			cond = compose.DependsOnStarted
		}
		d.Event(svcEvent(serviceName, "waiting", fmt.Sprintf("for %s (%s)", dep.Service, cond)))
		done := func(st api.DeployStatus) bool { return dependencySatisfied(st, cond) }
		_, outcome := pollTransitions(ctx, cl, dep.Service, plan.Resource, d, poll, timeout, done)
		switch outcome {
		case pollCancelled:
			return ctx.Err()
		case pollTimeout:
			if dep.Required {
				return fmt.Errorf("dependency %q of %q not %s within %s", dep.Service, serviceName, cond, timeout)
			}
			d.Warn("%s: optional dependency %q not %s within %s; continuing", serviceName, dep.Service, cond, timeout)
		}
	}
	return nil
}

// watchGone returns a channel that is closed once every named deployment reports
// zero instances (fully removed). It is used by a foreground `cornus compose up`
// to self-terminate when its workloads are removed elsewhere (e.g. a `down` from
// another terminal). On ctx cancellation the goroutine returns WITHOUT closing
// the channel, so the reader can tell "Ctrl-C" (ctx.Done) apart from "workloads
// removed" (this channel). A per-poll Status error counts as not-yet-gone.
func watchGone(ctx context.Context, cl statusPoller, deployNames []string, poll time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				allGone := true
				for _, n := range deployNames {
					st, err := cl.Status(ctx, n)
					if err != nil || len(st.Instances) > 0 {
						allGone = false
						break
					}
				}
				if allGone {
					close(ch)
					return
				}
			}
		}
	}()
	return ch
}
