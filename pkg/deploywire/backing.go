package deploywire

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/blockcache"
	"cornus/pkg/wire"
)

// mountFn performs the kernel-9p mount of the backing unix socket at mountpoint
// (read-only when ro). It is a package var so tests can inject a fake and run
// unprivileged; production uses the platform kernelMount9P.
var mountFn = kernelMount9P

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
}

type mountEntry struct {
	mountpoint string
	cleanup    func() // closes the backing unix socket + its temp dir
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
		case m.cache != nil && lm.WritableCacheable():
			sock, cleanup, err = wire.Backing9PSocketBlock(sess, lm.Name, onRx, onTx, m.cache)
			writeback = true
		case m.cache != nil && lm.Cacheable():
			sock, cleanup, err = wire.Backing9PSocketCached(sess, lm.Name, onRx, onTx, m.cache)
		default:
			sock, cleanup, err = wire.Backing9PSocketCached(sess, lm.Name, onRx, onTx, nil)
		}
		if err != nil {
			return out, fmt.Errorf("deploywire: backing socket for %q: %w", lm.Name, err)
		}
		mp := filepath.Join(sessDir, lm.Name)
		if err := os.MkdirAll(mp, 0o755); err != nil {
			cleanup()
			return out, err
		}
		if err := mountFn(sock, mp, lm.ReadOnly, writeback); err != nil {
			cleanup()
			return out, fmt.Errorf("deploywire: kernel-9p mount %q: %w", lm.Name, err)
		}
		m.entries = append(m.entries, mountEntry{mountpoint: mp, cleanup: cleanup})
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
		unmount9P(e.mountpoint)
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
