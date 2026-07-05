package deploywire

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hashicorp/yamux"

	"cornus/pkg/activity"
	"cornus/pkg/api"
	"cornus/pkg/blockcache"
	"cornus/pkg/wire"
)

// mountFn performs the kernel-9p mount of the backing unix socket at mountpoint
// (read-only when ro). It is a package var so tests can inject a fake and run
// unprivileged; production uses the platform kernelMount9P.
var mountFn = kernelMount9P

// unmountFn is the matching seam for the unmount, so a test can observe the
// ORDER of the unmount against the record that says it happened. That ordering
// is load-bearing: closing the record first leaves a window where the log says
// "gone" while the mountpoint is still there, and a process dying in it strands
// a mount with no open record to find it by.
var unmountFn = unmount9P

// MountManager creates a kernel-9p mount for each caller-local bind mount and
// rewrites the DeploySpec to point at the server-side mountpoints, so the deploy
// backend binds them like any host path (it stays unaware of 9P). Everything it
// creates lives under a per-session directory below baseDir and is removed on
// Teardown.
type MountManager struct {
	baseDir string // config.MountsDir()
	sessDir string // <baseDir>/sess-XXXX, created lazily in Prepare
	entries []mountEntry
	// meter, when set, returns per-mount rx/tx byte callbacks for the named mount
	// (rx = bytes into the container, tx = bytes out). It lets an OTel-aware caller
	// record per-mount throughput without pulling telemetry into this package.
	meter func(name string) (onRx, onTx func(int))
	// cache, when set, is the server-side block cache used for mounts flagged
	// immutable+read-only; other mounts keep the blind 9P pipe. nil disables it.
	cache *blockcache.Cache
	// rec, when set, records each mount write-ahead. nil is a working no-op.
	rec *activity.Recorder
	// ownership every mount reports, when ownerSet. See SetReportedOwner.
	ownerUID, ownerGID uint32
	ownerSet           bool
}

// blockOpts returns the block-protocol options these mounts are served with.
func (m *MountManager) blockOpts() []wire.BlockOpt {
	if !m.ownerSet {
		return nil
	}
	return []wire.BlockOpt{wire.WithReportedOwner(m.ownerUID, m.ownerGID)}
}

type mountEntry struct {
	mountpoint string
	cleanup    func() // closes the backing unix socket + its temp dir
	// act is this mount's flight-recorder activity, closed once the mountpoint
	// is actually gone. Left open, it is what tells the next server there is a
	// mount out there with nobody owning it.
	act *activity.Activity
}

// NewMountManager returns a manager rooted at baseDir (typically
// config.Config.MountsDir()).
func NewMountManager(baseDir string) *MountManager { return &MountManager{baseDir: baseDir} }

// SetMeter installs a per-mount byte meter used for each subsequently prepared
// mount. meter(name) returns onRx/onTx callbacks invoked with the byte counts
// flowing into / out of the container over that mount's 9P backing; either may be
// nil. A nil meter (the default) disables metering.
func (m *MountManager) SetMeter(meter func(name string) (onRx, onTx func(int))) { m.meter = meter }

// SetCache installs the server-side block cache used for mounts declared
// immutable+read-only. A nil cache (the default) keeps every mount on the blind
// 9P pipe.
func (m *MountManager) SetCache(cache *blockcache.Cache) { m.cache = cache }

// SetRecorder installs the flight recorder each mount is logged to. A kernel
// mount outlives the process that made it and leaves nothing behind saying it
// was cornus's, so this record is the only thing that can tell a later run the
// mountpoint exists and who it belonged to. A nil recorder (the default) is a
// no-op.
func (m *MountManager) SetRecorder(rec *activity.Recorder) { m.rec = rec }

// SetReportedOwner makes every mount this manager prepares report uid/gid as the
// owner of its files, instead of passing the caller's own ids through.
//
// Set it only where the runtime remaps ids. The caller's uids and the workload's
// are unrelated id spaces, so on a remapping runtime an untranslated id lands
// outside the container's map and the workload sees 65534 — the overflow uid —
// with writes refused. Where the runtime does NOT remap (rootful docker, bare,
// containerd), leaving this unset keeps the caller's real ownership visible,
// which is the long-standing behaviour there. Takes HOST ids; see
// deploy.HostIDsFor and wire.WithReportedOwner.
func (m *MountManager) SetReportedOwner(uid, gid uint32) {
	m.ownerUID, m.ownerGID, m.ownerSet = uid, gid, true
}

// Prepare creates a 9P backing socket and kernel-9p mount for each LocalMount,
// then returns a copy of the DeploySpec with those mount sources rewritten to
// the server-side mountpoints (and forced read-only for phase 1). On any error
// the caller must still call Teardown to unwind partial state.
func (m *MountManager) Prepare(sess *yamux.Session, spec DeployAttachSpec) (api.DeploySpec, error) {
	out := spec.Spec
	// Copy Mounts so we never mutate the caller's slice.
	mounts := make([]api.Mount, len(out.Mounts))
	copy(mounts, out.Mounts)
	out.Mounts = mounts

	if len(spec.LocalMounts) == 0 {
		return out, nil
	}
	if err := os.MkdirAll(m.baseDir, 0o755); err != nil {
		return out, err
	}
	sessDir, err := os.MkdirTemp(m.baseDir, "sess-")
	if err != nil {
		return out, err
	}
	// os.MkdirTemp creates 0700, and the CONTAINER RUNTIME has to walk this
	// directory to reach the mountpoints inside it. On a runtime that runs as
	// root that is invisible; on one that does not — rootless podman — it fails
	// the deploy outright with `statfs .../sess-<id>/m0: permission denied`,
	// which reads like a 9P problem and is not one.
	//
	// 0711: traversable, NOT listable. The session directory's name is random
	// (MkdirTemp), so it stays an unguessable capability, and the mountpoints
	// inside it keep their own permissions — this only lets a runtime that
	// already knows the path walk to it.
	if err := os.Chmod(sessDir, 0o711); err != nil {
		return out, err
	}
	m.sessDir = sessDir

	for _, lm := range spec.LocalMounts {
		if lm.Index < 0 || lm.Index >= len(mounts) {
			return out, fmt.Errorf("deploywire: local mount index %d out of range (%d mounts)", lm.Index, len(mounts))
		}
		var onRx, onTx func(int)
		if m.meter != nil {
			onRx, onTx = m.meter(lm.Name)
		}
		// Choose the backing: the writable block proxy (async writeback, cache=mmap),
		// the read-only caching proxy (immutable), or the blind pipe (default). The
		// caching modes need a configured server cache; without one they fall back
		// to the pipe.
		var (
			sock      string
			cleanup   func()
			err       error
			writeback bool
		)
		switch {
		case lm.WritableCacheable():
			sock, cleanup, err = wire.Backing9PSocketBlock(sess, lm.Name, onRx, onTx, m.cache, m.blockOpts()...)
			writeback = true
		case m.cache != nil && lm.Cacheable():
			sock, cleanup, err = wire.Backing9PSocketCached(sess, lm.Name, onRx, onTx, m.cache, m.blockOpts()...)
		default:
			sock, cleanup, err = wire.Backing9PSocketCached(sess, lm.Name, onRx, onTx, nil, m.blockOpts()...)
		}
		if err != nil {
			return out, fmt.Errorf("deploywire: backing socket for %q: %w", lm.Name, err)
		}
		mp := filepath.Join(sessDir, lm.Name)
		if err := os.MkdirAll(mp, 0o755); err != nil {
			cleanup()
			return out, err
		}
		// Write-ahead: the record is on disk BEFORE the syscall that creates the
		// effect, so a crash in between leaves a recoverable trace rather than an
		// untraceable mountpoint. Recording after the mount would leave exactly
		// the window this is meant to close.
		act := m.rec.Begin(activity.KindMount9P, mp, map[string]string{
			"deployment": spec.Spec.Name,
			"mount":      lm.Name,
			"readOnly":   strconv.FormatBool(lm.ReadOnly),
		})
		if err := mountFn(sock, mp, lm.ReadOnly, writeback); err != nil {
			act.End(err)
			cleanup()
			return out, fmt.Errorf("deploywire: kernel-9p mount %q: %w", lm.Name, err)
		}
		m.entries = append(m.entries, mountEntry{mountpoint: mp, cleanup: cleanup, act: act})
		// A file mount exports its parent dir over 9P; bind just the file within
		// the mountpoint (the deploy backend's file bind creates the target file).
		src := mp
		if lm.Subpath != "" {
			src = filepath.Join(mp, lm.Subpath)
		}
		mounts[lm.Index].Source = src
		mounts[lm.Index].ReadOnly = lm.ReadOnly
	}
	return out, nil
}

// Teardown unmounts every kernel-9p mount (containers must already be removed by
// the caller so the bind is released), closes the backing sockets, and removes
// the session directory. It is safe to call more than once.
func (m *MountManager) Teardown() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		unmountFn(e.mountpoint)
		// Closed only after the unmount, so the record can never say "gone"
		// while the mountpoint is still there.
		e.act.End(nil)
		if e.cleanup != nil {
			e.cleanup()
		}
	}
	m.entries = nil
	if m.sessDir != "" {
		_ = os.RemoveAll(m.sessDir)
		m.sessDir = ""
	}
}
