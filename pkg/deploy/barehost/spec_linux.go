//go:build linux

package barehost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/snapshots"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

// ociImage is a minimal oci.Image over our in-process content store: it is all
// oci.WithImageConfig needs to read an image's config (env, entrypoint, cmd,
// user, workingdir) and generate a runtime spec — no containerd daemon, no
// containerd image store. config is the image manifest's config descriptor,
// resolved during the pull (image_linux.go).
type ociImage struct {
	store  content.Store
	config ocispec.Descriptor
}

func (i ociImage) Config(ctx context.Context) (ocispec.Descriptor, error) { return i.config, nil }
func (i ociImage) ContentStore() content.Store                            { return i.store }

var _ oci.Image = ociImage{}

// specClient is a minimal oci.Client: oci.GenerateSpec requires a Client, but
// the spec opts cornus uses only reach for SnapshotService via a few opts we do
// not apply (rootfs is prepared out-of-band, image_linux.go). sn may be nil for
// pure spec generation.
type specClient struct {
	sn snapshots.Snapshotter
}

func (c specClient) SnapshotService(string) snapshots.Snapshotter { return c.sn }

var _ oci.Client = specClient{}

// buildSpec generates the OCI runtime spec (config.json contents) for one
// instance and returns it. The image-independent + image-config spec opts are the
// shared hostrun.SpecOpts (identical to containerdhost); barehost additionally
// feeds them to oci.GenerateSpec in-process (no daemon) via the ociImage/
// specClient wrappers, sets an absolute rootfs, and pins Linux.CgroupsPath — the
// two things the daemonless path owns that the containerd shim used to.
func buildSpec(ctx context.Context, id string, spec api.DeploySpec, img ociImage, rootfsPath, netnsPath, cgroupPath string, mounts []specs.Mount) (*specs.Spec, error) {
	// oci.WithImageConfig reads the image config from the content store, whose
	// containerd-library implementation requires a namespace on the context.
	ctx = namespaces.WithNamespace(ctx, contentNamespace)
	// Root.Path must be the absolute, already-mounted rootfs BEFORE the image
	// config opts run: WithImageConfig resolves the image user's supplementary
	// GIDs by reading <rootfs>/etc/group directly (there is no daemon-managed
	// snapshot to consult, unlike containerd), so the path has to be set first.
	opts := append([]oci.SpecOpts{oci.WithRootFSPath(rootfsPath)}, hostrun.SpecOpts(ctx, "bare", id, spec, img, netnsPath, mounts)...)
	if cgroupPath != "" {
		opts = append(opts, withCgroupsPath(cgroupPath))
	}
	s, err := oci.GenerateSpec(ctx, specClient{}, &containers.Container{ID: id}, opts...)
	if err != nil {
		return nil, fmt.Errorf("bare: generate spec for %s: %w", id, err)
	}
	return s, nil
}

// writeBundleConfig marshals the spec to <bundleDir>/config.json, the file the
// OCI runtime reads. The bundle dir must already exist with a rootfs/ beside it.
func writeBundleConfig(bundleDir string, s *specs.Spec) error {
	data, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		return fmt.Errorf("bare: marshal config.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), data, 0o644); err != nil {
		return fmt.Errorf("bare: write config.json: %w", err)
	}
	return nil
}

// cgroupsPath returns the spec's Linux.CgroupsPath for an instance. systemd form
// (slice:prefix:name) when the runtime drives systemd cgroups, else a cgroupfs
// path. The bare backend owns this because there is no daemon/shim to assign it.
func cgroupsPath(id string, systemd bool) string {
	if systemd {
		return "cornus.slice:cornus:" + id
	}
	return "/cornus/" + id
}

func withCgroupsPath(path string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Linux == nil {
			s.Linux = &specs.Linux{}
		}
		s.Linux.CgroupsPath = path
		return nil
	}
}
