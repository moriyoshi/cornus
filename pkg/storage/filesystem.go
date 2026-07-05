package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fsObjectStore is an ObjectStore over a local directory. It is the default,
// zero-dependency backend and additionally implements NativeUploader so
// resumable uploads append to a session file and commit by renaming it directly
// into the blob path (no extra copy).
type fsObjectStore struct {
	root     string
	uploads  string // session files for native uploads
	stageDir string // staging dir, shared with the Backend (for temp writes)
}

// NewFilesystem returns a filesystem ObjectStore rooted at dir. uploadsDir holds
// in-progress native upload sessions.
func NewFilesystem(dir, uploadsDir string) (ObjectStore, error) {
	for _, d := range []string{dir, uploadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return &fsObjectStore{root: dir, uploads: uploadsDir, stageDir: uploadsDir}, nil
}

func (s *fsObjectStore) keyPath(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

func (s *fsObjectStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(s.keyPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *fsObjectStore) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	dst := s.keyPath(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.stageDir, "put-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dst)
}

func (s *fsObjectStore) Stat(_ context.Context, key string) (int64, error) {
	fi, err := os.Stat(s.keyPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return fi.Size(), nil
}

func (s *fsObjectStore) Delete(_ context.Context, key string) error {
	err := os.Remove(s.keyPath(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns the keys under prefix. With delimiter "/" it lists one directory
// level, returning regular keys and "directory" prefixes (ending in "/").
func (s *fsObjectStore) List(_ context.Context, prefix, delimiter string) ([]string, error) {
	if delimiter == "/" {
		dir := s.keyPath(strings.TrimSuffix(prefix, "/"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return []string{}, nil
			}
			return nil, err
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			k := joinKey(prefix, e.Name())
			if e.IsDir() {
				k += "/"
			}
			out = append(out, k)
		}
		sort.Strings(out)
		return out, nil
	}

	// Recursive listing of all keys under prefix.
	base := s.keyPath(prefix)
	var out []string
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(s.root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (s *fsObjectStore) Close() error { return nil }

// joinKey joins a key prefix and a name with a single forward slash.
func joinKey(prefix, name string) string {
	return strings.TrimSuffix(prefix, "/") + "/" + name
}

// --- NativeUploader ---------------------------------------------------------

func (s *fsObjectStore) sessionPath(id string) string {
	return filepath.Join(s.uploads, "session-"+sanitize(id))
}

func (s *fsObjectStore) NewUpload(_ context.Context) (Upload, error) {
	id := newUploadID()
	p := s.sessionPath(id)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &fsUpload{store: s, id: id, path: p, f: f}, nil
}

func (s *fsObjectStore) GetUpload(_ context.Context, id string) (Upload, error) {
	p := s.sessionPath(id)
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
	return &fsUpload{store: s, id: id, path: p, f: f}, nil
}

func (s *fsObjectStore) AbortUpload(_ context.Context, id string) error {
	err := os.Remove(s.sessionPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// fsUpload is a native filesystem upload that commits by renaming its session
// file directly into the blob path.
type fsUpload struct {
	store *fsObjectStore
	id    string
	path  string
	f     *os.File
}

func (u *fsUpload) ID() string { return u.id }

func (u *fsUpload) Write(_ context.Context, r io.Reader) (int64, error) {
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

func (u *fsUpload) Reader(_ context.Context) (io.ReadCloser, error) {
	return os.Open(u.path)
}

func (u *fsUpload) Close() error { return u.f.Close() }

// Commit hashes the session file in place, verifies it against expect, and
// renames it into the CAS blob path — the native fast path (no copy).
func (u *fsUpload) Commit(_ context.Context, expect string) (digest string, size int64, err error) {
	if err = u.f.Close(); err != nil {
		return "", 0, err
	}
	f, err := os.Open(u.path)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	size, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", 0, err
	}
	digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	if expect != "" && expect != digest {
		return "", 0, ErrDigestMismatch
	}

	key, err := blobKey(digest)
	if err != nil {
		return "", 0, err
	}
	dst := u.store.keyPath(key)
	// If the blob already exists, drop the session file.
	if _, statErr := os.Stat(dst); statErr == nil {
		os.Remove(u.path)
		return digest, size, nil
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	if err = os.Rename(u.path, dst); err != nil {
		return "", 0, err
	}
	return digest, size, nil
}
