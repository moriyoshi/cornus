package lazyctx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// RefAnnotation is the OCI index annotation carrying the image reference name.
const RefAnnotation = "org.opencontainers.image.ref.name"

// Image is a synthetic single-layer OCI image standing in for a lazy bind
// context. Its one layer's digest is the metadata-manifest digest (the
// content-addressed cache key), and the layer *blob is never materialized* — the
// cornus remote snapshotter mounts a 9p view for that layer instead of
// unpacking a tar. The config and manifest blobs are real and small; only they
// are served over the session. Consumed as `context:<name>=oci-layout://<ref>`.
type Image struct {
	// Manifest is the descriptor (digest+size) of the manifest blob, i.e. the
	// entry the oci-layout ref resolves to.
	Manifest ocispec.Descriptor
	// Layer is the single layer's descriptor (digest + size + lazy annotation),
	// used to pre-seed the ref via GetByBlob.
	Layer ocispec.Descriptor
	// LayerDigest == the metadata-manifest digest; the id the remote snapshotter
	// keys on.
	LayerDigest digest.Digest

	blobs map[digest.Digest][]byte // config + manifest only (NOT the layer)
	index []byte
}

// SyntheticImage builds the image for a lazy layer of the given digest/size.
// layerAnnotations are attached to the layer descriptor (e.g. the 9p session +
// subtree reference the snapshotter resolves). refName is the oci-layout ref.
func SyntheticImage(layerDigest digest.Digest, layerSize int64, refName string, layerAnnotations map[string]string) (*Image, error) {
	if err := layerDigest.Validate(); err != nil {
		return nil, fmt.Errorf("lazyctx: layer digest: %w", err)
	}

	// Uncompressed layer: diffID == digest, so RootFS.DiffIDs references it.
	cfg := ocispec.Image{
		Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"},
		RootFS:   ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{layerDigest}},
		Config:   ocispec.ImageConfig{},
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	cfgDesc := bytesDescriptor(ocispec.MediaTypeImageConfig, cfgBytes)

	// The layer is uncompressed (MediaTypeImageLayer), so its diffID equals its
	// digest. Declare it via the containerd.io/uncompressed annotation so
	// cache.GetByBlob can resolve the diffID directly (the puller otherwise reads
	// it from the config's rootfs.diff_ids).
	annotations := map[string]string{"containerd.io/uncompressed": layerDigest.String()}
	for k, v := range layerAnnotations {
		annotations[k] = v
	}
	layerDesc := ocispec.Descriptor{
		MediaType:   ocispec.MediaTypeImageLayer,
		Digest:      layerDigest,
		Size:        layerSize,
		Annotations: annotations,
	}
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    cfgDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	}
	manBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manDesc := bytesDescriptor(ocispec.MediaTypeImageManifest, manBytes)

	idxManDesc := manDesc
	if refName != "" {
		idxManDesc.Annotations = map[string]string{RefAnnotation: refName}
	}
	index := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{idxManDesc},
	}
	idxBytes, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}

	return &Image{
		Manifest:    manDesc,
		Layer:       layerDesc,
		LayerDigest: layerDigest,
		blobs: map[digest.Digest][]byte{
			cfgDesc.Digest: cfgBytes,
			manDesc.Digest: manBytes,
		},
		index: idxBytes,
	}, nil
}

// Blobs returns the materialized blobs (config + manifest) by digest. The layer
// blob is deliberately absent — it is served lazily by the snapshotter.
func (im *Image) Blobs() map[digest.Digest][]byte { return im.blobs }

// Index returns the index.json bytes.
func (im *Image) Index() []byte { return im.index }

// WriteLayout writes an OCI image layout (oci-layout, index.json, blobs/sha256/*)
// under dir. The layer blob is intentionally not written; a lazy consumer (our
// remote snapshotter) never reads it.
func (im *Image) WriteLayout(dir string) error {
	blobDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return err
	}
	layout, err := json.Marshal(ocispec.ImageLayout{Version: ocispec.ImageLayoutVersion})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ocispec.ImageLayoutFile), layout, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), im.index, 0o644); err != nil {
		return err
	}
	for dgst, b := range im.blobs {
		if err := os.WriteFile(filepath.Join(blobDir, dgst.Encoded()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func bytesDescriptor(mediaType string, b []byte) ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(b),
		Size:      int64(len(b)),
	}
}
