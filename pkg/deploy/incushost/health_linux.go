//go:build linux

package incushost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// Container health on incus.
//
// incus has no instance-level probe of its own, so until now the healthcheck was
// dropped with a warning. That is user-visible rather than cosmetic: a compose
// file with `depends_on: condition: service_healthy` is REFUSED up front on a
// backend that reports no health, so an ordinary compose project could not deploy
// here at all. cornus runs the probes itself (pkg/deploy/healthengine).
//
// Of the three backends that needed this, incus has the best-shaped exec for it:
// its operation metadata carries the process's actual RETURN CODE, where the bare
// backend's OCI runtime only reports non-zero as an error.

var _ deploy.HealthReporter = (*Backend)(nil)

// ReportsHealth implements deploy.HealthReporter.
func (b *Backend) ReportsHealth() bool { return true }

// healthcheckConfigKey records the workload's healthcheck on the INSTANCE, so the
// probe engine can re-arm after a server restart.
//
// The probe state legitimately resets then — Docker keeps its own in a daemon that
// outlives cornus — but losing the healthcheck DEFINITION would mean health never
// came back for an already-deployed workload, which surfaces much later as a
// compose dependency that hangs rather than as anything that looks like a fault.
// Same reasoning, and the same user.-prefixed key space, as anonVolumesConfigKey
// and the cornus.origin.* keys.
var healthcheckConfigKey = configKeyPrefix + "cornus.healthcheck"

// nopWriteCloser discards a probe's output: a healthcheck is graded on its exit
// status and nothing reads what it prints. incus's exec args want WriteClosers.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// probeExec runs argv in one instance and reports its exit code.
//
// It does not go through ExecCreate/ExecStart: those maintain a client-facing
// session (registry entry, PTY resize control channel, stdcopy framing) that a
// probe has no use for, and each probe would leave an entry behind.
func (b *Backend) probeExec(ctx context.Context, instanceID string, argv []string) (int, error) {
	post := incusapi.InstanceExecPost{
		Command:     argv,
		WaitForWS:   true,
		Environment: map[string]string{},
	}
	done := make(chan bool)
	args := &incus.InstanceExecArgs{
		Stdout:   nopWriteCloser{io.Discard},
		Stderr:   nopWriteCloser{io.Discard},
		DataDone: done,
	}
	op, err := b.conn.Exec(instanceID, post, args)
	if err != nil {
		// An instance that is not running yet cannot be exec'd into. That is a
		// FAILED probe, not an engine fault — it is what a start period absorbs.
		return 0, fmt.Errorf("incus: health probe exec on %s: %w", instanceID, err)
	}

	// Wait for the operation, but never past the probe's own deadline: healthengine
	// bounds each probe by Healthcheck.Timeout, and a hung command must not pin
	// this goroutine for the life of the deployment.
	waitErr := make(chan error, 1)
	go func() { waitErr <- op.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			return 0, err
		}
	case <-ctx.Done():
		_ = op.Cancel()
		return 0, ctx.Err()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return execReturnCode(op), nil
}

// healthcheckFromConfig reads the healthcheck an instance was created with.
func healthcheckFromConfig(config map[string]string) *api.Healthcheck {
	raw := config[healthcheckConfigKey]
	if raw == "" {
		return nil
	}
	var hc api.Healthcheck
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		// Unreadable means no probing, which is what this backend did before the
		// engine existed. Failing the deploy over it would be worse.
		return nil
	}
	return &hc
}

// healthcheckToConfig renders the healthcheck for the instance config, or "" when
// there is nothing to probe. Screening here means an instance never carries a
// check syncHealth would then have to ignore.
func healthcheckToConfig(hc *api.Healthcheck) string {
	if hc == nil || hc.Disabled() || len(hc.Test) == 0 {
		return ""
	}
	data, err := json.Marshal(hc)
	if err != nil {
		return ""
	}
	return string(data)
}

// syncHealth (re)arms probing for every app instance of a deployment, reading the
// healthcheck off each instance's own config.
//
// Reading the instance rather than taking the spec keeps ONE path: the deploy that
// just created them and the restart that found them already there resolve the
// healthcheck identically, so the two cannot diverge.
func (b *Backend) syncHealth(_ context.Context, name string) {
	// appInstances separates replicas from companions, so a companion is never
	// probed — it runs the caretaker, not the workload the healthcheck describes.
	replicas, _, err := b.appInstances(name)
	if err != nil {
		return
	}
	for _, in := range replicas {
		// A nil healthcheck is how the engine clears a stale one, so this is called
		// unconditionally rather than only when a check exists.
		b.health.Watch(in.Name, healthcheckFromConfig(in.Config))
	}
}

// unwatchHealth stops probing every instance of a deployment (stop / delete).
func (b *Backend) unwatchHealth(_ context.Context, name string) {
	replicas, _, err := b.appInstances(name)
	if err != nil {
		return
	}
	for _, in := range replicas {
		b.health.Unwatch(in.Name)
	}
}
