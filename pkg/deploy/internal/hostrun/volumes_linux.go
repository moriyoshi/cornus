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

// NamedVolumeDir is the shared, project-scoped backing dir for a named volume.
func (s *VolumeStore) NamedVolumeDir(name string) string {
	return filepath.Join(s.dataDir, s.subdir, "volumes", "named", name)
}

func (s *VolumeStore) AnonVolumesDir(app string) string {
	return filepath.Join(s.dataDir, s.subdir, "volumes", "anon", app)
}

func (s *VolumeStore) anonVolumeDir(app string, replica int, target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(s.AnonVolumesDir(app), fmt.Sprintf("%d-%s", replica, hex.EncodeToString(sum[:])[:8]))
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
		if v.Name != "" {
			dir = s.NamedVolumeDir(v.Name)
		} else {
			dir = s.anonVolumeDir(spec.Name, replica, v.Target)
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
	_ = os.RemoveAll(s.AnonVolumesDir(name))
}

// RemoveVolume removes the host directory backing a named, project-scoped volume
// (deploy.VolumeRemover, for `compose down --volumes`). Delete-if-exists.
func (s *VolumeStore) RemoveVolume(name string) error {
	return os.RemoveAll(s.NamedVolumeDir(name))
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
