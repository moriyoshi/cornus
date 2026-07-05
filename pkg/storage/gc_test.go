package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// putBlobString is a test helper that stores s and returns its digest.
func putBlobString(t *testing.T, b *Backend, s string) string {
	t.Helper()
	d, _, err := b.PutBlob(context.Background(), strings.NewReader(s), "")
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	return d
}

// putManifestJSON marshals v, stores it as a manifest tagged tag in repo, and
// returns the manifest digest.
func putManifestJSON(t *testing.T, b *Backend, repo, tag string, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	d, err := b.PutManifest(context.Background(), repo, tag, "application/vnd.oci.image.manifest.v1+json", data)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	return d
}

func TestDeleteBlob(t *testing.T) {
	for name, factory := range backendFactories(t) {
		factory := factory
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			b := factory()
			defer b.Close()

			d := putBlobString(t, b, "delete me")
			if err := b.DeleteBlob(ctx, d); err != nil {
				t.Fatalf("DeleteBlob: %v", err)
			}
			if _, err := b.StatBlob(ctx, d); err != ErrNotFound {
				t.Fatalf("blob still present after delete: %v", err)
			}
			if err := b.DeleteBlob(ctx, d); err != ErrNotFound {
				t.Fatalf("DeleteBlob(absent) = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestGC(t *testing.T) {
	for name, factory := range backendFactories(t) {
		factory := factory
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			b := factory()
			defer b.Close()

			// Reachable image: config + one layer, referenced by a tagged manifest.
			config := putBlobString(t, b, "config-bytes")
			layer := putBlobString(t, b, "layer-bytes")
			manifest := map[string]any{
				"schemaVersion": 2,
				"mediaType":     "application/vnd.oci.image.manifest.v1+json",
				"config":        map[string]any{"digest": config, "mediaType": "application/vnd.oci.image.config.v1+json"},
				"layers":        []map[string]any{{"digest": layer}},
			}
			manifestDigest := putManifestJSON(t, b, "app", "v1", manifest)

			// An index that references the image manifest (manifest list reachability).
			index := map[string]any{
				"schemaVersion": 2,
				"mediaType":     "application/vnd.oci.image.index.v1+json",
				"manifests":     []map[string]any{{"digest": manifestDigest}},
			}
			indexData, _ := json.Marshal(index)
			indexDigest, err := b.PutManifest(ctx, "app", "multi", "application/vnd.oci.image.index.v1+json", indexData)
			if err != nil {
				t.Fatalf("PutManifest index: %v", err)
			}

			// An orphan blob that nothing references.
			orphan := putBlobString(t, b, "orphan-garbage")

			freed, err := b.GC(ctx)
			if err != nil {
				t.Fatalf("GC: %v", err)
			}
			if freed != 1 {
				t.Fatalf("GC freed = %d, want 1", freed)
			}
			if _, err := b.StatBlob(ctx, orphan); err != ErrNotFound {
				t.Fatalf("orphan survived GC: %v", err)
			}
			for _, keep := range []string{config, layer, manifestDigest, indexDigest} {
				if _, err := b.StatBlob(ctx, keep); err != nil {
					t.Fatalf("reachable blob %s removed by GC: %v", keep, err)
				}
			}
		})
	}
}

// TestGCReclaimsOrphanedManifestBlob covers the leak the task targets: deleting a
// manifest membership marker leaves the manifest blob (and its now-unreferenced
// config/layers) behind; GC must reclaim them.
func TestGCReclaimsOrphanedManifestBlob(t *testing.T) {
	ctx := context.Background()
	b, err := Open(ctx, "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	config := putBlobString(t, b, "cfg")
	layer := putBlobString(t, b, "lyr")
	manifest := map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": config},
		"layers":        []map[string]any{{"digest": layer}},
	}
	md := putManifestJSON(t, b, "app", "v1", manifest)

	// Delete the manifest membership marker (as DELETE /manifests/<digest> does).
	if err := b.DeleteManifest(ctx, "app", md); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	// Also drop the dangling tag so it isn't a GC root.
	if err := b.obj.Delete(ctx, tagKey("app", "v1")); err != nil {
		t.Fatalf("delete tag: %v", err)
	}

	freed, err := b.GC(ctx)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if freed != 3 { // manifest + config + layer
		t.Fatalf("GC freed = %d, want 3", freed)
	}
	for _, d := range []string{md, config, layer} {
		if _, err := b.StatBlob(ctx, d); err != ErrNotFound {
			t.Fatalf("blob %s survived GC: %v", d, err)
		}
	}
}

// TestRepoWithManifestsPathSegment covers the walkRepos shadowing bug: a
// repository whose OCI name contains an intermediate "manifests" path component
// (a legal name) must still be discovered by Repos() and, crucially, have its
// blobs kept by GC rather than swept as unreachable.
func TestRepoWithManifestsPathSegment(t *testing.T) {
	for name, factory := range backendFactories(t) {
		factory := factory
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			b := factory()
			defer b.Close()

			config := putBlobString(t, b, "nested-config")
			layer := putBlobString(t, b, "nested-layer")
			manifest := map[string]any{
				"schemaVersion": 2,
				"config":        map[string]any{"digest": config},
				"layers":        []map[string]any{{"digest": layer}},
			}
			// A legal OCI name whose intermediate path component is "manifests".
			const repo = "team/manifests/api"
			md := putManifestJSON(t, b, repo, "v1", manifest)

			// The catalog must surface the nested repository.
			repos, err := b.Repos(ctx)
			if err != nil {
				t.Fatalf("Repos: %v", err)
			}
			found := false
			for _, r := range repos {
				if r == repo {
					found = true
				}
			}
			if !found {
				t.Fatalf("Repos = %v, missing %q (walkRepos shadowed it)", repos, repo)
			}

			// GC must not sweep the nested image's still-referenced blobs.
			if _, err := b.GC(ctx); err != nil {
				t.Fatalf("GC: %v", err)
			}
			for _, d := range []string{md, config, layer} {
				if _, err := b.StatBlob(ctx, d); err != nil {
					t.Fatalf("GC swept live blob %s of nested repo: %v", d, err)
				}
			}
		})
	}
}

// faultyGetStore wraps an ObjectStore and injects an error for Get on one key,
// simulating a transient (non-NotFound) read failure on an existing object.
type faultyGetStore struct {
	ObjectStore
	failKey string
	err     error
}

func (f *faultyGetStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == f.failKey {
		return nil, f.err
	}
	return f.ObjectStore.Get(ctx, key)
}

// TestGCAbortsOnManifestReadError covers the error-handling bug: a transient
// (non-NotFound) read failure on a real manifest blob must abort GC, not be
// treated as a leaf -- otherwise the manifest's still-referenced children are
// under-marked and swept.
func TestGCAbortsOnManifestReadError(t *testing.T) {
	ctx := context.Background()
	b, err := Open(ctx, "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	config := putBlobString(t, b, "cfg-bytes")
	layer := putBlobString(t, b, "layer-bytes")
	manifest := map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": config},
		"layers":        []map[string]any{{"digest": layer}},
	}
	md := putManifestJSON(t, b, "app", "v1", manifest)

	mkey, err := blobKey(md)
	if err != nil {
		t.Fatalf("blobKey: %v", err)
	}
	ioErr := errors.New("simulated transient I/O error")
	b.obj = &faultyGetStore{ObjectStore: b.obj, failKey: mkey, err: ioErr}

	freed, gcErr := b.GC(ctx)
	if gcErr == nil {
		t.Fatal("GC succeeded despite a transient manifest read error; children would be swept")
	}
	if !errors.Is(gcErr, ioErr) {
		t.Fatalf("GC error = %v, want wrapping the injected I/O error", gcErr)
	}
	if freed != 0 {
		t.Fatalf("GC freed = %d on aborted run, want 0", freed)
	}
	// The manifest's still-referenced config and layer must survive.
	for _, d := range []string{config, layer} {
		if _, err := b.StatBlob(ctx, d); err != nil {
			t.Fatalf("referenced blob %s deleted after aborted GC: %v", d, err)
		}
	}
}

func TestSweepStaleUploads(t *testing.T) {
	ctx := context.Background()
	staging := t.TempDir()
	b, err := Open(ctx, t.TempDir(), staging)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	// A fresh upload session must survive the sweep.
	u, err := b.NewUpload(ctx)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	if _, err := u.Write(ctx, strings.NewReader("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	u.Close()
	freshID := u.ID()

	// A stale session file and a stale put temp: back-date their mtimes.
	stale := filepath.Join(staging, "upload-staleid")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	staleTemp := filepath.Join(staging, "put-1234")
	if err := os.WriteFile(staleTemp, []byte("y"), 0o644); err != nil {
		t.Fatalf("write stale temp: %v", err)
	}
	// A non-staging file must never be touched.
	keep := filepath.Join(staging, "important.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{stale, staleTemp, keep} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	removed, err := b.SweepStaleUploads(24 * time.Hour)
	if err != nil {
		t.Fatalf("SweepStaleUploads: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale session survived: %v", err)
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Fatalf("stale temp survived: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("non-staging file removed: %v", err)
	}
	// The fresh session is still resumable.
	if _, err := b.GetUpload(ctx, freshID); err != nil {
		t.Fatalf("fresh session reaped: %v", err)
	}
}

func TestReferrers(t *testing.T) {
	ctx := context.Background()
	b, err := Open(ctx, "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	subject := putManifestJSON(t, b, "app", "v1", map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": putBlobString(t, b, "cfg")},
	})

	// A signature referrer and an SBOM referrer, both with subject == the image.
	sig := putManifestJSON(t, b, "app", "sig", map[string]any{
		"schemaVersion": 2,
		"artifactType":  "application/vnd.example.signature",
		"config":        map[string]any{"digest": putBlobString(t, b, "sigcfg")},
		"subject":       map[string]any{"digest": subject},
	})
	sbom := putManifestJSON(t, b, "app", "sbom", map[string]any{
		"schemaVersion": 2,
		"artifactType":  "application/vnd.example.sbom",
		"config":        map[string]any{"digest": putBlobString(t, b, "sbomcfg")},
		"subject":       map[string]any{"digest": subject},
	})

	refs, err := b.Referrers(ctx, "app", subject)
	if err != nil {
		t.Fatalf("Referrers: %v", err)
	}
	got := map[string]string{}
	for _, d := range refs {
		got[d.Digest] = d.ArtifactType
	}
	if got[sig] != "application/vnd.example.signature" {
		t.Fatalf("signature referrer missing/wrong: %v", got)
	}
	if got[sbom] != "application/vnd.example.sbom" {
		t.Fatalf("sbom referrer missing/wrong: %v", got)
	}

	// A subject with no referrers yields an empty list, not an error.
	empty, err := b.Referrers(ctx, "app", putBlobString(t, b, "unrelated"))
	if err != nil {
		t.Fatalf("Referrers(no refs): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no referrers, got %v", empty)
	}

	// An invalid subject digest is an error.
	if _, err := b.Referrers(ctx, "app", "not-a-digest"); err == nil {
		t.Fatal("Referrers(bad digest) = nil error, want error")
	}
}
