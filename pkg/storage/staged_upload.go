package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// stagedUpload is the default resumable upload used by backends that do not
// implement NativeUploader. Chunks are appended to a local file in the staging
// directory; on commit the Backend reads the file back and streams it into the
// ObjectStore via PutBlob.
type stagedUpload struct {
	id   string
	path string
	f    *os.File // append handle for the current request
}

func stagedUploadPath(stagingDir, id string) string {
	return filepath.Join(stagingDir, "upload-"+sanitize(id))
}

func newStagedUpload(stagingDir, id string) (Upload, error) {
	p := stagedUploadPath(stagingDir, id)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &stagedUpload{id: id, path: p, f: f}, nil
}

func openStagedUpload(stagingDir, id string) (Upload, error) {
	p := stagedUploadPath(stagingDir, id)
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &stagedUpload{id: id, path: p, f: f}, nil
}

func abortStagedUpload(stagingDir, id string) error {
	err := os.Remove(stagedUploadPath(stagingDir, id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (u *stagedUpload) ID() string { return u.id }

func (u *stagedUpload) Write(_ context.Context, r io.Reader) (int64, error) {
	if _, err := io.Copy(u.f, r); err != nil {
		return 0, err
	}
	if err := u.f.Sync(); err != nil {
		return 0, err
	}
	fi, err := os.Stat(u.path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (u *stagedUpload) Reader(_ context.Context) (io.ReadCloser, error) {
	return os.Open(u.path)
}

func (u *stagedUpload) Close() error { return u.f.Close() }
