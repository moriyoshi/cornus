//go:build linux

package barehost

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/remotecompanion"
)

// Backend deploys workloads on a bare host by driving a low-level OCI runtime
// (runc/crun/youki) directly — no container daemon. See the package doc.
type Backend struct {
	policy      hostpolicy.Policy
	dataDir     string
	runtime     string // resolved OCI runtime binary (path or name on PATH)
	snapshotter string // "" until resolved at New; then overlayfs/native

	// img is the daemonless image machinery (content store + snapshotter +
	// applier); rt drives the OCI runtime binary. Both are the seams unit tests
	// replace with fakes.
	img *imageStore
	rt  containerRuntime
	// net realizes CNI networking (bridge + portmap + pinned netns); hosts owns
	// the per-instance /etc/hosts files for inter-container name resolution; dns
	// is the server-hosted resolver (bound per network on its bridge gateway);
	// resolv owns the per-instance /etc/resolv.conf files pointing at it.
	net    *hostrun.CNIManager
	hosts  *hostrun.HostsStore
	vols   *hostrun.VolumeStore
	dns    *dnsManager
	resolv *resolvStore
	// execs tracks in-flight exec sessions (create/start/inspect/resize land on
	// the same server process).
	execs *execRegistry
	// super is the in-process restart supervisor (pidfd-waits each instance's
	// init and restarts it per policy).
	super *supervisor
	// systemdCgroup selects the cgroup path form and the runtime's cgroup driver.
	systemdCgroup bool
	// sandboxed marks a runtime whose guest resource accounting is not reflected
	// in the host cgroup files (gVisor/runsc): Stats then reads runtime-native
	// metrics (`runc events --stats`) instead of the cgroup pseudo-files. Resolved
	// once from the runtime binary name + CORNUS_BARE_STATS_SOURCE.
	sandboxed bool
	// useShim opts into the detached per-container supervision shim
	// (shim_linux.go / shim_control_linux.go) instead of the in-process supervisor:
	// CORNUS_BARE_SHIM. Off by default while the shim soaks; the observable
	// restart/stop/start contract is identical either way.
	useShim bool

	// remote opts every instance into an always-on remote companion instead of
	// leaving client-local mounts unsupported (mirrors containerdhost). Wired in
	// M6; the field is carried from New so the option surface is stable.
	remote bool
	// agentImage is the cornus-embedding image for a companion started with no
	// mount roles (a plain Apply in remote mode).
	agentImage string
	// companions is the server's per-instance companion-connection registry.
	companions *remotecompanion.Registry
}

var (
	_ deploy.Backend       = (*Backend)(nil)
	_ deploy.RemoteCapable = (*Backend)(nil)
	_ deploy.VolumeRemover = (*Backend)(nil)
)

// New constructs the bare backend per cfg (empty fields resolve from the
// environment; see Config). It validates that the configured OCI runtime binary
// is on PATH so a misconfiguration fails fast, then builds the in-process image
// store (content CAS + snapshotter) and the runtime driver. By default it
// enforces a default-deny host-privilege policy; pass WithPolicy to relax it.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	cfg, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(cfg.Runtime); err != nil {
		return nil, fmt.Errorf("bare: OCI runtime %q not found on PATH: %w (install runc/crun/youki or set CORNUS_BARE_RUNTIME)", cfg.Runtime, err)
	}
	img, err := newImageStore(cfg.DataDir, cfg.Snapshotter)
	if err != nil {
		return nil, err
	}
	systemd := detectSystemdCgroup()
	rt := newRuncRuntime(cfg.Runtime, systemd)
	b := newBackend(cfg, rt, img, systemd, opts...)
	// Re-establish supervision (and restart any desired-running instance that
	// died while the server was down) before serving deploy requests. Unlike
	// containerdhost's netns-only repair, this also relaunches — the bare backend
	// IS the restart monitor, so nothing else will.
	b.reconcile()
	return b, nil
}

// newBackend assembles the backend from resolved dependencies. It is the seam
// unit tests use to inject a fake runtime (and a nil image store for lifecycle
// tests that do not exercise the pull/rootfs path).
func newBackend(cfg Config, rt containerRuntime, img *imageStore, systemd bool, opts ...Option) *Backend {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	b := &Backend{
		policy:        o.policy,
		dataDir:       cfg.DataDir,
		runtime:       cfg.Runtime,
		snapshotter:   cfg.Snapshotter,
		img:           img,
		rt:            rt,
		net:           hostrun.NewCNIManager(cfg.DataDir, "bare", "bare"),
		hosts:         hostrun.NewHostsStore(cfg.DataDir, "bare", "bare"),
		vols:          hostrun.NewVolumeStore(cfg.DataDir, "bare", "bare"),
		dns:           newDNSManager(!envFalse(os.Getenv("CORNUS_BARE_DNS"))),
		resolv:        newResolvStore(cfg.DataDir),
		execs:         newExecRegistry(),
		systemdCgroup: systemd,
		sandboxed:     resolveSandboxed(cfg.Runtime),
		useShim:       envTrue(os.Getenv("CORNUS_BARE_SHIM")),
		remote:        o.remote,
		agentImage:    o.agentImage,
		companions:    o.companions,
	}
	b.super = newSupervisor(b)
	return b
}

// resolveSandboxed decides whether the runtime sandboxes guest resource
// accounting, so Backend.Stats must read runtime-native metrics rather than the
// host cgroup files. CORNUS_BARE_STATS_SOURCE forces the choice: "runtime" ⇒
// true, "cgroup" ⇒ false. Otherwise it auto-detects from the runtime binary
// basename — gVisor's "runsc" (and a "gvisor" alias) are sandboxed; runc, crun,
// and youki are not. Matching on the basename lets an absolute path
// (/usr/local/bin/runsc) auto-detect too, while the env override covers oddly
// named installs.
func resolveSandboxed(runtime string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORNUS_BARE_STATS_SOURCE"))) {
	case "runtime":
		return true
	case "cgroup":
		return false
	}
	switch strings.ToLower(filepath.Base(runtime)) {
	case "runsc", "gvisor":
		return true
	default:
		return false
	}
}

// detectSystemdCgroup decides whether the OCI runtime should use the systemd
// cgroup driver. CORNUS_BARE_SYSTEMD_CGROUP forces it; otherwise the default is
// cgroupfs, which runc manages directly and works on cgroup v1 and v2 without a
// systemd dependency. A fuller v1/v2 + systemd/cgroupfs detection lands with the
// cgroup work in a later milestone.
func detectSystemdCgroup() bool {
	return envTrue(os.Getenv("CORNUS_BARE_SYSTEMD_CGROUP"))
}

// envTrue reports whether an env value is a truthy setting.
func envTrue(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}

// envFalse reports whether an env value explicitly disables a feature (used for
// default-on toggles like CORNUS_BARE_DNS). An unset/empty value is NOT false.
func envFalse(v string) bool {
	switch v {
	case "0", "false", "FALSE", "no", "off":
		return true
	}
	return false
}

// Name returns the backend identifier (the CORNUS_DEPLOY_BACKEND value).
func (b *Backend) Name() string { return "bare" }

// Remote implements deploy.RemoteCapable.
func (b *Backend) Remote() bool { return b.remote }

// Close releases backend resources. Like containerdhost, running workloads
// deliberately survive: a cornus server restart must not kill deployments — but
// the server-hosted DNS listeners are process-bound, so they are shut down here
// (a restarted server rebinds them on its first Apply).
func (b *Backend) Close() error {
	if b.super != nil {
		b.super.stopAll() // stop supervising; containers keep running (re-supervised on next start)
	}
	if b.dns != nil {
		b.dns.close()
	}
	return nil
}

// The rest of the Backend surface is implemented across the package:
// Apply/Status/List/Delete/Start/Stop/Restart in lifecycle_linux.go, Logs in
// logs_linux.go, StatPath/CopyFrom/CopyTo in copy_linux.go, Stats in
// stats_linux.go, and ExecCreate/ExecStart/ExecInspect/ExecResize/Attach/
// ForwardPort/SupportsUDPPortForward in exec_linux.go.
