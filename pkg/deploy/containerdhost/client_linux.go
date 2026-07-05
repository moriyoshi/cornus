//go:build linux

package containerdhost

import (
	"context"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/snapshots"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

// clientAPI is the slice of the containerd client surface the backend uses.
// CreateContainer takes structured arguments rather than ctd.NewContainerOpts
// because the stock opts dereference a concrete *ctd.Client, which an
// in-memory fake cannot provide; realClient maps it back onto the stock opts.
// Tests inject a fake (the kubernetes backend's NewWithClient pattern).
type clientAPI interface {
	Containers(ctx context.Context, filters ...string) ([]ctd.Container, error)
	LoadContainer(ctx context.Context, id string) (ctd.Container, error)
	CreateContainer(ctx context.Context, id string, img ctd.Image, labels map[string]string, specOpts []oci.SpecOpts) (ctd.Container, error)
	Pull(ctx context.Context, ref string, opts ...ctd.RemoteOpt) (ctd.Image, error)
	GetImage(ctx context.Context, ref string) (ctd.Image, error)
	SnapshotService(snapshotterName string) snapshots.Snapshotter
	Close() error
}

// realClient adapts *ctd.Client to clientAPI. snapshotter (Config.Snapshotter)
// overrides containerd's default rootfs snapshotter when non-empty.
type realClient struct {
	*ctd.Client
	snapshotter string
}

func (r realClient) CreateContainer(ctx context.Context, id string, img ctd.Image, labels map[string]string, specOpts []oci.SpecOpts) (ctd.Container, error) {
	opts := make([]ctd.NewContainerOpts, 0, 5)
	if r.snapshotter != "" {
		// Must precede WithNewSnapshot: it resolves the snapshotter from the
		// container record being built.
		opts = append(opts, ctd.WithSnapshotter(r.snapshotter))
	}
	opts = append(opts,
		ctd.WithImage(img),
		ctd.WithNewSnapshot(id, img),
		ctd.WithNewSpec(specOpts...),
		ctd.WithContainerLabels(labels),
	)
	return r.Client.NewContainer(ctx, id, opts...)
}

// networkManager is the seam over CNI networking (cniManager in production;
// faked in unit tests, which cannot create network namespaces unprivileged).
type networkManager interface {
	EnsureNetworks(names []string) error
	Setup(ctx context.Context, id string, networks []string, ports []api.PortMapping) (hostrun.Attachment, error)
	teardownInstance(ctx context.Context, id string, labels map[string]string)
	RemoveNetwork(name string) error
}

// ns stamps the backend's containerd namespace onto ctx. Every containerd call
// must go through it.
func (b *Backend) ns(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, b.namespace)
}
