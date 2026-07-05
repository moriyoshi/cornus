//go:build linux

package barehost

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/logging"
)

// stopPollInterval is how often Stop polls the runtime for the process to exit
// after SIGTERM before escalating to SIGKILL.
const stopPollInterval = 100 * time.Millisecond

// defaultStopGrace is the SIGTERM->SIGKILL grace used when a stop carries no
// explicit spec grace (Stop/Restart act on a name, not a spec).
const defaultStopGrace = 10 * time.Second

// applyHooks lets the optional-interface entrypoints (ApplyWithMounts,
// ApplyWithEgress) extend a normal Apply with a companion caretaker per replica,
// without duplicating the whole pull/rootfs/network/spec pipeline. Both fields
// are optional; the plain Apply passes a zero value.
type applyHooks struct {
	// extraAppMounts returns additional OCI mounts to bind into replica's app
	// container — the mount-sidecar's rslave scratch dirs (mounts_linux.go). nil
	// means none.
	extraAppMounts func(replica int) []specs.Mount
	// afterStart runs once replica's app instance is up and recorded, with that
	// instance's pinned netns path, so a companion caretaker can be started
	// JOINING it. A returned error fails createInstance, so Apply's rollback
	// reaps the half-built deployment (companions included). nil means none.
	afterStart func(ctx context.Context, replica int, netnsPath string) error
}

// Apply converges the host to spec by (re)creating the deployment's instances:
// it pulls the image into the in-process store, unpacks a rootfs per replica,
// generates the OCI spec, and runs each instance under the OCI runtime with
// file-backed logging, networking, volumes, and supervision. Unsupported fields
// are warned about, not silently honored.
func (b *Backend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	return b.applyInternal(ctx, spec, applyHooks{})
}

// applyInternal is Apply's body, parameterized by hooks so ApplyWithMounts /
// ApplyWithEgress can graft a per-replica companion onto the same pipeline.
func (b *Backend) applyInternal(ctx context.Context, spec api.DeploySpec, hooks applyHooks) (api.DeployStatus, error) {
	if spec.Name == "" || spec.Image == "" {
		return api.DeployStatus{}, fmt.Errorf("bare: spec requires name and image")
	}
	if err := b.policy.Validate("bare", spec); err != nil {
		return api.DeployStatus{}, err
	}
	log := logging.FromContext(ctx, slog.Group("bare", "deployment", spec.Name))
	if hc := spec.Healthcheck; hc != nil && !hc.Disabled() {
		log.WarnContext(ctx, "bare backend ignores healthcheck (no probe engine)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		// Knative Serving needs the Knative controllers on a Kubernetes cluster; the
		// bare backend runs the workload as an ordinary container, so the block is
		// ignored (no autoscaling / scale-to-zero).
		log.WarnContext(ctx, "bare backend ignores knative (kubernetes-only feature); running as an ordinary container without autoscaling")
	}
	if feats := hostrun.UnsupportedNetworkFeatures(spec); len(feats) > 0 {
		log.WarnContext(ctx, "bare backend ignores unsupported network features",
			"features", strings.Join(feats, ", "))
	}

	// Telemetry: inject the OTEL_* app env and graft a per-replica collector
	// companion onto the hooks (composes with any egress/mount companion).
	spec, hooks, err := b.withTelemetry(ctx, spec, hooks)
	if err != nil {
		return api.DeployStatus{}, err
	}

	pulled, err := b.img.pull(ctx, spec.Image)
	if err != nil {
		return api.DeployStatus{}, err
	}
	networks := hostrun.InstanceNetworks(spec)
	if err := b.net.EnsureNetworks(networks); err != nil {
		return api.DeployStatus{}, err
	}
	// Recreate semantics (keyed by name): drop any existing instances first.
	if err := b.Delete(ctx, spec.Name); err != nil {
		return api.DeployStatus{}, err
	}
	replicas := deploy.Replicas(spec)
	for i := 0; i < replicas; i++ {
		if err := b.createInstance(ctx, spec, pulled, i, hooks); err != nil {
			// Best-effort roll back the whole deployment so a partial Apply does
			// not leave orphaned instances/rootfs mounts/netns.
			_ = b.Delete(ctx, spec.Name)
			return api.DeployStatus{}, err
		}
	}
	// Publish this deployment's instances (and refresh peers) in every hosts file,
	// and refresh the server-hosted DNS zones + per-network resolver listeners.
	if err := b.syncHosts(); err != nil {
		log.WarnContext(ctx, "hosts sync failed", "error", err)
	}
	b.reconcileDNS()
	return b.Status(ctx, spec.Name)
}

// createInstance prepares replica i's rootfs, writes its bundle, and runs it
// under the OCI runtime. On any failure it unwinds what it created (rootfs
// snapshot, runtime container) so the caller sees a clean slate.
func (b *Backend) createInstance(ctx context.Context, spec api.DeploySpec, pulled pulledImage, replica int, hooks applyHooks) (retErr error) {
	id := instanceName(spec.Name, replica)
	bundle := b.bundleDir(id)
	rootfs := filepath.Join(bundle, "rootfs")
	snapKey := "cornus-" + id
	if err := os.MkdirAll(b.recordDir(id), 0o700); err != nil {
		return fmt.Errorf("bare: record dir: %w", err)
	}

	if err := b.img.prepareRootfs(ctx, snapKey, pulled.chainID, rootfs); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = b.img.removeRootfs(ctx, snapKey, rootfs)
		}
	}()

	// Networking: a pinned netns joined to the instance's networks. Published
	// host ports go to replica 0 only — portmap DNATs a host port to exactly one
	// instance, and duplicate bindings would conflict.
	networks := hostrun.InstanceNetworks(spec)
	ports := spec.Ports
	if replica > 0 {
		ports = nil
	}
	att, err := b.net.Setup(ctx, id, networks, ports)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			b.net.Teardown(ctx, id, att.Netns, networks, ports)
		}
	}()

	// Per-instance /etc/hosts (seeded with its own hostname; peers filled by
	// syncHosts after all instances exist).
	hostname := id
	if spec.Hostname != "" {
		hostname = spec.Hostname
	}
	hostsPath, err := b.hosts.Create(id, hostname, att.IP)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			b.hosts.Remove(id)
		}
	}()

	// Per-instance /etc/resolv.conf pointing at the cornus DNS resolver (its
	// bridge gateway) with the host upstreams as fallback.
	primaryNetwork := hostrun.DefaultNetwork
	if len(networks) > 0 {
		primaryNetwork = networks[0]
	}
	resolvPath, err := b.resolv.create(id, b.dnsNameservers(ctx, primaryNetwork, spec.DNSServers), spec.DNSSearch, nil)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			b.resolv.remove(id)
		}
	}()

	// Bind mounts: host binds + volume backings (seeded copy-only-when-empty from
	// the image), plus the managed /etc/hosts and /etc/resolv.conf.
	mounts, vols, err := b.instanceMounts(spec, replica)
	if err != nil {
		return err
	}
	if err := b.seedVolumes(ctx, pulled.chainID, vols); err != nil {
		return err
	}
	mounts = append(mounts,
		hostrun.OCIBindMount(hostsPath, "/etc/hosts", false),
		hostrun.OCIBindMount(resolvPath, "/etc/resolv.conf", false),
	)
	// Sidecar-mount targets (ApplyWithMounts): rslave binds of the companion's
	// shared scratch dirs, so the caretaker's 9P mount propagates into this
	// container's view. Added before spec generation so config.json carries them.
	if hooks.extraAppMounts != nil {
		mounts = append(mounts, hooks.extraAppMounts(replica)...)
	}

	cgPath := cgroupsPath(id, b.systemdCgroup)
	s, err := buildSpec(ctx, id, spec, pulled.img, rootfs, att.Netns, cgPath, mounts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bundle, 0o711); err != nil {
		return fmt.Errorf("bare: bundle dir: %w", err)
	}
	if err := writeBundleConfig(bundle, s); err != nil {
		return err
	}

	// Persist the record BEFORE launching: the detached shim reads it to drive
	// create/start (the in-process path does not need this ordering, but it is
	// harmless — Status reads runc state, not the record, for liveness).
	rec := &instanceRecord{
		ID:             id,
		App:            spec.Name,
		Image:          spec.Image,
		Replica:        replica,
		SnapshotKey:    snapKey,
		ChainID:        pulled.chainID.String(),
		BundleDir:      bundle,
		RootfsDir:      rootfs,
		CgroupPath:     cgPath,
		LogPath:        b.logPath(id),
		Restart:        deploy.RestartPolicy(spec),
		CreatedUnix:    time.Now().Unix(),
		Origin:         spec.Origin,
		Networks:       networks,
		NetNS:          att.Netns,
		IP:             att.IP,
		NetIPs:         att.IPs,
		Aliases:        hostrun.SpecAliases(spec),
		Ports:          ports,
		HostsPath:      hostsPath,
		ResolvPath:     resolvPath,
		DesiredRunning: true,
		MaxAttempts:    spec.RestartMaxAttempts,
	}
	if err := b.writeRecord(rec); err != nil {
		return err
	}
	// Launch under supervision: the detached shim (CORNUS_BARE_SHIM) or the
	// in-process supervisor owns runc create/start + the restart loop.
	if err := b.launchSupervised(ctx, rec); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			b.teardownSupervised(ctx, id)
		}
	}()
	// Companion caretaker (ApplyWithMounts / ApplyWithEgress): started AFTER the
	// app instance is up and recorded, joining its netns. A failure unwinds this
	// instance (and the deferred rollback above), so Apply's outer rollback reaps
	// the whole deployment.
	if hooks.afterStart != nil {
		if err := hooks.afterStart(ctx, replica, att.Netns); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a deployment's instances. Delete-if-exists: a name with no
// records is a no-op success. Each instance's runtime container, rootfs mount +
// snapshot, bundle, log, and record are reaped.
func (b *Backend) Delete(ctx context.Context, name string) error {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return err
	}
	// Reap companions before app instances: a companion joins its app instance's
	// netns, so it must be gone before that netns is torn down. (Kill+delete is
	// idempotent, so even a stale companion of a since-recreated instance is safe.)
	sort.SliceStable(recs, func(i, j int) bool {
		return isCompanionRec(recs[i]) && !isCompanionRec(recs[j])
	})
	for _, rec := range recs {
		// Stop supervision (in-process watcher or detached shim) and force-kill the
		// container so the teardown exit is not treated as a crash.
		b.teardownSupervised(ctx, rec.ID)
		// A companion owns no CNI/hosts/resolv state of its own (it joins the app's
		// netns); only app instances tear those down.
		if !isCompanionRec(rec) {
			if b.net != nil {
				b.net.Teardown(ctx, rec.ID, rec.NetNS, rec.Networks, rec.Ports)
			}
			if b.hosts != nil {
				b.hosts.Remove(rec.ID)
			}
			if b.resolv != nil {
				b.resolv.remove(rec.ID)
			}
		}
		if b.img != nil && rec.SnapshotKey != "" && rec.RootfsDir != "" {
			_ = b.img.removeRootfs(ctx, rec.SnapshotKey, rec.RootfsDir)
		}
		_ = os.RemoveAll(rec.BundleDir)
		_ = os.Remove(rec.LogPath)
		if err := b.removeRecord(rec.ID); err != nil {
			return err
		}
	}
	if len(recs) > 0 {
		b.reapAnonymousVolumes(name)
		b.gcNetworks()
		// Refresh the remaining instances' hosts files (and the DNS zones +
		// listeners) now this app's peers are gone.
		if err := b.syncHosts(); err != nil {
			logging.FromContext(ctx, slog.Group("bare", "deployment", name)).
				WarnContext(ctx, "hosts sync after delete failed", "error", err)
		}
		b.reconcileDNS()
	}
	return nil
}

// gcNetworks removes conflists for user networks no live instance references,
// freeing their subnet allocation. The default network is never reaped.
func (b *Backend) gcNetworks() {
	if b.net == nil {
		return
	}
	recs, err := b.listRecords()
	if err != nil {
		return
	}
	inUse := map[string]bool{}
	for _, rec := range recs {
		for _, n := range rec.Networks {
			inUse[n] = true
		}
	}
	entries, err := os.ReadDir(b.net.ConfDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "cornus-"), ".conflist")
		if name == "" || name == hostrun.DefaultNetwork || inUse[name] {
			continue
		}
		_ = b.net.RemoveNetwork(name)
	}
}

// Stop stops a deployment's instances without removing them: it SIGTERMs each
// (escalating to SIGKILL after a grace period) and deletes the runtime container,
// but keeps the bundle, rootfs mount, log, and record so Start can re-run it.
func (b *Backend) Stop(ctx context.Context, name string) error {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	for _, rec := range recs {
		// Persist the stop intent BEFORE unwatching/killing: if the exit and the
		// stop interleave, the supervisor re-reads explicitly-stopped and does not
		// resurrect it; the startup reconcile honors it too.
		rec.DesiredRunning = false
		rec.ExplicitlyStopped = true
		_ = b.writeRecord(rec)
		b.stopSupervised(ctx, rec.ID)
	}
	return nil
}

// stopInstance graceful-stops one runtime container: SIGTERM, poll for exit up to
// defaultStopGrace, then SIGKILL, then remove the runtime state (keeping the
// bundle/rootfs so Start can recreate it). Best-effort throughout.
func (b *Backend) stopInstance(ctx context.Context, id string) {
	st, err := b.rt.State(ctx, id)
	if err != nil {
		return // already gone
	}
	if st.Status == runcStateRunning || st.Status == runcStateCreated {
		_ = b.rt.Kill(ctx, id, int(syscall.SIGTERM), false)
		deadline := time.Now().Add(defaultStopGrace)
		for time.Now().Before(deadline) {
			cur, err := b.rt.State(ctx, id)
			if err != nil || cur.Status == runcStateStopped {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(stopPollInterval):
			}
		}
		if cur, err := b.rt.State(ctx, id); err == nil && cur.Status != runcStateStopped {
			_ = b.rt.Kill(ctx, id, int(syscall.SIGKILL), true)
		}
	}
	_ = b.rt.Delete(ctx, id, true)
}

// Start (re)starts a stopped deployment's instances from their persisted bundle.
func (b *Backend) Start(ctx context.Context, name string) error {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	for _, rec := range recs {
		// Clear the stopped flags and reset the backoff counter BEFORE launching, so
		// the record the shim reads reflects the desired-running intent.
		rec.DesiredRunning = true
		rec.ExplicitlyStopped = false
		rec.RestartCount = 0
		if err := b.writeRecord(rec); err != nil {
			return err
		}
		// launchSupervised recreates from the persisted bundle (or adopts an
		// already-running container) and re-arms supervision — the detached shim or
		// the in-process supervisor per CORNUS_BARE_SHIM.
		if err := b.launchSupervised(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// Restart stops then starts a deployment's instances.
func (b *Backend) Restart(ctx context.Context, name string) error {
	if err := b.Stop(ctx, name); err != nil {
		return err
	}
	return b.Start(ctx, name)
}

// Status reports a deployment's observed state from its records + the runtime.
func (b *Backend) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return api.DeployStatus{}, err
	}
	st := api.DeployStatus{Name: name, Backend: b.Name()}
	for _, rec := range recs {
		if isCompanionRec(rec) {
			continue // companions are not app replicas
		}
		if st.Image == "" {
			st.Image = rec.Image
		}
		if st.Origin == nil {
			st.Origin = rec.Origin
		}
		st.Instances = append(st.Instances, b.instanceStatus(ctx, rec))
	}
	return st, nil
}

// List reports all bare-managed deployments, grouped by name.
func (b *Backend) List(ctx context.Context) ([]api.DeployStatus, error) {
	all, err := b.listRecords()
	if err != nil {
		return nil, err
	}
	byApp := map[string]*api.DeployStatus{}
	var order []string
	for _, rec := range all {
		if isCompanionRec(rec) {
			continue // companions are not app replicas
		}
		ds := byApp[rec.App]
		if ds == nil {
			ds = &api.DeployStatus{Name: rec.App, Backend: b.Name(), Image: rec.Image, Origin: rec.Origin}
			byApp[rec.App] = ds
			order = append(order, rec.App)
		}
		ds.Instances = append(ds.Instances, b.instanceStatus(ctx, rec))
	}
	out := make([]api.DeployStatus, 0, len(order))
	for _, a := range order {
		out = append(out, *byApp[a])
	}
	return out, nil
}

// instanceStatus reports one instance's observed state in Docker-ish terms,
// mapping the OCI runtime's states. The guaranteed-portable subset is "running"
// (with Running == true); other states carry the runtime's own vocabulary.
func (b *Backend) instanceStatus(ctx context.Context, rec *instanceRecord) api.InstanceStatus {
	is := api.InstanceStatus{ID: rec.ID, State: "exited"}
	st, err := b.rt.State(ctx, rec.ID)
	if err != nil {
		return is // no runtime state: stopped/gone
	}
	switch st.Status {
	case runcStateRunning:
		is.State = "running"
		is.Running = true
	case runcStateCreated:
		is.State = "created"
	case runcStatePaused:
		is.State = "paused"
	case runcStateStopped:
		is.State = "exited"
	default:
		is.State = st.Status
	}
	return is
}

// fileIO gives the OCI runtime file-backed stdio: the container inherits the log
// file fd directly (stdin is /dev/null), so its output survives a cornus server
// restart — the file stays open in the container process, not held by a pipe the
// server owns, which also avoids a SIGPIPE killing the workload when the server
// exits. M4's supervision shim replaces this with logfmt-framed, stream-separated
// stdio; M1 keeps it simple and restart-safe. Logs frames the raw bytes as a
// single stdcopy stdout stream.
type fileIO struct {
	log  *os.File
	null *os.File
}

func newFileIO(logPath string) (*fileIO, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("bare: logs dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("bare: open log: %w", err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("bare: open /dev/null: %w", err)
	}
	return &fileIO{log: f, null: null}, nil
}

func (io *fileIO) Stdin() io.WriteCloser { return nil }
func (io *fileIO) Stdout() io.ReadCloser { return nil }
func (io *fileIO) Stderr() io.ReadCloser { return nil }

func (io *fileIO) Set(cmd *exec.Cmd) {
	cmd.Stdin = io.null
	cmd.Stdout = io.log
	cmd.Stderr = io.log
}

func (io *fileIO) Close() error {
	_ = io.log.Close()
	_ = io.null.Close()
	return nil
}
