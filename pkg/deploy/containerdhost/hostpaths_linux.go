//go:build linux

package containerdhost

// Path translation for a cornus containerized away from the containerd it
// drives.
//
// containerd resolves every path this backend hands it — and so does each shim
// it spawns — in the HOST's mount namespace, never in cornus's. On a
// non-containerized server the two are the same and everything here is the
// identity. Containerize the server with the socket bind-mounted in and they
// diverge: a volume backing directory cornus created at /var/lib/cornus/... is
// nowhere to be found at that path on the host, so containerd creates it fresh
// and the workload starts against an empty directory. No error, no log line.
//
// Two kinds of path need handling, and they are not symmetric:
//
//   - paths cornus creates and containerd then OPENS (volume backings, the
//     managed /etc/hosts, the log file, the log-shim binary) must be translated;
//   - paths a USER supplies as a bind source are already host paths by
//     definition — the runtime is what opens them — so they pass through
//     untouched, exactly as on a non-containerized server.
//
// The rule that separates them is ownership: anything under cornus's data dir
// is ours and gets translated, anything else is the user's and does not.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/hostenv"
)

// pathMapper is b.mapper with the documented nil default applied: no mapper
// means cornus and containerd see the same filesystem. Honouring it here rather
// than only in Config.resolve keeps a directly-constructed Backend behaving
// exactly as one did before path translation existed.
func (b *Backend) pathMapper() hostenv.Mapper {
	if b.mapper == nil {
		return hostenv.Identity()
	}
	return b.mapper
}

// hostPath translates a path cornus owns into the spelling containerd will
// resolve. what names the path in the error, which is the only place an
// operator will learn that a bind mount is about to silently do nothing.
func (b *Backend) hostPath(what, path string) (string, error) {
	host, ok := b.pathMapper().ToHost(path)
	if !ok {
		return "", fmt.Errorf(
			"containerd: %s %s is not visible to containerd: this cornus runs in a container and its data dir is not bind-mounted from the host; "+
				"bind-mount it (-v <host-path>:%s) or declare the mapping with CORNUS_HOST_PATH_MAP=%s=<host-path>",
			what, path, b.dataDir, b.dataDir)
	}
	return host, nil
}

// hostNetnsPath translates a pinned netns path into the spelling containerd will
// resolve it at, for the one moment it crosses into an OCI spec.
//
// It needs its own helper rather than riding hostMounts because the pin does not
// live under the data dir — it is under hostrun.NetnsDir, i.e. /run — so
// hostMounts's underDir(dataDir) gate skips it, and no CORNUS_HOST_PATH_MAP entry
// ever reached it either. Handing containerd a path only cornus's mount namespace
// has is a LOUD failure (the shim cannot open it, so the task never starts), which
// is why this is worth translating rather than merely warning about: an operator
// who binds the directory anywhere at all now works, instead of only one who binds
// it at exactly the same path.
//
// The empty path passes through: not every instance has a pinned netns, and "" is
// how the spec builder is told there is none.
//
// Cornus's OWN reads of the pin — hostrun.NetnsAlive, the reboot-recovery repair,
// reconcile — must keep using the local spelling. Only the spec gets this one.
func (b *Backend) hostNetnsPath(netns string) (string, error) {
	if netns == "" {
		return "", nil
	}
	return b.hostPath("netns pin", netns)
}

// hostMounts translates the sources of the mounts cornus itself provisioned,
// leaving user-supplied bind sources alone (see the package comment above).
//
// Called once, immediately before the OCI spec is built, so there is exactly
// one place where a path crosses from cornus's namespace into containerd's.
func (b *Backend) hostMounts(mounts []specs.Mount) ([]specs.Mount, error) {
	out := append([]specs.Mount(nil), mounts...)
	for i, m := range out {
		if !underDir(m.Source, b.dataDir) {
			continue
		}
		host, err := b.hostPath("mount source", m.Source)
		if err != nil {
			return nil, err
		}
		out[i].Source = host
	}
	return out, nil
}

// underDir reports whether path is dir or lies beneath it, comparing whole path
// components so a sibling directory sharing a name prefix does not match.
func underDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	path, dir = filepath.Clean(path), strings.TrimSuffix(filepath.Clean(dir), "/")
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// stagedShimDir holds copies of the cornus binary that containerd's shim can
// exec (see logShimBinary).
func (b *Backend) stagedShimDir() string { return filepath.Join(b.dataDir, "containerd", "bin") }

// logShimBinary returns the path containerd must exec for the binary log URI.
//
// The log URI names an executable that a containerd SHIM runs, on the host. Our
// own /proc/self/exe path is therefore only usable when the host can see it —
// true for a server running on the host, false for one in a container, where
// /usr/local/bin/cornus exists only inside the image and every task would fail
// its IO setup.
//
// So when our executable is not host-visible we stage a copy into the data dir,
// which is host-visible by construction (the preflight makes an unmappable data
// dir a fatal containerd misconfiguration). The copy is content-addressed for a
// reason that outlives the deploy: the chosen path is persisted in each
// container's containerd.io/restart label, and the restart monitor re-reads it
// long after cornus has been upgraded. A fixed filename would leave every
// pre-upgrade container pointing at a binary whose content had changed
// underneath it; a hashed one keeps the old binary present and valid until
// nothing refers to it (see gcStagedShims).
func (b *Backend) logShimBinary() (string, error) {
	b.shimMu.Lock()
	defer b.shimMu.Unlock()
	if b.shimPath != "" || b.shimErr != nil {
		return b.shimPath, b.shimErr
	}
	b.shimPath, b.shimErr = b.resolveLogShim()
	return b.shimPath, b.shimErr
}

func (b *Backend) resolveLogShim() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("containerd: resolve cornus executable for log shim: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// The common case: cornus and containerd share a filesystem, so the running
	// binary is exactly what the shim should exec. No copy, no new files.
	if host, ok := b.pathMapper().ToHost(exe); ok {
		return host, nil
	}
	return b.stageLogShim(exe)
}

// stageLogShim copies exe to <DataDir>/containerd/bin/cornus-<contenthash> and
// returns the HOST path of the copy.
func (b *Backend) stageLogShim(exe string) (string, error) {
	sum, err := fileDigest(exe)
	if err != nil {
		return "", fmt.Errorf("containerd: hash cornus executable for log shim: %w", err)
	}
	dst := filepath.Join(b.stagedShimDir(), "cornus-"+sum)
	host, err := b.hostPath("staged log-shim binary", dst)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(dst); err == nil && st.Mode().IsRegular() {
		return host, nil // already staged by an earlier run
	}
	if err := os.MkdirAll(b.stagedShimDir(), 0o755); err != nil {
		return "", fmt.Errorf("containerd: stage log-shim binary: %w", err)
	}
	if err := copyExecutable(exe, dst); err != nil {
		return "", fmt.Errorf("containerd: stage log-shim binary: %w", err)
	}
	return host, nil
}

// gcStagedShims removes staged binaries nothing refers to any more. inUse holds
// the log URIs recorded on the surviving containers; any staged file whose host
// path appears in none of them, and which is not the copy this process would
// hand out, is unreferenced.
//
// Best-effort by design: a staged binary left behind costs disk, while deleting
// one that is still named by a restart label would break the resurrection of a
// running workload. Every uncertainty resolves toward keeping the file.
func (b *Backend) gcStagedShims(inUse []string) {
	entries, err := os.ReadDir(b.stagedShimDir())
	if err != nil {
		return // never staged anything, or cannot look: nothing to do
	}
	current, _ := b.logShimBinary()
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "cornus-") {
			continue
		}
		path := filepath.Join(b.stagedShimDir(), e.Name())
		host, ok := b.pathMapper().ToHost(path)
		if !ok || host == current {
			continue
		}
		if slicesContainsSubstring(inUse, host) {
			continue
		}
		_ = os.Remove(path)
	}
}

// slicesContainsSubstring reports whether any element contains needle. The log
// URIs are whole "binary:///path?..." strings, so the staged path is a
// substring of the one that references it rather than the entire value.
func slicesContainsSubstring(haystack []string, needle string) bool {
	if needle == "" {
		return false
	}
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// fileDigest returns the first 16 hex digits of a file's SHA-256 — enough to
// separate cornus builds without an unwieldy filename.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// copyExecutable copies src to dst through a temporary file in the same
// directory, so a concurrent shim never execs a partially written binary.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".cornus-shim-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
