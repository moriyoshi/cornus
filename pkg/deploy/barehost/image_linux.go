//go:build linux

package barehost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/rootfs"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/containerd/containerd/snapshots/overlay"
	"github.com/containerd/containerd/snapshots/overlay/overlayutils"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// imageStore is the backend's daemonless image machinery: an on-disk content
// CAS, a snapshotter (overlayfs, or native on hosts that reject overlay), and a
// layer applier — exactly what containerd's daemon provided, assembled here from
// its libraries in-process. It pulls an image ref into the CAS, unpacks its
// layers into a committed snapshot chain, and prepares a per-instance writable
// rootfs mount for the OCI runtime.
type imageStore struct {
	content  content.Store
	sn       snapshots.Snapshotter
	snName   string // "overlayfs" or "native"
	applier  diff.Applier
	insecure map[string]bool
}

// newImageStore builds the content store and snapshotter under <dataDir>/bare/.
// snapPref is Config.Snapshotter: "overlayfs"/"native" force the choice, ""
// auto-selects overlayfs when the kernel supports it (lifted from the build
// engine's snapshotterFactory), falling back to native — the docker-in-docker
// overlay-on-overlay escape hatch.
func newImageStore(dataDir, snapPref string) (*imageStore, error) {
	base := filepath.Join(dataDir, "bare")
	cs, err := local.NewStore(filepath.Join(base, "content"))
	if err != nil {
		return nil, fmt.Errorf("bare: content store: %w", err)
	}
	name, sn, err := newSnapshotter(filepath.Join(base, "snapshots"), snapPref)
	if err != nil {
		return nil, fmt.Errorf("bare: snapshotter: %w", err)
	}
	return &imageStore{
		content:  cs,
		sn:       sn,
		snName:   name,
		applier:  apply.NewFileSystemApplier(cs),
		insecure: parseInsecureRegistries(os.Getenv("CORNUS_BARE_INSECURE_REGISTRIES")),
	}, nil
}

// newSnapshotter selects overlayfs (with a native fallback) under root, honoring
// an explicit preference. It mirrors pkg/build/builder's snapshotterFactory,
// including the overlayutils kernel-support probe.
func newSnapshotter(root, pref string) (string, snapshots.Snapshotter, error) {
	overlayRoot := filepath.Join(root, "overlayfs")
	nativeRoot := filepath.Join(root, "native")
	useOverlay := false
	switch pref {
	case "overlayfs":
		useOverlay = true
	case "native":
		useOverlay = false
	case "":
		if err := os.MkdirAll(overlayRoot, 0o711); err != nil {
			return "", nil, err
		}
		useOverlay = overlayutils.Supported(overlayRoot) == nil
	default:
		return "", nil, fmt.Errorf("unknown snapshotter %q (want overlayfs or native)", pref)
	}
	if useOverlay {
		sn, err := overlay.NewSnapshotter(overlayRoot, overlay.AsynchronousRemove)
		return "overlayfs", sn, err
	}
	sn, err := native.NewSnapshotter(nativeRoot)
	return "native", sn, err
}

// resolver builds the registry resolver, lifted from containerdhost: localhost
// registries (the co-located cornus registry) are plain-HTTP automatically,
// CORNUS_BARE_INSECURE_REGISTRIES extends that, and public registries get the
// anonymous bearer-token flow.
func (s *imageStore) resolver() remotes.Resolver {
	matcher := func(host string) (bool, error) {
		if ok, _ := docker.MatchLocalhost(host); ok {
			return true, nil
		}
		return s.insecure[host], nil
	}
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(docker.NewDockerAuthorizer()),
			docker.WithPlainHTTP(matcher),
		),
	})
}

// parseInsecureRegistries parses a comma-separated host[:port] list.
func parseInsecureRegistries(v string) map[string]bool {
	out := map[string]bool{}
	for _, h := range strings.Split(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out[h] = true
		}
	}
	return out
}

// normalizeRef expands a docker-style short image name into the fully qualified
// reference the resolver requires (nginx -> docker.io/library/nginx:latest).
func normalizeRef(ref string) (string, error) {
	named, err := reference.ParseDockerRef(ref)
	if err != nil {
		return "", fmt.Errorf("bare: invalid image reference %q: %w", ref, err)
	}
	return named.String(), nil
}

// pulledImage is the result of a pull: the oci.Image wrapper the spec generator
// consumes plus the committed snapshot chain ID a rootfs is prepared from.
type pulledImage struct {
	img     ociImage
	chainID digest.Digest
}

// pull fetches ref's host-platform manifest, config, and layers into the content
// store and unpacks the layers into a committed snapshot chain, returning the
// image config wrapper and the chain ID. No containerd daemon is involved.
func (s *imageStore) pull(ctx context.Context, ref string) (pulledImage, error) {
	ctx = namespaces.WithNamespace(ctx, contentNamespace)
	full, err := normalizeRef(ref)
	if err != nil {
		return pulledImage{}, err
	}
	res := s.resolver()
	name, rootDesc, err := res.Resolve(ctx, full)
	if err != nil {
		return pulledImage{}, fmt.Errorf("bare: resolve %s: %w", ref, err)
	}
	fetcher, err := res.Fetcher(ctx, name)
	if err != nil {
		return pulledImage{}, fmt.Errorf("bare: fetcher %s: %w", ref, err)
	}
	platform := platforms.Default()
	// Fetch the root descriptor and, for an index, only the host-platform child
	// chain (manifest -> config + layers) into the content store.
	h := images.Handlers(
		remotes.FetchHandler(s.content, fetcher),
		images.FilterPlatforms(images.ChildrenHandler(s.content), platform),
	)
	if err := images.Dispatch(ctx, h, nil, rootDesc); err != nil {
		return pulledImage{}, fmt.Errorf("bare: fetch %s: %w", ref, err)
	}
	manifest, err := images.Manifest(ctx, s.content, rootDesc, platform)
	if err != nil {
		return pulledImage{}, fmt.Errorf("bare: resolve manifest %s: %w", ref, err)
	}
	diffIDs, err := s.diffIDs(ctx, manifest.Config)
	if err != nil {
		return pulledImage{}, err
	}
	if len(diffIDs) != len(manifest.Layers) {
		return pulledImage{}, fmt.Errorf("bare: image %s: %d diff ids for %d layers", ref, len(diffIDs), len(manifest.Layers))
	}
	layers := make([]rootfs.Layer, len(manifest.Layers))
	for i, l := range manifest.Layers {
		layers[i] = rootfs.Layer{
			Diff: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayer, Digest: diffIDs[i]},
			Blob: l,
		}
	}
	chainID, err := rootfs.ApplyLayers(ctx, layers, s.sn, s.applier)
	if err != nil {
		return pulledImage{}, fmt.Errorf("bare: unpack %s: %w", ref, err)
	}
	return pulledImage{img: ociImage{store: s.content, config: manifest.Config}, chainID: chainID}, nil
}

// diffIDs reads an image config blob and returns its rootfs diff IDs (the
// uncompressed layer digests the snapshotter keys on).
func (s *imageStore) diffIDs(ctx context.Context, configDesc ocispec.Descriptor) ([]digest.Digest, error) {
	p, err := content.ReadBlob(ctx, s.content, configDesc)
	if err != nil {
		return nil, fmt.Errorf("bare: read image config: %w", err)
	}
	var img ocispec.Image
	if err := json.Unmarshal(p, &img); err != nil {
		return nil, fmt.Errorf("bare: parse image config: %w", err)
	}
	return img.RootFS.DiffIDs, nil
}

// prepareRootfs prepares a writable snapshot for one instance off the image's
// committed chain and mounts it at rootfsDir. key is the per-instance snapshot
// key (persisted so removeRootfs can release it). It is a no-op-safe rebuild:
// an already-prepared key is reused (host-reboot recovery re-mounts it).
func (s *imageStore) prepareRootfs(ctx context.Context, key string, chainID digest.Digest, rootfsDir string) error {
	ctx = namespaces.WithNamespace(ctx, contentNamespace)
	if err := os.MkdirAll(rootfsDir, 0o711); err != nil {
		return fmt.Errorf("bare: rootfs dir: %w", err)
	}
	mounts, err := s.sn.Prepare(ctx, key, chainID.String())
	if err != nil {
		// A pre-existing active snapshot (e.g. a reboot rebuild) is reusable: get
		// its mounts and continue.
		mounts, err = s.sn.Mounts(ctx, key)
		if err != nil {
			return fmt.Errorf("bare: prepare rootfs snapshot: %w", err)
		}
	}
	if err := mount.All(mounts, rootfsDir); err != nil {
		return fmt.Errorf("bare: mount rootfs: %w", err)
	}
	return nil
}

// removeRootfs unmounts an instance's rootfs and removes its snapshot.
func (s *imageStore) removeRootfs(ctx context.Context, key, rootfsDir string) error {
	ctx = namespaces.WithNamespace(ctx, contentNamespace)
	if err := mount.UnmountAll(rootfsDir, 0); err != nil {
		return fmt.Errorf("bare: unmount rootfs: %w", err)
	}
	if err := s.sn.Remove(ctx, key); err != nil {
		return fmt.Errorf("bare: remove snapshot: %w", err)
	}
	return nil
}
