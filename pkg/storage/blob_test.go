package storage

import (
	"context"
	"errors"
	"io"
	"testing"

	"gocloud.dev/blob/memblob"
)

// failingReader yields n bytes and then fails, simulating a mid-copy source
// error (e.g. a disk read fault while streaming a staged blob into the store).
type failingReader struct {
	remaining int
	err       error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, r.err
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.remaining -= n
	return n, nil
}

// TestBlobObjectStorePutAbortsOnCopyError verifies that a mid-copy error does
// not commit a truncated object under the (content-addressed) key. gocloud
// commits on Close(); Put must cancel the write context so the partial bytes are
// discarded rather than finalized.
func TestBlobObjectStorePutAbortsOnCopyError(t *testing.T) {
	ctx := context.Background()
	store := newBlobObjectStore(memblob.OpenBucket(nil))
	defer store.Close()

	wantErr := errors.New("boom: source read failed")
	key := "blobs/sha256/deadbeef"

	err := store.Put(ctx, key, &failingReader{remaining: 8, err: wantErr}, -1)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Put error = %v, want %v", err, wantErr)
	}

	// The truncated object must not have been committed.
	if _, err := store.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat after aborted Put = %v, want ErrNotFound (partial object was committed)", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after aborted Put = %v, want ErrNotFound (partial object was committed)", err)
	}
}

// TestBlobObjectStorePutSucceeds is a companion sanity check: a clean copy
// commits the object and leaves the write context uncancelled.
func TestBlobObjectStorePutSucceeds(t *testing.T) {
	ctx := context.Background()
	store := newBlobObjectStore(memblob.OpenBucket(nil))
	defer store.Close()

	key := "blobs/sha256/cafebabe"
	if err := store.Put(ctx, key, &failingReader{remaining: 5, err: io.EOF}, -1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sz, err := store.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if sz != 5 {
		t.Fatalf("Stat size = %d, want 5", sz)
	}
}
