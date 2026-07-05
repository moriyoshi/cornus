package lazyctx

import (
	"github.com/containerd/containerd/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// LazyContext bundles everything the build engine needs to wire a directory as a
// lazy named build context (RUN --mount=type=bind,from=<Name>): the synthetic OCI
// image served over the session, the oci-layout reference the named context
// points at, and the content store to register on client.SolveOpt.OCIStores. The
// image's single layer is never materialized as a blob — the remote snapshotter
// mounts Backing for it instead of unpacking a tar.
type LazyContext struct {
	Name        string             // the named context
	StoreID     string             // key for SolveOpt.OCIStores and llb.OCIStore(sessionID, StoreID)
	Ref         string             // oci-layout reference: <StoreID>@<manifest-digest>
	ContextAttr string             // FrontendAttrs["context:<Name>"] = "oci-layout://<Ref>"
	Store       content.Store      // serves the image's config + manifest blobs
	LayerDigest digest.Digest      // == the metadata-manifest digest (the RUN cache key)
	LayerDesc   ocispec.Descriptor // the layer descriptor, for pre-seeding the ref via GetByBlob
	Backing     string             // value carried in the layer's LazyLabel (a host dir today; a 9p ref later)
	Dir         string             // the local source directory (for computing producer-side content digests); "" for a remote context
	Ignore      Ignore             // the ignore predicate applied to the manifest (nil if none)
	Digests     []FileDigest       // caller-computed content digests (remote build); nil => compute from Dir
}

// storeID derives a valid image-reference name for a named context.
func storeID(name string) string { return "cornus-lazy-" + name }

// Prepare packages dir as a lazy named context: compute the content-identity
// manifest (cache key / layer digest) via a content-free walk, build the
// synthetic single-layer OCI image whose layer carries LazyLabel=backing, and
// return the wiring bundle. ignore may filter the walk (e.g. a .dockerignore
// matcher). backing is what the remote snapshotter resolves to a mount — a host
// dir in the current skeleton, a 9p session reference once P2 lands.
func Prepare(name, dir, backing string, ignore Ignore) (*LazyContext, error) {
	m, err := Build(dir, ignore)
	if err != nil {
		return nil, err
	}
	lc, err := buildContext(name, m.Digest(), int64(len(m.Bytes())), backing, nil)
	if err != nil {
		return nil, err
	}
	lc.Dir = dir
	lc.Ignore = ignore
	return lc, nil
}

// FromRemote builds a lazy context from a caller-computed layer digest + content
// digests (a remote build: the files live on the caller, served over 9p, so the
// server never walks them). backing is the 9p endpoint the snapshotter mounts.
func FromRemote(name string, layerDigest digest.Digest, layerSize int64, backing string, digests []FileDigest) (*LazyContext, error) {
	return buildContext(name, layerDigest, layerSize, backing, digests)
}

// build constructs the LazyContext (synthetic oci-layout image + descriptors)
// from an already-computed layer digest, shared by the local and remote paths.
func buildContext(name string, layerDigest digest.Digest, layerSize int64, backing string, digests []FileDigest) (*LazyContext, error) {
	sid := storeID(name)
	im, err := SyntheticImage(layerDigest, layerSize, sid, map[string]string{LazyLabel: backing})
	if err != nil {
		return nil, err
	}
	ref := sid + "@" + im.Manifest.Digest.String()
	return &LazyContext{
		Name:        name,
		StoreID:     sid,
		Ref:         ref,
		ContextAttr: "oci-layout://" + ref,
		Store:       im.ContentStore(),
		LayerDigest: layerDigest,
		LayerDesc:   im.Layer,
		Backing:     backing,
		Digests:     digests,
	}, nil
}
