package storage

import (
	"context"
	"io"
	"strings"
	"testing"

	"gocloud.dev/blob/memblob"
)

// recordingObjectStore wraps an ObjectStore and records the key of every Put in
// call order, so a test can assert PutManifest's write ordering
// (blob -> membership marker -> tag).
type recordingObjectStore struct {
	ObjectStore
	puts []string
}

func (r *recordingObjectStore) Put(ctx context.Context, key string, rd io.Reader, size int64) error {
	if err := r.ObjectStore.Put(ctx, key, rd, size); err != nil {
		return err
	}
	r.puts = append(r.puts, key)
	return nil
}

func newRecordingBackend(t *testing.T) (*Backend, *recordingObjectStore) {
	t.Helper()
	rec := &recordingObjectStore{ObjectStore: newBlobObjectStore(memblob.OpenBucket(nil))}
	b, err := NewBackend(rec, t.TempDir())
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	return b, rec
}

// TestPutManifestWriteOrdering asserts the crash-safety invariant: the content
// blob and the per-repo membership marker are written BEFORE the tag that points
// at them, so a crash can never publish a tag ahead of its data (no dangling
// tag).
func TestPutManifestWriteOrdering(t *testing.T) {
	ctx := context.Background()
	b, rec := newRecordingBackend(t)
	defer b.Close()

	manifest := []byte(`{"schemaVersion":2}`)
	mt := "application/vnd.oci.image.manifest.v1+json"
	digest, err := b.PutManifest(ctx, "library/app", "v1", mt, manifest)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	_, hexv, _ := ParseDigest(digest)

	blobK, _ := blobKey(digest)
	markerK := manifestKey("library/app", hexv)
	tagK := tagKey("library/app", "v1")

	idx := func(key string) int {
		for i, k := range rec.puts {
			if k == key {
				return i
			}
		}
		return -1
	}
	iBlob, iMarker, iTag := idx(blobK), idx(markerK), idx(tagK)
	if iBlob < 0 || iMarker < 0 || iTag < 0 {
		t.Fatalf("missing Put: blob=%d marker=%d tag=%d (puts=%v)", iBlob, iMarker, iTag, rec.puts)
	}
	if !(iBlob < iMarker && iMarker < iTag) {
		t.Fatalf("write order violated: blob=%d marker=%d tag=%d, want blob<marker<tag", iBlob, iMarker, iTag)
	}
}

// TestDanglingTagReadsNotFound simulates the still-possible transient state a
// crash / external mutation can leave -- a tag whose target manifest data is
// gone -- and asserts the read is a clean ErrNotFound, never a broken success or
// a 500-class error.
func TestDanglingTagReadsNotFound(t *testing.T) {
	ctx := context.Background()

	t.Run("tag points at missing marker+blob", func(t *testing.T) {
		b, _ := newRecordingBackend(t)
		defer b.Close()
		// Write a tag entry directly, pointing at a digest that was never stored.
		absent := sha256Of("never stored")
		if err := b.obj.Put(ctx, tagKey("library/app", "ghost"),
			strings.NewReader(absent), int64(len(absent))); err != nil {
			t.Fatalf("seed dangling tag: %v", err)
		}
		if _, _, _, err := b.GetManifest(ctx, "library/app", "ghost"); err != ErrNotFound {
			t.Fatalf("GetManifest(dangling) = %v, want ErrNotFound", err)
		}
	})

	t.Run("marker deleted out from under a live tag", func(t *testing.T) {
		b, _ := newRecordingBackend(t)
		defer b.Close()
		digest := putManifestJSON(t, b, "library/app", "v1", map[string]any{"schemaVersion": 2})
		// Drop the membership marker but leave the tag (and blob) in place.
		_, hexv, _ := ParseDigest(digest)
		if err := b.obj.Delete(ctx, manifestKey("library/app", hexv)); err != nil {
			t.Fatalf("delete marker: %v", err)
		}
		if _, _, _, err := b.GetManifest(ctx, "library/app", "v1"); err != ErrNotFound {
			t.Fatalf("GetManifest(marker gone) = %v, want ErrNotFound", err)
		}
	})

	t.Run("content blob deleted, marker+tag intact", func(t *testing.T) {
		b, _ := newRecordingBackend(t)
		defer b.Close()
		digest := putManifestJSON(t, b, "library/app", "v1", map[string]any{"schemaVersion": 2})
		if err := b.DeleteBlob(ctx, digest); err != nil {
			t.Fatalf("delete blob: %v", err)
		}
		if _, _, _, err := b.GetManifest(ctx, "library/app", "v1"); err != ErrNotFound {
			t.Fatalf("GetManifest(blob gone) = %v, want ErrNotFound", err)
		}
	})

	t.Run("corrupt tag value", func(t *testing.T) {
		b, _ := newRecordingBackend(t)
		defer b.Close()
		garbage := "not-a-digest"
		if err := b.obj.Put(ctx, tagKey("library/app", "junk"),
			strings.NewReader(garbage), int64(len(garbage))); err != nil {
			t.Fatalf("seed corrupt tag: %v", err)
		}
		if _, _, _, err := b.GetManifest(ctx, "library/app", "junk"); err != ErrNotFound {
			t.Fatalf("GetManifest(corrupt tag) = %v, want ErrNotFound", err)
		}
	})
}

// TestPutGetManifestRoundTrip is a focused positive control: a normal tag
// round-trip still works after the ordering / tolerance changes.
func TestPutGetManifestRoundTrip(t *testing.T) {
	ctx := context.Background()
	b, _ := newRecordingBackend(t)
	defer b.Close()

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	mt := "application/vnd.oci.image.manifest.v1+json"
	digest, err := b.PutManifest(ctx, "library/app", "v1", mt, manifest)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	content, d, gotMT, err := b.GetManifest(ctx, "library/app", "v1")
	if err != nil || string(content) != string(manifest) || d != digest || gotMT != mt {
		t.Fatalf("GetManifest = %q, %s, %q, %v", content, d, gotMT, err)
	}
}
