//go:build linux

package barehost

import (
	"context"
	"io"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// Container health on the bare backend.
//
// cornus IS the runtime here, so nothing else was ever going to run the probe.
// Until now the healthcheck was dropped with a warning, which is user-visible
// rather than cosmetic: a compose file with `depends_on: condition:
// service_healthy` is REFUSED up front on a backend that reports no health, so an
// ordinary compose project could not deploy here at all.
//
// The probe loop lives in pkg/deploy/healthengine and sits beside the restart
// supervisor (supervise_linux.go), whose watch/unwatch shape it deliberately
// mirrors — the two track the same instances for different reasons.

var _ deploy.HealthReporter = (*Backend)(nil)

// ReportsHealth implements deploy.HealthReporter.
func (b *Backend) ReportsHealth() bool { return true }

// probeExec runs argv inside one instance and reports whether it passed.
//
// The OCI runtime reports a non-zero container exit as an ERROR rather than a
// code, so a failing probe and an unrunnable one are indistinguishable here. That
// is not a loss: healthengine treats both as a failed probe, which is also what
// Docker does — a healthcheck that cannot run is not a healthy container.
func (b *Backend) probeExec(ctx context.Context, instanceID string, argv []string) (int, error) {
	rec, err := b.readRecord(instanceID)
	if err != nil {
		return 0, err
	}
	base, err := readBundleConfig(rec.BundleDir)
	if err != nil {
		return 0, err
	}
	pspec, err := execProcessSpec(base, api.ExecConfig{Cmd: argv})
	if err != nil {
		return 0, err
	}
	// ctx already carries Healthcheck.Timeout (healthengine bounds each probe), so
	// a hung probe is killed by the runtime when the context is cancelled.
	if err := b.rt.Exec(ctx, rec.ID, *pspec, runtimeExecOpts{IO: &copyIO{stdout: io.Discard, stderr: io.Discard}}); err != nil {
		return 1, err
	}
	return 0, nil
}

// syncHealth (re)arms probing for every app instance of a deployment, reading the
// healthcheck off each instance's persisted record.
//
// Reading the record rather than taking the spec keeps ONE path: the deploy that
// just wrote the records and the restart that found them already there resolve the
// healthcheck identically, so the two cannot diverge. It is also what makes health
// come back after a SERVER restart, when the spec is long gone.
func (b *Backend) syncHealth(_ context.Context, name string) {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec.Role != "" {
			continue // a companion is not an app instance
		}
		// A nil healthcheck is how the engine clears a stale one, so this is called
		// unconditionally rather than only when a check exists.
		b.health.Watch(rec.ID, rec.Healthcheck)
	}
}

// unwatchHealth stops probing every instance of a deployment (stop / delete).
func (b *Backend) unwatchHealth(_ context.Context, name string) {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return
	}
	for _, rec := range recs {
		b.health.Unwatch(rec.ID)
	}
}

// healthcheckToRecord returns the healthcheck worth persisting, or nil when there
// is nothing to probe. Screening here rather than at every read means a record
// never carries a check that syncHealth would then have to ignore.
func healthcheckToRecord(hc *api.Healthcheck) *api.Healthcheck {
	if hc == nil || hc.Disabled() || len(hc.Test) == 0 {
		return nil
	}
	return hc
}
