//go:build linux

package hostrun

// Volume backings: plain host directories under <DataDir>/<subdir>/volumes bind-
// mounted into instances (the dockerhost volume semantics by hand). A named
// volume survives Delete and is shared across deployments; an anonymous volume is
// per-instance and reaped with the deployment. A fresh (empty) volume is seeded
// copy-only from the image's baked content at its target path. Only SeedVolumes'
// snapshot SOURCE differs per backend (bare's in-process snapshotter + a known
// chainID vs containerd's client snapshot service + a RootFS round-trip), so it
// takes the resolved snapshotter + chainID as parameters.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/continuity/fs"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
)

// VolumeStore owns the on-disk volume backings under <DataDir>/<subdir>/volumes.
type VolumeStore struct {
	dataDir   string
	subdir    string
	errPrefix string
}

// NewVolumeStore builds a store rooted at <dataDir>/<subdir>/volumes; errPrefix
// ("bare"/"containerd") heads its errors.
func NewVolumeStore(dataDir, subdir, errPrefix string) *VolumeStore {
	return &VolumeStore{dataDir: dataDir, subdir: subdir, errPrefix: errPrefix}
}

// ValidateVolumeName rejects a volume or deployment name that is not usable as a
// single path element.
//
// This is a security boundary, not tidiness. These names are joined onto the data
// dir to form a backing directory, and filepath.Join CLEANS its result — so an
// unvalidated ".." escapes the store entirely: a name of "../../../../../../etc"
// resolves to "/etc". That gave two primitives to anyone who could reach the
// deploy API, on every backend using this store (containerd, bare):
//
//   - an arbitrary host bind mount, since InstanceMounts binds the resolved
//     directory into the container read-write. It bypasses pkg/deploy/hostpolicy
//     completely, because that only inspects spec.Mounts and never spec.Volumes.
//   - arbitrary directory deletion, since RemoveVolume is os.RemoveAll of the
//     resolved directory and is reachable from DELETE /.cornus/v1/volume/{name}.
//
// dockerhost is unaffected: Docker validates volume names itself.
//
// The rule is deliberately narrow — a name must be exactly one path element — so
// it cannot be defeated by a spelling this check did not anticipate. Callers get
// the error; the directory builders below refuse to return a path without it.
func ValidateVolumeName(kind, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%s name is empty", kind)
	case name == "." || name == "..":
		return fmt.Errorf("%s name %q is a path traversal", kind, name)
	case strings.ContainsRune(name, '/'), strings.ContainsRune(name, filepath.Separator):
		return fmt.Errorf("%s name %q contains a path separator; it must be a single name, not a path", kind, name)
	case strings.ContainsRune(name, 0):
		return fmt.Errorf("%s name contains a NUL byte", kind)
	}
	return nil
}

// underDir reports whether path is dir itself or lies beneath it. It is the
// belt-and-braces half of the check: ValidateVolumeName rejects the spellings we
// know about, and this asserts the outcome regardless.
func underDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// namedVolumesRoot / anonVolumesRoot are the two containment bases.
func (s *VolumeStore) namedVolumesRoot() string {
	return filepath.Join(s.dataDir, s.subdir, "volumes", "named")
}

func (s *VolumeStore) anonVolumesRoot() string {
	return filepath.Join(s.dataDir, s.subdir, "volumes", "anon")
}

// NamedVolumeDir is the shared, project-scoped backing dir for a named volume.
// It returns an error rather than a path for a name that would escape the store
// (see ValidateVolumeName).
func (s *VolumeStore) NamedVolumeDir(name string) (string, error) {
	if err := ValidateVolumeName("volume", name); err != nil {
		return "", fmt.Errorf("%s: %w", s.errPrefix, err)
	}
	root := s.namedVolumesRoot()
	dir := filepath.Join(root, name)
	if !underDir(root, dir) {
		return "", fmt.Errorf("%s: volume name %q escapes the volume store", s.errPrefix, name)
	}
	return dir, nil
}

// AnonVolumesDir is a deployment's anonymous-volume root. The deployment name is
// validated for the same reason a volume name is: it is joined onto the data dir.
func (s *VolumeStore) AnonVolumesDir(app string) (string, error) {
	if err := ValidateVolumeName("deployment", app); err != nil {
		return "", fmt.Errorf("%s: %w", s.errPrefix, err)
	}
	root := s.anonVolumesRoot()
	dir := filepath.Join(root, app)
	if !underDir(root, dir) {
		return "", fmt.Errorf("%s: deployment name %q escapes the volume store", s.errPrefix, app)
	}
	return dir, nil
}

func (s *VolumeStore) anonVolumeDir(app string, replica int, target string) (string, error) {
	base, err := s.AnonVolumesDir(app)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(base, fmt.Sprintf("%d-%s", replica, hex.EncodeToString(sum[:])[:8])), nil
}

// VolumeBacking is one resolved volume: its host directory and container target.
type VolumeBacking struct {
	HostDir string
	Target  string
}

// InstanceMounts resolves an instance's OCI bind mounts: the spec's host binds
// (already policy-validated) plus a backing directory per volume. It returns the
// volume backings separately so the caller can seed fresh ones from the image.
func (s *VolumeStore) InstanceMounts(spec api.DeploySpec, replica int) ([]specs.Mount, []VolumeBacking, error) {
	var mounts []specs.Mount
	for _, m := range spec.Mounts {
		mounts = append(mounts, OCIBindMount(m.Source, m.Target, m.ReadOnly))
	}
	var vols []VolumeBacking
	for _, v := range spec.Volumes {
		if v.Target == "" {
			return nil, nil, fmt.Errorf("%s: volume requires a target path", s.errPrefix)
		}
		// A volume's compose driver / driver_opts / labels are Docker-volume-plugin
		// metadata with no analogue for a plain host-directory backing (NO-OP).
		var dir string
		var err error
		if v.Name != "" {
			dir, err = s.NamedVolumeDir(v.Name)
		} else {
			dir, err = s.anonVolumeDir(spec.Name, replica, v.Target)
		}
		if err != nil {
			return nil, nil, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("%s: create volume dir: %w", s.errPrefix, err)
		}
		mounts = append(mounts, OCIBindMount(dir, v.Target, v.ReadOnly))
		vols = append(vols, VolumeBacking{HostDir: dir, Target: v.Target})
	}
	return mounts, vols, nil
}

// ReapAnonymousVolumes removes a deployment's anonymous volume directories (named
// volumes survive, docker parity). Best-effort.
func (s *VolumeStore) ReapAnonymousVolumes(name string) {
	dir, err := s.AnonVolumesDir(name)
	if err != nil {
		// An unusable name can never have produced a directory, so there is
		// nothing to reap. Skipping is right; deleting anything would be wrong.
		return
	}
	_ = os.RemoveAll(dir)
}

// RemoveVolume removes the host directory backing a named, project-scoped volume
// (deploy.VolumeRemover, for `compose down --volumes`). Delete-if-exists.
func (s *VolumeStore) RemoveVolume(name string) error {
	dir, err := s.NamedVolumeDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// SeedVolumes copies the image's content at each fresh (empty) volume's target
// path into its backing directory, via a read-only snapshot view of the image
// chain (identified by chainID, resolved by the caller in the given snapshotter).
// A target the image does not carry seeds nothing (docker parity). ctx must
// already carry the backend's content namespace.
func SeedVolumes(ctx context.Context, sn snapshots.Snapshotter, chainID string, vols []VolumeBacking, errPrefix string) error {
	var pending []VolumeBacking
	for _, v := range vols {
		entries, err := os.ReadDir(v.HostDir)
		if err != nil {
			return fmt.Errorf("%s: read volume dir: %w", errPrefix, err)
		}
		if len(entries) == 0 {
			pending = append(pending, v)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	key := fmt.Sprintf("cornus-volume-seed-%d-%d", os.Getpid(), time.Now().UnixNano())
	mounts, err := sn.View(ctx, key, chainID)
	if err != nil {
		return fmt.Errorf("%s: view image snapshot: %w", errPrefix, err)
	}
	defer func() { _ = sn.Remove(ctx, key) }()
	return mount.WithTempMount(ctx, mounts, func(root string) error {
		for _, v := range pending {
			src, err := fs.RootPath(root, v.Target)
			if err != nil {
				continue
			}
			st, err := os.Stat(src)
			if err != nil || !st.IsDir() {
				continue
			}
			if err := fs.CopyDir(v.HostDir, src); err != nil {
				return fmt.Errorf("%s: seed volume %s from image: %w", errPrefix, v.Target, err)
			}
		}
		return nil
	})
}
