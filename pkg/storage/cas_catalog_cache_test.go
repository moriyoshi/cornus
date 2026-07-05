package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"gocloud.dev/blob/memblob"
)

// listCountingObjectStore wraps an ObjectStore and counts List calls so a test
// can prove that a cached Repos result serves without re-walking the repos/
// tree (each walk issues at least one List).
type listCountingObjectStore struct {
	ObjectStore
	lists int
}

func (s *listCountingObjectStore) List(ctx context.Context, prefix, delimiter string) ([]string, error) {
	s.lists++
	return s.ObjectStore.List(ctx, prefix, delimiter)
}

func newCountingBackend(t *testing.T) (*Backend, *listCountingObjectStore) {
	t.Helper()
	store := &listCountingObjectStore{ObjectStore: newBlobObjectStore(memblob.OpenBucket(nil))}
	b, err := NewBackend(store, t.TempDir())
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	return b, store
}

func putManifest(t *testing.T, b *Backend, repo, tag string) {
	t.Helper()
	mt := "application/vnd.oci.image.manifest.v1+json"
	if _, err := b.PutManifest(context.Background(), repo, tag, mt, []byte(`{"schemaVersion":2}`)); err != nil {
		t.Fatalf("PutManifest(%s): %v", repo, err)
	}
}

// TestCatalogCacheServesWithoutRewalk proves a second Repos call within the TTL
// returns the same list without issuing any further List I/O against the store.
func TestCatalogCacheServesWithoutRewalk(t *testing.T) {
	ctx := context.Background()
	b, store := newCountingBackend(t)
	defer b.Close()
	// Pin the clock so the TTL never elapses during the test.
	b.now = func() time.Time { return time.Unix(1000, 0) }

	putManifest(t, b, "library/a", "v1")
	putManifest(t, b, "library/b", "v1")

	first, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos #1: %v", err)
	}
	want := []string{"library/a", "library/b"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("Repos #1 = %v, want %v", first, want)
	}

	listsAfterFirst := store.lists
	if listsAfterFirst == 0 {
		t.Fatalf("expected the first Repos to walk the tree (List calls), got 0")
	}

	second, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos #2: %v", err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("Repos #2 = %v, want %v", second, want)
	}
	if store.lists != listsAfterFirst {
		t.Fatalf("cached Repos re-walked: List calls went %d -> %d", listsAfterFirst, store.lists)
	}
}

// TestCatalogCacheInvalidatedByPush proves a repo pushed after the cache is warm
// becomes visible immediately (PutManifest invalidates), even without TTL expiry.
func TestCatalogCacheInvalidatedByPush(t *testing.T) {
	ctx := context.Background()
	b, _ := newCountingBackend(t)
	defer b.Close()
	b.now = func() time.Time { return time.Unix(1000, 0) } // frozen clock: TTL cannot expire

	putManifest(t, b, "library/a", "v1")
	if got, err := b.Repos(ctx); err != nil || !reflect.DeepEqual(got, []string{"library/a"}) {
		t.Fatalf("Repos warm-up = %v, err=%v", got, err)
	}

	// A new push must appear despite the frozen clock: invalidation, not TTL.
	putManifest(t, b, "library/c", "v1")
	got, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos after push: %v", err)
	}
	want := []string{"library/a", "library/c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Repos after push = %v, want %v", got, want)
	}
}

// TestCatalogCacheInvalidatedByDelete proves removing a repo's only manifest
// marker drops it from a warm catalog immediately.
func TestCatalogCacheInvalidatedByDelete(t *testing.T) {
	ctx := context.Background()
	b, _ := newCountingBackend(t)
	defer b.Close()
	b.now = func() time.Time { return time.Unix(1000, 0) }

	mt := "application/vnd.oci.image.manifest.v1+json"
	digest, err := b.PutManifest(ctx, "library/a", "v1", mt, []byte(`{"schemaVersion":2}`))
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	putManifest(t, b, "library/b", "v1")

	if got, _ := b.Repos(ctx); !reflect.DeepEqual(got, []string{"library/a", "library/b"}) {
		t.Fatalf("warm-up Repos = %v", got)
	}

	if err := b.DeleteManifest(ctx, "library/a", digest); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	got, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos after delete: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"library/b"}) {
		t.Fatalf("Repos after delete = %v, want [library/b]", got)
	}
}

// TestCatalogCacheTTLExpiry proves that once the TTL elapses, a repo added
// out-of-band (directly on the object store, bypassing invalidation) becomes
// visible -- i.e. the TTL is a real safety net for external mutations.
func TestCatalogCacheTTLExpiry(t *testing.T) {
	ctx := context.Background()
	b, _ := newCountingBackend(t)
	defer b.Close()

	clock := time.Unix(1000, 0)
	b.now = func() time.Time { return clock }

	putManifest(t, b, "library/a", "v1")
	if got, _ := b.Repos(ctx); !reflect.DeepEqual(got, []string{"library/a"}) {
		t.Fatalf("warm-up Repos = %v", got)
	}

	// Write a marker straight to the store, bypassing PutManifest's invalidation.
	extMarker := manifestKey("library/ext", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err := b.obj.Put(ctx, extMarker, strings.NewReader("mt"), 2); err != nil {
		t.Fatalf("out-of-band Put: %v", err)
	}

	// Still within TTL: the external repo is invisible.
	if got, _ := b.Repos(ctx); !reflect.DeepEqual(got, []string{"library/a"}) {
		t.Fatalf("within TTL Repos = %v, want [library/a] (cache should hide external write)", got)
	}

	// Advance past the TTL: the safety-net walk now discovers it.
	clock = clock.Add(defaultCatalogTTL + time.Second)
	got, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos after TTL: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"library/a", "library/ext"}) {
		t.Fatalf("after TTL Repos = %v, want [library/a library/ext]", got)
	}
}

// TestCatalogPaginationUnaffected confirms the registry-layer pagination still
// operates on the full sorted list the (now cached) Repos returns. Pagination
// lives in the registry package; here we assert Repos yields the complete sorted
// slice that pagination slices, both cold and warm.
func TestCatalogPaginationUnaffected(t *testing.T) {
	ctx := context.Background()
	b, _ := newCountingBackend(t)
	defer b.Close()
	b.now = func() time.Time { return time.Unix(1000, 0) }

	names := []string{"library/d", "library/a", "library/c", "library/b"}
	for _, n := range names {
		putManifest(t, b, n, "v1")
	}
	want := []string{"library/a", "library/b", "library/c", "library/d"}

	cold, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos cold: %v", err)
	}
	if !reflect.DeepEqual(cold, want) {
		t.Fatalf("cold Repos = %v, want %v", cold, want)
	}
	warm, err := b.Repos(ctx)
	if err != nil {
		t.Fatalf("Repos warm: %v", err)
	}
	if !reflect.DeepEqual(warm, want) {
		t.Fatalf("warm Repos = %v, want %v", warm, want)
	}

	// Simulate the registry paginate() slicing the full list: page of 2 then the
	// remainder, driven by the sorted order the cache preserves.
	page1 := warm[:2]
	page2 := warm[2:]
	if !reflect.DeepEqual(page1, []string{"library/a", "library/b"}) {
		t.Fatalf("page1 = %v", page1)
	}
	if !reflect.DeepEqual(page2, []string{"library/c", "library/d"}) {
		t.Fatalf("page2 = %v", page2)
	}
}
