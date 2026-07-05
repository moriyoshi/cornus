//go:build linux

package barehost

// Volume backings: the shared machinery lives in cornus/pkg/deploy/internal/
// hostrun (VolumeStore + SeedVolumes). These thin wrappers bind it to the bare
// backend — b.vols for the dir/mount/reap logic, and a seedVolumes that resolves
// the in-process snapshotter + the pull's chain ID (containerd resolves those
// from its client + a RootFS round-trip instead).

import (
	"context"

	"github.com/containerd/containerd/namespaces"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

func (b *Backend) instanceMounts(spec api.DeploySpec, replica int) ([]specs.Mount, []hostrun.VolumeBacking, error) {
	return b.vols.InstanceMounts(spec, replica)
}

func (b *Backend) namedVolumeDir(name string) string { return b.vols.NamedVolumeDir(name) }
func (b *Backend) anonVolumesDir(app string) string  { return b.vols.AnonVolumesDir(app) }

func (b *Backend) reapAnonymousVolumes(name string) { b.vols.ReapAnonymousVolumes(name) }

// RemoveVolume implements deploy.VolumeRemover.
func (b *Backend) RemoveVolume(_ context.Context, name string) error {
	return b.vols.RemoveVolume(name)
}

// seedVolumes copies the image's baked content into each fresh volume, reading
// directly from the bare image store's snapshotter under the content namespace
// with the chain ID the pull already produced.
func (b *Backend) seedVolumes(ctx context.Context, chainID digest.Digest, vols []hostrun.VolumeBacking) error {
	if len(vols) == 0 {
		return nil // no volumes: do not touch the (possibly-absent) image store
	}
	return hostrun.SeedVolumes(namespaces.WithNamespace(ctx, contentNamespace), b.img.sn, chainID.String(), vols, "bare")
}
