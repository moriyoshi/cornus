package lazyctx

import (
	"bytes"
	"context"
	"fmt"

	"github.com/containerd/containerd/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContentStore returns a read-only containerd content.Store serving the image's
// materialized blobs (config + manifest). It is registered on the build client
// session via client.SolveOpt.OCIStores[storeID] (keyed "oci:"+storeID by
// BuildKit), so `context:<name>=oci-layout://<ref>` with llb.OCIStore(sessionID,
// storeID) resolves it in-process — no registry round-trip. The lazy layer blob
// is deliberately absent: a read for it returns NotFound, which never happens on
// the remote-snapshot path (the layer is mounted from 9p, not fetched).
func (im *Image) ContentStore() content.Store {
	return &roContentStore{blobs: im.blobs}
}

type roContentStore struct {
	blobs map[digest.Digest][]byte
}

var _ content.Store = (*roContentStore)(nil)

func (s *roContentStore) Info(_ context.Context, dgst digest.Digest) (content.Info, error) {
	b, ok := s.blobs[dgst]
	if !ok {
		return content.Info{}, fmt.Errorf("content %s: %w", dgst, errdefs.ErrNotFound)
	}
	return content.Info{Digest: dgst, Size: int64(len(b))}, nil
}

func (s *roContentStore) ReaderAt(_ context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	b, ok := s.blobs[desc.Digest]
	if !ok {
		return nil, fmt.Errorf("content %s: %w", desc.Digest, errdefs.ErrNotFound)
	}
	return &bytesReaderAt{b: b}, nil
}

func (s *roContentStore) Walk(_ context.Context, fn content.WalkFunc, _ ...string) error {
	for dgst, b := range s.blobs {
		if err := fn(content.Info{Digest: dgst, Size: int64(len(b))}); err != nil {
			return err
		}
	}
	return nil
}

// Read-only: mutation and ingestion are unsupported.
func (s *roContentStore) Update(context.Context, content.Info, ...string) (content.Info, error) {
	return content.Info{}, errdefs.ErrNotImplemented
}
func (s *roContentStore) Delete(context.Context, digest.Digest) error {
	return errdefs.ErrNotImplemented
}
func (s *roContentStore) Status(context.Context, string) (content.Status, error) {
	return content.Status{}, errdefs.ErrNotFound
}
func (s *roContentStore) ListStatuses(context.Context, ...string) ([]content.Status, error) {
	return nil, nil
}
func (s *roContentStore) Abort(context.Context, string) error {
	return errdefs.ErrNotImplemented
}
func (s *roContentStore) Writer(context.Context, ...content.WriterOpt) (content.Writer, error) {
	return nil, errdefs.ErrNotImplemented
}

type bytesReaderAt struct{ b []byte }

func (r *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(r.b).ReadAt(p, off)
}
func (r *bytesReaderAt) Size() int64  { return int64(len(r.b)) }
func (r *bytesReaderAt) Close() error { return nil }
