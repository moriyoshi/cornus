// Package storage provides cornus's pluggable registry persistence. A minimal
// ObjectStore interface (get / put / stat / delete / list by key) is implemented
// per backend (filesystem, gocloud blob: memory, S3, ...). All registry
// semantics — the sha256 content-addressable store, digest verification,
// resumable upload staging, and manifest / tag / repo indexing — live once in
// the Backend layer (cas.go) on top of an ObjectStore.
package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound is returned when a blob, manifest, tag, object, or upload session
// does not exist.
var ErrNotFound = errors.New("not found")

// ErrDigestMismatch is returned when uploaded content does not match the digest
// the client claimed.
var ErrDigestMismatch = errors.New("digest mismatch")

// ObjectStore is the minimal per-backend interface. Keys are forward-slash
// separated paths (no leading slash). Implementations must return ErrNotFound
// for missing keys from Get and Stat.
type ObjectStore interface {
	// Get opens an object for reading.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Put writes an object, overwriting any existing one. size is the content
	// length, or -1 if unknown.
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Stat returns the size of an object.
	Stat(ctx context.Context, key string) (size int64, err error)
	// Delete removes an object. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns the keys under prefix. When delimiter is "/", keys are
	// rolled up at the next "/" boundary and the returned entries include
	// "directory" prefixes (each ending in "/"); when delimiter is "" every
	// matching key is returned. prefix may be "".
	List(ctx context.Context, prefix, delimiter string) (keys []string, err error)
	// Close releases backend resources.
	Close() error
}

// NativeUploader is an optional capability. A backend that can resume uploads
// natively (e.g. the filesystem backend appending to a session file) implements
// it; the Backend layer then uses it instead of local staging.
type NativeUploader interface {
	NewUpload(ctx context.Context) (Upload, error)
	GetUpload(ctx context.Context, id string) (Upload, error)
	AbortUpload(ctx context.Context, id string) error
}

// Upload is an in-progress resumable blob upload.
type Upload interface {
	// ID is the opaque session identifier echoed to the client.
	ID() string
	// Write appends a chunk and returns the new total size.
	Write(ctx context.Context, r io.Reader) (total int64, err error)
	// Reader returns a fresh reader over all bytes written so far. The caller
	// closes it.
	Reader(ctx context.Context) (io.ReadCloser, error)
	// Close releases the session's handles without committing it.
	Close() error
}

// --- digest helpers ---------------------------------------------------------

// ParseDigest validates a "sha256:<hex>" digest and returns its algorithm and
// hex portion.
func ParseDigest(d string) (algo, hexv string, err error) {
	i := strings.IndexByte(d, ':')
	if i < 0 {
		return "", "", fmt.Errorf("invalid digest %q: missing algorithm", d)
	}
	algo, hexv = d[:i], d[i+1:]
	if algo != "sha256" {
		return "", "", fmt.Errorf("unsupported digest algorithm %q", algo)
	}
	if len(hexv) != 64 {
		return "", "", fmt.Errorf("invalid sha256 digest length %d", len(hexv))
	}
	if _, err := hex.DecodeString(hexv); err != nil {
		return "", "", fmt.Errorf("invalid digest hex: %w", err)
	}
	return algo, hexv, nil
}

func isDigest(ref string) bool {
	_, _, err := ParseDigest(ref)
	return err == nil
}
