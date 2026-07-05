//go:build linux

package containerdhost

import (
	"context"

	ctd "github.com/containerd/containerd"
	"github.com/opencontainers/image-spec/identity"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

// Volume backings: the shared machinery lives in cornus/pkg/deploy/internal/
// hostrun (VolumeStore + SeedVolumes). These thin wrappers bind it to the
// containerd backend — b.vols for the dir/mount/reap logic, and a seedVolumes
// that resolves the chain ID via the image's RootFS and the client's snapshot
// service (bare resolves those from its in-process snapshotter + the pull's
// chain ID instead).

func (b *Backend) instanceMounts(spec api.DeploySpec, replica int) ([]specs.Mount, []hostrun.VolumeBacking, error) {
	return b.vols.InstanceMounts(spec, replica)
}

func (b *Backend) namedVolumeDir(name string) string { return b.vols.NamedVolumeDir(name) }
func (b *Backend) anonVolumesDir(app string) string  { return b.vols.AnonVolumesDir(app) }

func (b *Backend) reapAnonymousVolumes(name string) { b.vols.ReapAnonymousVolumes(name) }

// RemoveVolume implements deploy.VolumeRemover (for `compose down --volumes`).
func (b *Backend) RemoveVolume(_ context.Context, name string) error {
	return b.vols.RemoveVolume(name)
}

// seedVolumes copies the image's baked content into each fresh volume. nctx must
// be namespace-stamped; the chain ID comes from the image's RootFS and the
// snapshotter must match the one the image was unpacked into (pullImage).
func (b *Backend) seedVolumes(nctx context.Context, img ctd.Image, vols []hostrun.VolumeBacking) error {
	if len(vols) == 0 {
		return nil // no volumes: avoid the image RootFS + snapshotter round-trip
	}
	diffIDs, err := img.RootFS(nctx)
	if err != nil {
		return err
	}
	name := b.snapshotter
	if name == "" {
		name = ctd.DefaultSnapshotter
	}
	return hostrun.SeedVolumes(nctx, b.client.SnapshotService(name), identity.ChainID(diffIDs).String(), vols, "containerd")
}
