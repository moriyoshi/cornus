package storage

import (
	"context"
	"io"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

// blobObjectStore adapts a gocloud *blob.Bucket to the ObjectStore interface,
// providing the memory, S3, GCS, and Azure backends. It does not implement
// NativeUploader, so the Backend layer stages resumable uploads locally.
type blobObjectStore struct {
	bucket *blob.Bucket
}

func newBlobObjectStore(bucket *blob.Bucket) ObjectStore {
	return &blobObjectStore{bucket: bucket}
}

func (s *blobObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.bucket.NewReader(ctx, key, nil)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

func (s *blobObjectStore) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	// gocloud commits a blob write on Close(); the documented way to abort a
	// partial write is to cancel the context passed to NewWriter. Put is the CAS
	// commit path, so on a mid-copy error we must abort rather than finalize —
	// otherwise a truncated object gets committed under the content-addressed key.
	writeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := s.bucket.NewWriter(writeCtx, key, nil)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		cancel()      // abort the write so Close() discards the partial object
		_ = w.Close() // release writer resources; the cancelled ctx prevents a commit
		return err
	}
	return w.Close()
}

func (s *blobObjectStore) Stat(ctx context.Context, key string) (int64, error) {
	attrs, err := s.bucket.Attributes(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return attrs.Size, nil
}

func (s *blobObjectStore) Delete(ctx context.Context, key string) error {
	err := s.bucket.Delete(ctx, key)
	if err != nil && gcerrors.Code(err) == gcerrors.NotFound {
		return nil
	}
	return err
}

// List returns the keys under prefix. With delimiter "/" gocloud rolls results
// up at the next "/" boundary; "directory" results have IsDir set and a Key that
// ends in "/".
func (s *blobObjectStore) List(ctx context.Context, prefix, delimiter string) ([]string, error) {
	it := s.bucket.List(&blob.ListOptions{Prefix: prefix, Delimiter: delimiter})
	var out []string
	for {
		obj, err := it.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, obj.Key)
	}
	return out, nil
}

func (s *blobObjectStore) Close() error { return s.bucket.Close() }
