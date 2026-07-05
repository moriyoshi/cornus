//go:build cloudblob

package storage

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// TestGCSBackend and TestAzblobBackend exercise the gs:// / azblob:// backends end
// to end against a real (or emulated) endpoint. They are opt-in and only exist in a
// `-tags cloudblob` build (which registers the drivers):
//
//	# Google Cloud Storage (real, or fake-gcs-server via STORAGE_EMULATOR_HOST):
//	CORNUS_TEST_GCS='gs://my-bucket' go test -tags cloudblob ./pkg/storage/ -run GCS -v
//	# Azure Blob (real, or Azurite via the standard AZURE_STORAGE_* / connection env):
//	CORNUS_TEST_AZBLOB='azblob://my-container' go test -tags cloudblob ./pkg/storage/ -run Azblob -v
//
// Credentials come from the standard cloud credential chains (GOOGLE_APPLICATION_CREDENTIALS,
// AZURE_STORAGE_ACCOUNT / _KEY, etc.); the emulators are picked up via their standard env.
func TestGCSBackend(t *testing.T) {
	ref := os.Getenv("CORNUS_TEST_GCS")
	if ref == "" {
		t.Skip("set CORNUS_TEST_GCS (e.g. gs://bucket) to run the GCS integration test")
	}
	runCloudBlobRoundTrip(t, ref)
}

func TestAzblobBackend(t *testing.T) {
	ref := os.Getenv("CORNUS_TEST_AZBLOB")
	if ref == "" {
		t.Skip("set CORNUS_TEST_AZBLOB (e.g. azblob://container) to run the Azure Blob integration test")
	}
	runCloudBlobRoundTrip(t, ref)
}

// runCloudBlobRoundTrip drives the full registry surface (blob put/stat/get,
// manifest+tag put/get, List-backed Tags/Repos, delete) against a gocloud bucket ref.
func runCloudBlobRoundTrip(t *testing.T, ref string) {
	t.Helper()
	ctx := context.Background()
	b, err := Open(ctx, ref, t.TempDir())
	if err != nil {
		t.Fatalf("open %q: %v", ref, err)
	}
	defer b.Close()

	content := "cornus over " + ref
	digest, size, err := b.PutBlob(ctx, strings.NewReader(content), "")
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if sz, err := b.StatBlob(ctx, digest); err != nil || sz != size {
		t.Fatalf("StatBlob = %d, %v", sz, err)
	}
	rc, err := b.GetBlob(ctx, digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != content {
		t.Fatalf("GetBlob = %q", data)
	}

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	mt := "application/vnd.oci.image.manifest.v1+json"
	mdigest, err := b.PutManifest(ctx, "team/app", "v1", mt, manifest)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	got, d, gotMT, err := b.GetManifest(ctx, "team/app", "v1")
	if err != nil || string(got) != string(manifest) || d != mdigest || gotMT != mt {
		t.Fatalf("GetManifest = %q, %s, %q, %v", got, d, gotMT, err)
	}
	tags, err := b.Tags(ctx, "team/app")
	if err != nil || len(tags) != 1 || tags[0] != "v1" {
		t.Fatalf("Tags = %v, %v", tags, err)
	}
	if repos, err := b.Repos(ctx); err != nil || len(repos) == 0 {
		t.Fatalf("Repos = %v, %v", repos, err)
	}
	if err := b.DeleteManifest(ctx, "team/app", mdigest); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
}
