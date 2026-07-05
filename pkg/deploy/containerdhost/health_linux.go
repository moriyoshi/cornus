//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// Container health on containerd.
//
// containerd runs no health probes of its own, so until now the healthcheck was
// dropped with a warning. That is user-visible rather than cosmetic: a compose
// file with `depends_on: condition: service_healthy` is REFUSED up front on a
// backend that reports no health, so an ordinary compose project could not deploy
// here at all. cornus runs the probes itself instead (pkg/deploy/healthengine).

var _ deploy.HealthReporter = (*Backend)(nil)

// ReportsHealth implements deploy.HealthReporter: this backend runs the probe
// engine below and fills api.InstanceStatus.Health from it.
func (b *Backend) ReportsHealth() bool { return true }

// healthExecSeq makes each probe's containerd exec id unique.
//
// containerd requires an exec id unique among a task's LIVE processes. Reusing a
// fixed id would be fine in the common case — probes are sequential per instance
// and each is deleted before the next — but not after a probe that timed out and
// whose process is still being killed, which is exactly when a second failure
// would be most confusing to diagnose.
var healthExecSeq atomic.Uint64

// probeExec runs argv inside one container and reports its exit code.
//
// It deliberately does NOT go through ExecCreate/ExecStart. Those maintain a
// client-facing exec session (registry entry, stdin pump, TTY resize, stdcopy
// framing) that a probe has no use for, and every probe would leave a registry
// entry behind. cio.NullIO discards the output, which also removes the need for
// the fake connection ExecStart would otherwise require.
func (b *Backend) probeExec(ctx context.Context, containerID string, argv []string) (int, error) {
	nctx := b.ns(ctx)
	c, err := b.client.LoadContainer(nctx, containerID)
	if err != nil {
		return 0, err
	}
	task, err := runningTask(nctx, c)
	if err != nil {
		// Not running yet is a FAILED probe, not an engine fault — that is what a
		// start period exists to absorb.
		return 0, err
	}
	baseSpec, err := c.Spec(nctx)
	if err != nil {
		return 0, err
	}
	pspec, err := execProcessSpec(baseSpec, api.ExecConfig{Cmd: argv})
	if err != nil {
		return 0, err
	}

	execID := fmt.Sprintf("cornus-health-%d", healthExecSeq.Add(1))
	process, err := task.Exec(nctx, execID, pspec, cio.NullIO)
	if err != nil {
		return 0, fmt.Errorf("containerd: health probe exec in %s: %w", containerID, err)
	}
	defer func() {
		// Cleanup must outlive the probe's own deadline: on the timeout path ctx is
		// already done, and a Delete on a cancelled context would leak the process
		// AND its exec id. WithProcessKill covers the still-running case.
		cctx := b.ns(context.WithoutCancel(ctx))
		_, _ = process.Delete(cctx, ctd.WithProcessKill)
	}()

	waitCh, err := process.Wait(nctx)
	if err != nil {
		return 0, err
	}
	if err := process.Start(nctx); err != nil {
		return 0, err
	}
	select {
	case exit := <-waitCh:
		if err := exit.Error(); err != nil {
			return 0, err
		}
		return int(exit.ExitCode()), nil
	case <-ctx.Done():
		// Timed out (healthengine bounds each probe by Healthcheck.Timeout). The
		// deferred Delete kills the process.
		return 0, ctx.Err()
	}
}

// healthcheckLabel carries the workload's healthcheck on the container itself.
//
// It is stored rather than remembered in memory because the engine's watch has to
// survive a SERVER restart. The probe state legitimately resets then (Docker keeps
// its own in the daemon, which outlives cornus), but losing the healthcheck
// DEFINITION would mean health never came back at all for an already-deployed
// workload — a silent regression that only shows up as a compose dependency
// hanging much later.
func healthcheckFromLabels(labels map[string]string) *api.Healthcheck {
	raw := labels[labelHealthcheck]
	if raw == "" {
		return nil
	}
	var hc api.Healthcheck
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		// An unreadable label means no probing, which is what this backend did
		// before the engine existed. Failing the deploy over it would be worse.
		return nil
	}
	return &hc
}

// syncHealth (re)arms probing for every app instance of a deployment, reading
// each container's own healthcheck label.
//
// Reading the label rather than taking the spec as an argument keeps ONE path:
// the deploy that just created the containers and the restart that found them
// already there resolve the healthcheck the same way, so the two cannot diverge.
func (b *Backend) syncHealth(ctx context.Context, name string) {
	cs, err := b.instances(ctx, name)
	if err != nil {
		return
	}
	nctx := b.ns(ctx)
	for _, c := range cs {
		labels, err := c.Labels(nctx)
		if err != nil || isCompanion(labels) {
			continue // a companion is not an app instance and has no healthcheck
		}
		// Watch with a nil healthcheck is how the engine clears a stale one, so
		// this is called unconditionally rather than only when a check exists.
		b.health.Watch(c.ID(), healthcheckFromLabels(labels))
	}
}

// unwatchHealth stops probing every instance of a deployment (stop / delete).
func (b *Backend) unwatchHealth(ctx context.Context, name string) {
	cs, err := b.instances(ctx, name)
	if err != nil {
		return
	}
	for _, c := range cs {
		b.health.Unwatch(c.ID())
	}
}
