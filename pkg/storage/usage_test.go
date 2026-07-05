package storage

import (
	"bytes"
	"context"
	"testing"

	"gocloud.dev/blob/memblob"
)

// TestUsage verifies the CAS usage snapshot: the count and total bytes track the
// blobs actually present, and a deletion is reflected on the next call.
func TestUsage(t *testing.T) {
	ctx := context.Background()
	b, err := NewBackend(newBlobObjectStore(memblob.OpenBucket(nil)), t.TempDir())
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer b.Close()

	// Empty store: zero everything.
	if u, err := b.Usage(ctx); err != nil || u.Blobs != 0 || u.Bytes != 0 {
		t.Fatalf("Usage on empty store = %+v, %v; want {0 0}, nil", u, err)
	}

	blobs := [][]byte{
		bytes.Repeat([]byte("a"), 10),
		bytes.Repeat([]byte("b"), 100),
		bytes.Repeat([]byte("c"), 1000),
	}
	var wantBytes int64
	var digests []string
	for _, data := range blobs {
		d, size, err := b.PutBlob(ctx, bytes.NewReader(data), "")
		if err != nil {
			t.Fatalf("PutBlob: %v", err)
		}
		wantBytes += size
		digests = append(digests, d)
	}

	u, err := b.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.Blobs != int64(len(blobs)) {
		t.Errorf("Usage.Blobs = %d, want %d", u.Blobs, len(blobs))
	}
	if u.Bytes != wantBytes {
		t.Errorf("Usage.Bytes = %d, want %d", u.Bytes, wantBytes)
	}

	// Deleting a blob shrinks the snapshot.
	if err := b.DeleteBlob(ctx, digests[2]); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	u2, err := b.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage after delete: %v", err)
	}
	if u2.Blobs != int64(len(blobs)-1) {
		t.Errorf("Usage.Blobs after delete = %d, want %d", u2.Blobs, len(blobs)-1)
	}
	if u2.Bytes != wantBytes-1000 {
		t.Errorf("Usage.Bytes after delete = %d, want %d", u2.Bytes, wantBytes-1000)
	}
}
