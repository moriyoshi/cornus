//go:build linux

package containerdhost

// Startup reconcile: after a host reboot the pinned netns bind mounts under
// /run/cornus/netns are gone (/run is tmpfs), so containerd's restart monitor
// cannot resurrect tasks — their OCI specs point at dead netns paths — and
// before this pass only `cornus deploy start` repaired them. The pass runs
// once per backend (kicked at construction, retried lazily from the API entry
// points while containerd is unreachable) and rebuilds the netns + CNI
// hostrun.Attachment + pin, via the same repairNetns helper Start uses, for every
// persisted record whose desired state is running. The repaired specs let the
// monitor's next resurrection attempt succeed; cornus does not start tasks
// itself here, so it never races the monitor.

import (
	"context"
	"fmt"
	"log/slog"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/logging"
)

// ensureReconciled runs the startup reconcile pass at most once per backend.
// A pass that cannot enumerate the records (containerd unreachable,
// insufficient privilege) degrades to a warning and is retried on the next
// call instead of being consumed.
func (b *Backend) ensureReconciled(ctx context.Context) {
	b.reconcileMu.Lock()
	defer b.reconcileMu.Unlock()
	if b.reconciled {
		return
	}
	log := logging.FromContext(ctx, slog.String("component", "containerd"))
	repaired, skipped, err := b.reconcile(ctx)
	if err != nil {
		log.WarnContext(ctx, "startup netns reconcile skipped", "error", err)
		return
	}
	b.reconciled = true
	level := slog.LevelDebug
	if repaired > 0 {
		level = slog.LevelInfo
	}
	log.Log(ctx, level, "startup netns reconcile done", "repaired", repaired, "skipped", skipped)
}

// reconcile enumerates the persisted container records and repairs the ones
// needsNetnsRepair selects. Individual repair failures are logged and counted
// as skipped (Start can still repair them on demand); only failing to
// enumerate at all is an error.
func (b *Backend) reconcile(ctx context.Context) (repaired, skipped int, err error) {
	log := logging.FromContext(ctx, slog.String("component", "containerd"))
	nctx := b.ns(ctx)
	cs, err := b.client.Containers(nctx, fmt.Sprintf(`labels.%q==%q`, deploy.LabelManaged, "true"))
	if err != nil {
		return 0, 0, fmt.Errorf("containerd: reconcile: list managed containers: %w", err)
	}
	var logURIs []string
	for _, c := range cs {
		labels, lerr := c.Labels(nctx)
		if lerr != nil {
			skipped++
			continue
		}
		logURIs = append(logURIs, labels[restart.LogURILabel])
		running := false
		if task, terr := c.Task(nctx, nil); terr == nil {
			if st, serr := task.Status(nctx); serr == nil && st.Status == ctd.Running {
				running = true
			}
		}
		b.rearmHealth(c.ID(), labels, running)
		need, reason := needsNetnsRepair(labels, running, hostrun.NetnsAlive)
		if !need {
			skipped++
			log.DebugContext(ctx, "reconcile: skip", "instance", c.ID(), "reason", reason)
			continue
		}
		if _, rerr := b.repairNetns(ctx, c, labels); rerr != nil {
			skipped++
			log.WarnContext(ctx, "reconcile: netns repair failed", "instance", c.ID(), "error", rerr)
			continue
		}
		repaired++
	}
	// Now that every surviving container has been seen, drop the staged log-shim
	// binaries none of them still names. Only reachable at all on a server whose
	// executable is invisible to containerd (see hostpaths_linux.go); a no-op
	// everywhere else.
	b.gcStagedShims(logURIs)

	if repaired > 0 {
		// Repairs allocated fresh IPs; every peer's hosts file must follow.
		if herr := b.syncHosts(ctx); herr != nil {
			log.WarnContext(ctx, "reconcile: hosts sync failed", "error", herr)
		}
	}
	return repaired, skipped, nil
}

// rearmHealth restores health probing for one container after a SERVER restart.
//
// The probe engine's state is in-memory and legitimately resets here — Docker
// keeps its own in a daemon that outlives cornus. What must NOT be lost is the
// probing itself: the healthcheck is persisted on the container precisely so it
// can be re-read, and without this pass an already-deployed workload would report
// no health at all until someone redeployed it. That is silent — the workload
// runs fine — and surfaces much later as a compose `depends_on: service_healthy`
// that hangs.
//
// A stopped instance is left alone, matching Stop's own unwatch: probing one
// would exec into a dead task and report unhealthy where the pre-restart answer
// was "". `running` covers a live task; the desired-state labels cover a
// container the restart monitor is about to resurrect (this pass repairs its
// netns and deliberately does not start it).
func (b *Backend) rearmHealth(id string, labels map[string]string, running bool) {
	if isCompanion(labels) {
		return // a companion is not an app instance and has no healthcheck
	}
	if !running && !desiredRunning(labels) {
		return
	}
	// A nil healthcheck is how the engine spells "nothing to probe", so this is
	// called unconditionally rather than only when the label parses.
	b.health.Watch(id, healthcheckFromLabels(labels))
}

// desiredRunning reads a stopped container's persisted intent: is it down
// because the operator stopped it, or because the host went away under it?
//
// A container with no restart policy has no monitor to resurrect it, so a
// not-running one is simply down. This is the same desired-state reasoning
// needsNetnsRepair applies, kept separate because that one also weighs the netns.
func desiredRunning(labels map[string]string) bool {
	switch {
	case labels[restart.PolicyLabel] == "":
		return false
	case labels[restart.ExplicitlyStoppedLabel] == "true":
		return false
	case labels[restart.StatusLabel] != "" && labels[restart.StatusLabel] != string(ctd.Running):
		return false
	}
	return true
}

// needsNetnsRepair decides whether the reconcile pass rebuilds one record's
// netns pin. Repair only records whose desired state is running: restart
// policy "no" (no monitor labels) and explicitly-stopped instances are the
// user's business, a running task owns a live netns already, and an intact
// pin needs nothing. alive is the netns liveness probe (netnsAlive in
// production; injected by tests).
func needsNetnsRepair(labels map[string]string, taskRunning bool, alive func(string) bool) (bool, string) {
	nsPath := labels[labelNetNS]
	switch {
	case nsPath == "":
		return false, "no recorded netns"
	case taskRunning:
		return false, "task is running"
	case labels[restart.PolicyLabel] == "":
		return false, `restart policy "no"`
	case labels[restart.ExplicitlyStoppedLabel] == "true":
		return false, "explicitly stopped"
	case labels[restart.StatusLabel] != "" && labels[restart.StatusLabel] != string(ctd.Running):
		return false, "desired state is not running"
	case alive(nsPath):
		return false, "netns pin intact"
	}
	return true, "netns pin missing or stale"
}
